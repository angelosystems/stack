package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type Manifest struct {
	Repos map[string]RepoConfig `yaml:"repos"`
}

type RepoConfig struct {
	Path     string          `yaml:"path"`
	Services []ServiceConfig `yaml:"services"`
}

type ServiceConfig struct {
	Name         string `yaml:"name"`
	DeployScript string `yaml:"deploy_script"`
	HealthProbe  string `yaml:"health_probe"`
	Env          string `yaml:"env"`
}

func loadManifest(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var m Manifest
	if err := yaml.NewDecoder(f).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func cmdDeployReactor() *cobra.Command {
	var port string
	var manifestPath string

	c := &cobra.Command{
		Use:   "deploy-reactor",
		Short: "Startet den Deploy-on-Merge-Reactor (CD-Kern)",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()

			// Fallback: If manifest path is empty, check standard paths
			if manifestPath == "" {
				if _, err := os.Stat("/opt/stack/deploy-manifest.yaml"); err == nil {
					manifestPath = "/opt/stack/deploy-manifest.yaml"
				} else {
					manifestPath = "deploy-manifest.yaml"
				}
			}

			fmt.Println(fmt.Sprintf("Deploy-on-Merge-Reactor startet. Manifest: %s, Port: %s", manifestPath, port))

			http.HandleFunc("/api/github-webhook", makeGithubWebhookHandler(p, manifestPath))

			return http.ListenAndServe(":"+port, nil)
		},
	}

	c.Flags().StringVar(&port, "port", "7790", "Reactor HTTP Port")
	c.Flags().StringVar(&manifestPath, "manifest", "", "Pfad zum Deploy-Manifest")
	return c
}

func makeGithubWebhookHandler(p *pgxpool.Pool, manifestPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}

		secret := envOr("GITHUB_WEBHOOK_SECRET", "")
		if secret == "" {
			http.Error(w, "webhook nicht konfiguriert (GITHUB_WEBHOOK_SECRET fehlt)", 503)
			return
		}

		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		// HMAC verification
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(raw)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(r.Header.Get("X-Hub-Signature-256")), []byte(want)) {
			http.Error(w, "bad signature", 401)
			return
		}

		githubEvent := r.Header.Get("X-GitHub-Event")
		var repoName string
		var sha string
		var ref string

		if githubEvent == "push" {
			var ev struct {
				Ref        string `json:"ref"`
				After      string `json:"after"`
				Repository struct {
					FullName string `json:"full_name"`
				} `json:"repository"`
			}
			if err := json.Unmarshal(raw, &ev); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			ref = ev.Ref
			if ref != "refs/heads/main" && ref != "refs/heads/master" {
				fmt.Fprintln(w, fmt.Sprintf(`{"ok":true,"skipped":"branch %s"}`, ref))
				return
			}
			sha = ev.After
			repoName = ev.Repository.FullName
		} else if githubEvent == "pull_request" {
			var ev struct {
				Action      string `json:"action"`
				PullRequest struct {
					Merged         bool   `json:"merged"`
					MergeCommitSha string `json:"merge_commit_sha"`
					Base           struct {
						Ref string `json:"ref"`
					} `json:"base"`
				} `json:"pull_request"`
				Repository struct {
					FullName string `json:"full_name"`
				} `json:"repository"`
			}
			if err := json.Unmarshal(raw, &ev); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if ev.Action != "closed" || !ev.PullRequest.Merged {
				fmt.Fprintln(w, `{"ok":true,"skipped":"PR not merged"}`)
				return
			}
			ref = ev.PullRequest.Base.Ref
			if ref != "main" && ref != "master" {
				fmt.Fprintln(w, fmt.Sprintf(`{"ok":true,"skipped":"PR base branch %s"}`, ref))
				return
			}
			sha = ev.PullRequest.MergeCommitSha
			repoName = ev.Repository.FullName
		} else {
			fmt.Fprintln(w, fmt.Sprintf(`{"ok":true,"skipped":"event %s"}`, githubEvent))
			return
		}

		if sha == "" || strings.HasPrefix(sha, "000000") {
			fmt.Fprintln(w, `{"ok":true,"skipped":"invalid sha"}`)
			return
		}

		fmt.Println(fmt.Sprintf("Empfangenes Merge-Event für Repo %s bei SHA %s", repoName, sha))

		// Manifest laden
		m, err := loadManifest(manifestPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("Manifest Fehler: %v", err), 500)
			return
		}

		repoCfg, exists := m.Repos[repoName]
		if !exists {
			fmt.Fprintln(w, fmt.Sprintf(`{"ok":true,"skipped":"repo %s nicht im Manifest"}`, repoName))
			return
		}

		// Find associated initiative
		initiativeID := "sk-cicd-stack-tooling" // Default/Fallback
		if p != nil {
			row := p.QueryRow(r.Context(),
				`SELECT DISTINCT initiative_id FROM portfolio.initiative_link WHERE ref LIKE $1 LIMIT 1`,
				repoName+"%")
			if err := row.Scan(&initiativeID); err != nil {
				initiativeID = "sk-cicd-stack-tooling" // Fallback
			}
		}

		// 1. Fetch & Check for database migrations/schema changes
		fmt.Println(fmt.Sprintf("Hole Updates für Repo in %s...", repoCfg.Path))
		fetchCmd := exec.Command("git", "-C", repoCfg.Path, "fetch", "origin")
		if out, err := fetchCmd.CombinedOutput(); err != nil {
			logDeployEvent(p, initiativeID, "failed", "staging", "github", repoName, sha,
				fmt.Sprintf(`{"error":"git fetch failed: %s"}`, strings.ReplaceAll(string(out), `"`, `"`)))
			http.Error(w, fmt.Sprintf("git fetch failed: %v: %s", err, out), 500)
			return
		}

		// Check changed files
		diffCmd := exec.Command("git", "-C", repoCfg.Path, "show", "--name-only", "--oneline", sha)
		diffOut, err := diffCmd.CombinedOutput()
		if err != nil {
			logDeployEvent(p, initiativeID, "failed", "staging", "github", repoName, sha,
				fmt.Sprintf(`{"error":"git show failed: %s"}`, strings.ReplaceAll(string(diffOut), `"`, `"`)))
			http.Error(w, fmt.Sprintf("git show failed: %v: %s", err, diffOut), 500)
			return
		}

		hasMigration := false
		lines := strings.Split(string(diffOut), "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "schema/") || strings.HasPrefix(trimmed, "migrations/") {
				hasMigration = true
				break
			}
		}

		if hasMigration {
			fmt.Println(fmt.Sprintf("BLOCK: Migration/Schema-Änderung in %s erkannt. Automatischer Deploy gestoppt.", sha))
			if p != nil {
				payload, _ := json.Marshal(map[string]any{
					"repo":        repoName,
					"sha":         sha,
					"status":      "blocked_migrations",
					"message":     "Datenbank-Migration erkannt. Manueller Eingriff erforderlich.",
					"needs-human": true,
				})
				_, _ = p.Exec(r.Context(),
					`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor) VALUES ($1,'deployed','github',$2,'deploy-reactor')`,
					initiativeID, payload)
			}
			fmt.Fprintln(w, `{"ok":true,"status":"blocked","reason":"migrations"}`)
			return
		}

		// 2. Code auschecken
		fmt.Println(fmt.Sprintf("Checke SHA %s aus...", sha))
		checkoutCmd := exec.Command("git", "-C", repoCfg.Path, "checkout", sha)
		if out, err := checkoutCmd.CombinedOutput(); err != nil {
			logDeployEvent(p, initiativeID, "failed", "staging", "github", repoName, sha,
				fmt.Sprintf(`{"error":"git checkout failed: %s"}`, strings.ReplaceAll(string(out), `"`, `"`)))
			http.Error(w, fmt.Sprintf("git checkout failed: %v: %s", err, out), 500)
			return
		}

		// 3. Pro Service deployen
		for _, svc := range repoCfg.Services {
			fmt.Println(fmt.Sprintf("Deploye Service %s...", svc.Name))

			// Determine env
			env := svc.Env
			if env == "" {
				if strings.Contains(svc.Name, "staging") {
					env = "staging"
				} else if strings.Contains(svc.Name, "prod") {
					env = "prod"
				} else {
					env = "staging"
				}
			}

			// Backup current binary (if possible)
			binPath := envOr("MASTER_KANBAN_BIN_PATH", "/opt/stack/bin/master-kanban")
			backupPath := binPath + ".prev"
			backedUp := false
			if _, err := os.Stat(binPath); err == nil {
				backupCmd := exec.Command("cp", binPath, backupPath)
				if err := backupCmd.Run(); err == nil {
					backedUp = true
				}
			}

			// Script ausführen
			scriptPath := filepath.Join(repoCfg.Path, svc.DeployScript)
			fmt.Println(fmt.Sprintf("Führe Deploy-Script aus: %s %s", scriptPath, sha))
			runCmd := exec.Command(scriptPath, sha)
			if out, err := runCmd.CombinedOutput(); err != nil {
				fmt.Println(fmt.Sprintf("Deploy-Script Fehler: %v Output: %s", err, out))
				logDeployEvent(p, initiativeID, "failed", env, "github", repoName, sha,
					fmt.Sprintf(`{"service":"%s","error":"deploy script failed: %s"}`, svc.Name, strings.ReplaceAll(string(out), `"`, `"`)))
				http.Error(w, fmt.Sprintf("deploy script failed: %v: %s", err, out), 500)
				return
			}

			// Service restarten
			fmt.Println(fmt.Sprintf("Restarte Systemd Service %s...", svc.Name))
			restartCmd := exec.Command("systemctl", "restart", svc.Name)
			if out, err := restartCmd.CombinedOutput(); err != nil {
				fmt.Println(fmt.Sprintf("Restart Fehler: %v Output: %s", err, out))
				logDeployEvent(p, initiativeID, "failed", env, "github", repoName, sha,
					fmt.Sprintf(`{"service":"%s","error":"systemctl restart failed: %s"}`, svc.Name, strings.ReplaceAll(string(out), `"`, `"`)))
				http.Error(w, fmt.Sprintf("systemctl restart failed: %v: %s", err, out), 500)
				return
			}

			// Health Probe (mit Retries)
			probeSuccess := false
			retrySleep := 1 * time.Second
			if os.Getenv("DEPLOY_REACTOR_TEST") != "" {
				retrySleep = 1 * time.Millisecond
			}
			for i := 0; i < 15; i++ {
				time.Sleep(retrySleep)
				fmt.Println(fmt.Sprintf("Health Check %s (Versuch %d/15)...", svc.HealthProbe, i+1))
				
				func() {
					resp, err := http.Get(svc.HealthProbe)
					if err != nil {
						fmt.Println(fmt.Sprintf("Health check connection failed: %v", err))
						return
					}
					defer resp.Body.Close()

					if resp.StatusCode == 200 {
						var body struct {
							Version string `json:"version"`
						}
						if err := json.NewDecoder(resp.Body).Decode(&body); err == nil {
							if strings.HasPrefix(sha, body.Version) || strings.HasPrefix(body.Version, sha) || body.Version == sha {
								// Base version check passed. Now perform Feature-Smoke!
								smokeURL := strings.Replace(svc.HealthProbe, "/api/version", "/api/initiatives", 1)
								if smokeURL != svc.HealthProbe {
									fmt.Println(fmt.Sprintf("Running Feature-Smoke Check on %s...", smokeURL))
									smokeResp, err := http.Get(smokeURL)
									if err == nil {
										defer smokeResp.Body.Close()
										if smokeResp.StatusCode == 200 {
											var initiatives []any
											if err := json.NewDecoder(smokeResp.Body).Decode(&initiatives); err == nil {
												probeSuccess = true
												fmt.Println("Feature-Smoke Check passed successfully!")
											} else {
												fmt.Println("Feature-Smoke Check failed: invalid JSON response")
											}
										} else {
											fmt.Println(fmt.Sprintf("Feature-Smoke Check failed: status %d", smokeResp.StatusCode))
										}
									} else {
										fmt.Println(fmt.Sprintf("Feature-Smoke Check failed to connect: %v", err))
									}
								} else {
									// No Feature-Smoke URL pattern matched, default to success of basic probe
									probeSuccess = true
								}
							} else {
								fmt.Println(fmt.Sprintf("Version mismatch: erwartet %s, erhalten %s", sha, body.Version))
							}
						} else {
							fmt.Println(fmt.Sprintf("Failed to decode version json: %v", err))
						}
					} else {
						fmt.Println(fmt.Sprintf("Health check status mismatch: expected 200, got %d", resp.StatusCode))
					}
				}()

				if probeSuccess {
					break
				}
			}

			if !probeSuccess {
				fmt.Println("Health Check fehlgeschlagen! Rollback einleiten...")
				if backedUp {
					rollbackCmd := exec.Command("mv", backupPath, binPath)
					_ = rollbackCmd.Run()
					restartSvcCmd := exec.Command("systemctl", "restart", svc.Name)
					_ = restartSvcCmd.Run()
				}
				logDeployEvent(p, initiativeID, "rolled-back", env, "github", repoName, sha,
					fmt.Sprintf(`{"service":"%s","error":"health probe/feature smoke failed","rolled_back":true}`, svc.Name))
				http.Error(w, "health probe/feature smoke failed, rolled back", 500)
				return
			}

			fmt.Println(fmt.Sprintf("Service %s erfolgreich deployt!", svc.Name))
			if p != nil {
				payload, _ := json.Marshal(map[string]any{
					"repo":        repoName,
					"sha":         sha,
					"status":      "healthy",
					"health":      "healthy",
					"env":         env,
					"service":     svc.Name,
					"probe_url":   svc.HealthProbe,
					"deployed_at": time.Now().Format(time.RFC3339),
				})
				_, _ = p.Exec(r.Context(),
					`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor) VALUES ($1,'deployed','github',$2,'deploy-reactor')`,
					initiativeID, payload)
			}
		}

		fmt.Fprintln(w, fmt.Sprintf(`{"ok":true,"status":"deployed","sha":"%s"}`, sha))
	}
}

func logDeployEvent(p *pgxpool.Pool, initiativeID, status, env, sourceBackend, repo, sha, errorPayload string) {
	if p == nil {
		return
	}
	var payloadMap map[string]any
	if errorPayload != "" {
		_ = json.Unmarshal([]byte(errorPayload), &payloadMap)
	}
	if payloadMap == nil {
		payloadMap = map[string]any{}
	}
	payloadMap["repo"] = repo
	payloadMap["sha"] = sha
	payloadMap["status"] = status
	payloadMap["health"] = status
	payloadMap["env"] = env
	payloadMap["timestamp"] = time.Now().Format(time.RFC3339)

	payloadBytes, _ := json.Marshal(payloadMap)
	_, _ = p.Exec(context.Background(),
		`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor) VALUES ($1, 'deployed', $2, $3, 'deploy-reactor')`,
		initiativeID, sourceBackend, payloadBytes)
}
