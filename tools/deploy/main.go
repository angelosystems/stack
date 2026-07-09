// deploy — Schaltzentrale-Wrapper: steuert Container-Stacks (docker context über
// SSH) und native systemd-Units auf den Estate-Boxen von werkstatt aus.
//
//	deploy ls                              Boxen + Context-Health
//	deploy setup                           docker-Contexts (re)registrieren
//	deploy doctor                          tiefer Check (ssh + docker reachability)
//	deploy <box> ps                        alle Container auf der Box
//	deploy <box> <stack> <action>          Stack steuern
//
// action ∈ up|down|restart|pull|logs|ps. Ein Stack ist ein Compose-Stack, wenn
// <composeDir>/<box>/<stack>/compose.yaml existiert — sonst wird <stack> als
// native systemd-Unit behandelt (ssh <box> systemctl …). Convention over config.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

type box struct {
	Host    string `json:"host"`    // ssh target, z.B. root@1.2.3.4
	Context string `json:"context"` // docker context name
}

type config struct {
	ComposeDir string         `json:"composeDir"`
	Boxes      map[string]box `json:"boxes"`
}

func configPath() string {
	if p := os.Getenv("DEPLOY_CONFIG"); p != "" {
		return p
	}
	return "/opt/stack/tools/deploy/deploy.json"
}

func loadConfig() (*config, error) {
	b, err := os.ReadFile(configPath())
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", configPath(), err)
	}
	var c config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("config %s: %w", configPath(), err)
	}
	if c.ComposeDir == "" {
		c.ComposeDir = "/opt/stack/deploys"
	}
	return &c, nil
}

func (c *config) box(name string) (box, error) {
	b, ok := c.Boxes[name]
	if !ok {
		return box{}, fmt.Errorf("unbekannte Box %q — bekannt: %s", name, strings.Join(c.boxNames(), ", "))
	}
	return b, nil
}

func (c *config) boxNames() []string {
	names := make([]string, 0, len(c.Boxes))
	for n := range c.Boxes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// composeFile findet die Compose-Datei für einen Stack, oder "" wenn keine da ist
// (dann ist der Stack eine native systemd-Unit).
func (c *config) composeFile(boxName, stack string) string {
	dir := filepath.Join(c.ComposeDir, boxName, stack)
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// run führt ein Kommando aus und streamt stdio durch. Exit-Code wird in main
// durchgereicht.
func run(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	return c.Run()
}

// capture führt ein Kommando aus und gibt stdout zurück (für Health-Checks).
func capture(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

// --- Compose- vs. native-Action-Mapping ---

func composeArgs(action string) ([]string, error) {
	switch action {
	case "up":
		return []string{"up", "-d"}, nil
	case "down":
		return []string{"down"}, nil
	case "restart":
		return []string{"restart"}, nil
	case "pull":
		return []string{"pull"}, nil
	case "logs":
		return []string{"logs", "--tail=200", "-f"}, nil
	case "ps":
		return []string{"ps"}, nil
	default:
		return nil, fmt.Errorf("unbekannte action %q (up|down|restart|pull|logs|ps)", action)
	}
}

func nativeArgs(action, unit string) ([]string, error) {
	switch action {
	case "up":
		return []string{"systemctl", "start", unit}, nil
	case "down":
		return []string{"systemctl", "stop", unit}, nil
	case "restart":
		return []string{"systemctl", "restart", unit}, nil
	case "ps":
		return []string{"systemctl", "status", unit, "--no-pager"}, nil
	case "logs":
		return []string{"journalctl", "-u", unit, "-n", "200", "-f"}, nil
	case "pull":
		return nil, fmt.Errorf("action %q gibt es für native Units (%s) nicht", action, unit)
	default:
		return nil, fmt.Errorf("unbekannte action %q (up|down|restart|pull|logs|ps)", action)
	}
}

// --- Dispatch: deploy <box> [<stack>] <action> ---

func dispatch(cfg *config, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: deploy <box> <stack> <action>  |  deploy <box> ps")
	}
	boxName := args[0]
	b, err := cfg.box(boxName)
	if err != nil {
		return err
	}

	// Box-Ebene: deploy <box> ps  → alle Container auf der Box
	if len(args) == 2 {
		action := args[1]
		if action != "ps" {
			return fmt.Errorf("auf Box-Ebene nur 'ps' — für Stacks: deploy %s <stack> <action>", boxName)
		}
		return run("docker", "--context", b.Context, "ps")
	}

	// Stack-Ebene: deploy <box> <stack> <action>
	stack, action := args[1], args[2]
	if cf := cfg.composeFile(boxName, stack); cf != "" {
		ca, err := composeArgs(action)
		if err != nil {
			return err
		}
		full := append([]string{"--context", b.Context, "compose", "-f", cf, "-p", stack}, ca...)
		return run("docker", full...)
	}

	// kein Compose-File → native systemd-Unit über ssh
	na, err := nativeArgs(action, stack)
	if err != nil {
		return err
	}
	full := append([]string{b.Host}, na...)
	return run("ssh", full...)
}

func main() {
	cfg, cfgErr := loadConfig()

	root := &cobra.Command{
		Use:           "deploy",
		Short:         "Schaltzentrale: Container-Stacks + native Units auf den Estate-Boxen steuern",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cfgErr != nil {
				return cfgErr
			}
			if len(args) == 0 {
				return cmd.Help()
			}
			return dispatch(cfg, args)
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true

	root.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "Boxen auflisten + Context-Health prüfen",
		RunE: func(cmd *cobra.Command, args []string) error {
			if cfgErr != nil {
				return cfgErr
			}
			fmt.Printf("%-18s %-26s %-18s %s\n", "BOX", "HOST", "CONTEXT", "DOCKER")
			for _, name := range cfg.boxNames() {
				b := cfg.Boxes[name]
				status := "✗ nicht erreichbar"
				if ver, err := capture("docker", "--context", b.Context, "info", "--format", "{{.ServerVersion}}"); err == nil && ver != "" {
					status = "✓ " + ver
				} else if _, err := capture("docker", "context", "inspect", b.Context); err != nil {
					status = "– kein Context (deploy setup)"
				}
				fmt.Printf("%-18s %-26s %-18s %s\n", name, b.Host, b.Context, status)
			}
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "setup",
		Short: "docker-Contexts aus der Config (re)registrieren (idempotent)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if cfgErr != nil {
				return cfgErr
			}
			for _, name := range cfg.boxNames() {
				b := cfg.Boxes[name]
				endpoint := "host=ssh://" + b.Host
				if _, err := capture("docker", "context", "inspect", b.Context); err == nil {
					if err := run("docker", "context", "update", b.Context, "--docker", endpoint); err != nil {
						return err
					}
					fmt.Printf("updated  %s → %s\n", b.Context, endpoint)
				} else {
					if err := run("docker", "context", "create", b.Context, "--docker", endpoint); err != nil {
						return err
					}
					fmt.Printf("created  %s → %s\n", b.Context, endpoint)
				}
			}
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "doctor",
		Short: "tiefer Check: ssh-Erreichbarkeit + docker-Daemon je Box",
		RunE: func(cmd *cobra.Command, args []string) error {
			if cfgErr != nil {
				return cfgErr
			}
			for _, name := range cfg.boxNames() {
				b := cfg.Boxes[name]
				fmt.Printf("== %s (%s) ==\n", name, b.Host)
				if out, err := capture("ssh", "-o", "ConnectTimeout=8", b.Host, "true"); err == nil {
					fmt.Println("  ssh:    ✓")
					_ = out
				} else {
					fmt.Printf("  ssh:    ✗ %v\n", err)
					continue
				}
				if ver, err := capture("docker", "--context", b.Context, "info", "--format", "{{.ServerVersion}}"); err == nil && ver != "" {
					fmt.Printf("  docker: ✓ %s\n", ver)
				} else {
					fmt.Println("  docker: ✗ Daemon nicht erreichbar (docker installiert?)")
				}
			}
			return nil
		},
	})

	root.AddCommand(promoteCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "deploy:", err)
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		os.Exit(1)
	}
}
