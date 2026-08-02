package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// DiscoveryRule corresponds to portfolio.provider_discovery
type DiscoveryRule struct {
	ProcessPattern  *string
	ExecutorPattern *string
	ModelPattern    *string
	ProviderBucket  string
	Priority        int
	Description     *string
}

// TokenUsage holds aggregated token usage metrics
type TokenUsage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	OverloadEvents      int64
	RequestCount        int64
}

// Matches checks if the rule matches the given process, executor, and model
func (r DiscoveryRule) Matches(process, executor, model string) bool {
	if r.ProcessPattern != nil {
		if !strings.EqualFold(*r.ProcessPattern, process) {
			return false
		}
	}
	if r.ExecutorPattern != nil {
		if !strings.EqualFold(*r.ExecutorPattern, executor) {
			return false
		}
	}
	if r.ModelPattern != nil {
		if !strings.Contains(strings.ToLower(model), strings.ToLower(*r.ModelPattern)) {
			return false
		}
	}
	return true
}

// ExtractPEM classifies process + executor from the file PATH only.
// Content-substring matching was removed (PRD fabrik-token-usage-tracking WP-1 #5):
// first-20-lines content scan caused transcripts that mention "flows"/"gemini"/
// "opencode" in their text to be mis-bucketed. The model is supplied separately
// by FirstModel (which scans the whole file, not just the head).
func ExtractPEM(filePath string) (process, executor string) {
	process = "claude"
	executor = ""

	pathLower := strings.ToLower(filePath)

	if strings.Contains(pathLower, "paperclip-worker") {
		process = "paperclip-worker"
	} else if strings.Contains(pathLower, "gemini") {
		process = "gemini"
	} else if strings.Contains(pathLower, "opencode") {
		process = "opencode"
	} else if strings.Contains(pathLower, "claude") {
		process = "claude"
	}

	if strings.Contains(pathLower, "flows") {
		executor = "flows"
	}

	return process, executor
}

// FirstModel returns the first `message.model` value found in the transcript
// file, lowercased. Falls back to the first top-level `model` field (rare —
// init/system lines) if no message.model exists. Scans the whole file — the
// model field can appear after line 20 in GLM transcripts (PRD WP-1 #5).
// Empty string if no model field at all.
func FirstModel(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024*10)

	topLevelFallback := ""
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, `"model"`) {
			continue
		}
		var probe struct {
			Model   *string `json:"model"`
			Message *struct {
				Model *string `json:"model"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			continue
		}
		// message.model is authoritative — it's the model the API was called with.
		if probe.Message != nil && probe.Message.Model != nil && *probe.Message.Model != "" {
			return strings.ToLower(*probe.Message.Model)
		}
		if topLevelFallback == "" && probe.Model != nil && *probe.Model != "" {
			topLevelFallback = strings.ToLower(*probe.Model)
		}
	}
	return topLevelFallback
}

// ParseTranscriptFile reads a transcript file from storedOffset, parses its lines, and extracts metrics
func ParseTranscriptFile(path string, storedOffset int64, rules []DiscoveryRule) (usage TokenUsage, newOffset int64, matchedBucket string, err error) {
	matchedBucket = "other"
	newOffset = storedOffset

	// Classify from path + first model occurrence (whole-file scan, not just head).
	proc, exec := ExtractPEM(path)
	md := FirstModel(path)
	for _, rule := range rules {
		if rule.Matches(proc, exec, md) {
			matchedBucket = rule.ProviderBucket
			break
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return usage, storedOffset, matchedBucket, err
	}
	defer f.Close()

	// Seek back to storedOffset to parse increment
	_, err = f.Seek(storedOffset, io.SeekStart)
	if err != nil {
		return usage, storedOffset, matchedBucket, err
	}

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024*10) // up to 10MB line buffer

	// Track bytes consumed via the actual file position, not a +1-per-line
	// heuristic — files without a trailing newline would otherwise cause
	// newOffset to overshoot info.Size() by 1 byte, which then triggers the
	// `info.Size() < storedOffset` reset on every subsequent run and re-parses
	// the whole file (idempotency leak).
	startOffset, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return usage, storedOffset, matchedBucket, err
	}
	for scanner.Scan() {
		line := scanner.Text()

		// 1. Tokens usage
		if strings.Contains(line, `"usage":`) {
			var lineData struct {
				Usage *struct {
					InputTokens         *int64 `json:"input_tokens"`
					OutputTokens        *int64 `json:"output_tokens"`
					CacheCreationTokens *int64 `json:"cache_creation_input_tokens"`
					CacheReadTokens     *int64 `json:"cache_read_input_tokens"`
				} `json:"usage"`
				Message *struct {
					Usage *struct {
						InputTokens         *int64 `json:"input_tokens"`
						OutputTokens        *int64 `json:"output_tokens"`
						CacheCreationTokens *int64 `json:"cache_creation_input_tokens"`
						CacheReadTokens     *int64 `json:"cache_read_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(line), &lineData); err == nil {
				var u *struct {
					InputTokens         *int64 `json:"input_tokens"`
					OutputTokens        *int64 `json:"output_tokens"`
					CacheCreationTokens *int64 `json:"cache_creation_input_tokens"`
					CacheReadTokens     *int64 `json:"cache_read_input_tokens"`
				}
				if lineData.Usage != nil {
					u = lineData.Usage
				} else if lineData.Message != nil && lineData.Message.Usage != nil {
					u = lineData.Message.Usage
				}

				if u != nil {
					usage.RequestCount++
					if u.InputTokens != nil {
						usage.InputTokens += *u.InputTokens
					}
					if u.OutputTokens != nil {
						usage.OutputTokens += *u.OutputTokens
					}
					if u.CacheCreationTokens != nil {
						usage.CacheCreationTokens += *u.CacheCreationTokens
					}
					if u.CacheReadTokens != nil {
						usage.CacheReadTokens += *u.CacheReadTokens
					}
				}
			}
		}

		// 2. Overload / rate limit events
		if strings.Contains(line, `"subtype":"api_error"`) {
			var errData struct {
				Subtype string `json:"subtype"`
				Error   *struct {
					Status int `json:"status"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(line), &errData); err == nil {
				if errData.Subtype == "api_error" && errData.Error != nil {
					if errData.Error.Status == 429 || errData.Error.Status == 529 {
						usage.OverloadEvents++
					}
				}
			} else {
				if strings.Contains(line, "overloaded_error") || strings.Contains(line, "rate_limit_error") {
					usage.OverloadEvents++
				}
			}
		} else if strings.Contains(line, "overloaded_error") || strings.Contains(line, "rate_limit_error") {
			usage.OverloadEvents++
		}
	}

	newOffset = storedOffset
	if pos, err := f.Seek(0, io.SeekCurrent); err == nil {
		newOffset = pos
	} else {
		// Fallback: should not happen for a regular file, but keep the parser
		// forward-only rather than failing the whole walk.
		newOffset = startOffset + int64(len(scanner.Bytes()))
	}
	return usage, newOffset, matchedBucket, nil
}

func cmdFleetParse() *cobra.Command {
	return &cobra.Command{
		Use:   "fleet-parse",
		Short: "Inkrementelles Parsen von Agenten-Transkripten (.jsonl)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			p := connect()
			defer p.Close()

			// 1. Load Discovery Rules
			var rules []DiscoveryRule
			rows, err := p.Query(ctx, `
				SELECT process_pattern, executor_pattern, model_pattern, provider_bucket, priority
				FROM portfolio.provider_discovery
				ORDER BY priority DESC
			`)
			if err != nil {
				return fmt.Errorf("failed to query provider_discovery: %w", err)
			}
			defer rows.Close()

			for rows.Next() {
				var r DiscoveryRule
				if err := rows.Scan(&r.ProcessPattern, &r.ExecutorPattern, &r.ModelPattern, &r.ProviderBucket, &r.Priority); err != nil {
					return fmt.Errorf("failed to scan provider_discovery rule: %w", err)
				}
				rules = append(rules, r)
			}
			rows.Close()

			// 2. Load Existing Offsets
			offsets := make(map[string]int64)
			oRows, err := p.Query(ctx, `SELECT file_path, last_offset FROM portfolio.transcript_offset`)
			if err != nil {
				return fmt.Errorf("failed to query transcript_offset: %w", err)
			}
			defer oRows.Close()

			for oRows.Next() {
				var fp string
				var off int64
				if err := oRows.Scan(&fp, &off); err != nil {
					return fmt.Errorf("failed to scan transcript_offset row: %w", err)
				}
				offsets[fp] = off
			}
			oRows.Close()

			// 3. Walk roots. Env-configurable: FLEET_PARSE_ROOTS is a
			// colon-separated list of dirs to walk for *.jsonl. Defaults to
			// /root/.claude/projects (Claude-CLI transcripts of all
			// dispatchers — VK polecats, refinery, etc.). The Review-Hot-Pool
			// root (/opt/quantbot/.claude-workbench/.claude/projects) is
			// included in the default list when present. Subagent transcripts
			// (…/subagents/*.jsonl) are picked up by the recursive walk.
			roots := parseWalkRoots()

			for _, baseDir := range roots {
				if _, err := os.Stat(baseDir); err != nil {
					fmt.Fprintf(os.Stderr, "Walk root %s inaccessible: %v — skipping.\n", baseDir, err)
					continue
				}

				walkErr := filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
					if err != nil {
						return nil // Skip inaccessible files or folders
					}
					if info.IsDir() || filepath.Ext(path) != ".jsonl" {
						return nil
					}

					storedOffset := offsets[path]
					if info.Size() < storedOffset {
						storedOffset = 0 // Reset offset if file shrunk
					}

					usage, newOffset, bucket, err := ParseTranscriptFile(path, storedOffset, rules)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error parsing file %s: %v\n", path, err)
						return nil
					}

					// Only update database if we actually read any new content
					if newOffset > storedOffset {
						// 1. If any new metrics were parsed, update provider_usage and agent_usage
						if usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.CacheCreationTokens > 0 || usage.CacheReadTokens > 0 || usage.OverloadEvents > 0 || usage.RequestCount > 0 {
							_, err = p.Exec(ctx, `
								INSERT INTO portfolio.provider_usage (provider_bucket, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, overload_events, request_count, updated_at)
								VALUES ($1, $2, $3, $4, $5, $6, $7, now())
								ON CONFLICT (provider_bucket) DO UPDATE SET
									input_tokens = portfolio.provider_usage.input_tokens + EXCLUDED.input_tokens,
									output_tokens = portfolio.provider_usage.output_tokens + EXCLUDED.output_tokens,
									cache_creation_tokens = portfolio.provider_usage.cache_creation_tokens + EXCLUDED.cache_creation_tokens,
									cache_read_tokens = portfolio.provider_usage.cache_read_tokens + EXCLUDED.cache_read_tokens,
									overload_events = portfolio.provider_usage.overload_events + EXCLUDED.overload_events,
									request_count = portfolio.provider_usage.request_count + EXCLUDED.request_count,
									updated_at = now()
							`, bucket, usage.InputTokens, usage.OutputTokens, usage.CacheCreationTokens, usage.CacheReadTokens, usage.OverloadEvents, usage.RequestCount)
							if err != nil {
								fmt.Fprintf(os.Stderr, "Error updating provider_usage for bucket %s: %v\n", bucket, err)
							}

							agentName := ExtractAgentName(path)
							if agentName != "" {
								_, err = p.Exec(ctx, `
									INSERT INTO portfolio.agent_usage (agent_name, provider_bucket, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, overload_events, request_count, updated_at)
									VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
									ON CONFLICT (agent_name, provider_bucket) DO UPDATE SET
										input_tokens = portfolio.agent_usage.input_tokens + EXCLUDED.input_tokens,
										output_tokens = portfolio.agent_usage.output_tokens + EXCLUDED.output_tokens,
										cache_creation_tokens = portfolio.agent_usage.cache_creation_tokens + EXCLUDED.cache_creation_tokens,
										cache_read_tokens = portfolio.agent_usage.cache_read_tokens + EXCLUDED.cache_read_tokens,
										overload_events = portfolio.agent_usage.overload_events + EXCLUDED.overload_events,
										request_count = portfolio.agent_usage.request_count + EXCLUDED.request_count,
										updated_at = now()
								`, agentName, bucket, usage.InputTokens, usage.OutputTokens, usage.CacheCreationTokens, usage.CacheReadTokens, usage.OverloadEvents, usage.RequestCount)
								if err != nil {
									fmt.Fprintf(os.Stderr, "Error updating agent_usage for agent %s, bucket %s: %v\n", agentName, bucket, err)
								}
							}
						}

						// 2. Update offset
						_, err = p.Exec(ctx, `
							INSERT INTO portfolio.transcript_offset (file_path, last_offset, updated_at)
							VALUES ($1, $2, now())
							ON CONFLICT (file_path) DO UPDATE SET
								last_offset = EXCLUDED.last_offset,
								updated_at = now()
						`, path, newOffset)
						if err != nil {
							fmt.Fprintf(os.Stderr, "Error updating offset for %s: %v\n", path, err)
						}
					}

					return nil
				})
				if walkErr != nil {
					return fmt.Errorf("failed to process transcript files under %s: %w", baseDir, walkErr)
				}
			}

			fmt.Println("Incremental transcript parsing completed successfully.")
			return nil
		},
	}
}

// parseWalkRoots resolves the list of directories to walk for *.jsonl
// transcripts. Priority: $FLEET_PARSE_ROOTS (colon-separated), else the default
// list — /root/.claude/projects always, plus the Review-Hot-Pool
// /opt/quantbot/.claude-workbench/.claude/projects when it exists.
func parseWalkRoots() []string {
	if env := strings.TrimSpace(os.Getenv("FLEET_PARSE_ROOTS")); env != "" {
		roots := []string{}
		for _, r := range strings.Split(env, ":") {
			if r = strings.TrimSpace(r); r != "" {
				roots = append(roots, r)
			}
		}
		if len(roots) > 0 {
			return roots
		}
	}

	roots := []string{"/root/.claude/projects"}
	reviewHotPool := "/opt/quantbot/.claude-workbench/.claude/projects"
	if _, err := os.Stat(reviewHotPool); err == nil {
		roots = append(roots, reviewHotPool)
	}
	return roots
}

// ExtractAgentName extracts the agent/workspace name from the file path
func ExtractAgentName(path string) string {
	rel := strings.TrimPrefix(path, "/root/.claude/projects/")
	rel = strings.TrimPrefix(rel, "/")

	parts := strings.Split(rel, "/")
	dir := parts[0]

	if len(parts) == 1 {
		dir = strings.TrimSuffix(dir, ".jsonl")
	}

	if strings.Contains(dir, "polecats-") {
		idx := strings.Index(dir, "polecats-")
		sub := dir[idx+9:]
		p := strings.Split(sub, "-")
		if len(p) > 0 {
			return p[0]
		}
	}

	if strings.Contains(dir, "worktrees-") {
		idx := strings.Index(dir, "worktrees-")
		sub := dir[idx+10:]
		p := strings.Split(sub, "-")
		startIdx := 0
		if len(p) > 0 && len(p[0]) == 4 {
			startIdx = 1
		}
		if len(p) > startIdx {
			for i := startIdx; i < len(p); i++ {
				if strings.Contains(strings.ToLower(p[i]), "stayawesome") {
					return "stayawesomeOS"
				}
				if strings.Contains(strings.ToLower(p[i]), "solartown") {
					return "solartown"
				}
				if strings.Contains(strings.ToLower(p[i]), "quantbot") {
					return "quantbot"
				}
			}
			if len(p) > startIdx+2 {
				for i := startIdx; i < len(p)-1; i++ {
					if (p[i] == "st" || p[i] == "tr" || p[i] == "so" || p[i] == "qu") && i+1 < len(p) {
						return p[i] + "-" + p[i+1]
					}
				}
			}
			return p[startIdx]
		}
	}

	if strings.Contains(dir, "witness") {
		return "witness"
	}
	if strings.Contains(dir, "refinery") {
		return "refinery"
	}

	name := strings.TrimPrefix(dir, "-")
	name = strings.TrimPrefix(name, "root-")
	name = strings.TrimPrefix(name, "opt-")
	name = strings.TrimSuffix(name, ".jsonl")
	return name
}
