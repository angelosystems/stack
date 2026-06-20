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

// ExtractPEM extracts process, executor, and model from a file's first few lines and its path
func ExtractPEM(filePath string, lines []string) (process, executor, model string) {
	process = "claude"
	executor = ""
	model = ""

	pathLower := strings.ToLower(filePath)
	contentConcat := strings.ToLower(strings.Join(lines, " "))

	// Scan lines for model name
	for _, line := range lines {
		if strings.Contains(line, `"model":`) {
			idx := strings.Index(line, `"model":`)
			if idx != -1 {
				sub := line[idx:]
				quoteIdx := strings.Index(sub, `"model"`)
				if quoteIdx != -1 {
					sub2 := sub[quoteIdx+7:]
					colonIdx := strings.Index(sub2, ":")
					if colonIdx != -1 {
						sub3 := sub2[colonIdx+1:]
						firstQuote := strings.Index(sub3, `"`)
						if firstQuote != -1 {
							secondQuote := strings.Index(sub3[firstQuote+1:], `"`)
							if secondQuote != -1 {
								model = strings.ToLower(sub3[firstQuote+1 : firstQuote+1+secondQuote])
								break
							}
						}
					}
				}
			}
		}
	}

	if strings.Contains(pathLower, "paperclip-worker") || strings.Contains(contentConcat, "paperclip-worker") {
		process = "paperclip-worker"
	} else if strings.Contains(pathLower, "gemini") || strings.Contains(contentConcat, "gemini") {
		process = "gemini"
	} else if strings.Contains(pathLower, "opencode") || strings.Contains(contentConcat, "opencode") {
		process = "opencode"
	} else if strings.Contains(pathLower, "claude") || strings.Contains(contentConcat, "claude") {
		process = "claude"
	}

	if strings.Contains(pathLower, "flows") || strings.Contains(contentConcat, "flows") {
		executor = "flows"
	}

	return process, executor, model
}

// ParseTranscriptFile reads a transcript file from storedOffset, parses its lines, and extracts metrics
func ParseTranscriptFile(path string, storedOffset int64, rules []DiscoveryRule) (usage TokenUsage, newOffset int64, matchedBucket string, err error) {
	matchedBucket = "other"
	newOffset = storedOffset

	f, err := os.Open(path)
	if err != nil {
		return usage, storedOffset, matchedBucket, err
	}
	defer f.Close()

	// Read first 20 lines for classification
	var firstLines []string
	scannerClassify := bufio.NewScanner(f)
	for scannerClassify.Scan() {
		firstLines = append(firstLines, scannerClassify.Text())
		if len(firstLines) >= 20 {
			break
		}
	}

	// Classify
	proc, exec, md := ExtractPEM(path, firstLines)
	for _, rule := range rules {
		if rule.Matches(proc, exec, md) {
			matchedBucket = rule.ProviderBucket
			break
		}
	}

	// Seek back to storedOffset to parse increment
	_, err = f.Seek(storedOffset, io.SeekStart)
	if err != nil {
		return usage, storedOffset, matchedBucket, err
	}

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024*10) // up to 10MB line buffer

	bytesRead := int64(0)
	for scanner.Scan() {
		line := scanner.Text()
		bytesRead += int64(len(scanner.Bytes())) + 1

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

	newOffset = storedOffset + bytesRead
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

			// 3. Walk through all .jsonl files in /root/.claude/projects/
			baseDir := "/root/.claude/projects"
			if _, err := os.Stat(baseDir); os.IsNotExist(err) {
				fmt.Printf("Base directory %s does not exist. Skipping parsing.\n", baseDir)
				return nil
			}

			err = filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
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
					// 1. If any new metrics were parsed, update provider_usage
					if usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.CacheCreationTokens > 0 || usage.CacheReadTokens > 0 || usage.OverloadEvents > 0 {
						_, err = p.Exec(ctx, `
							INSERT INTO portfolio.provider_usage (provider_bucket, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, overload_events, updated_at)
							VALUES ($1, $2, $3, $4, $5, $6, now())
							ON CONFLICT (provider_bucket) DO UPDATE SET
								input_tokens = portfolio.provider_usage.input_tokens + EXCLUDED.input_tokens,
								output_tokens = portfolio.provider_usage.output_tokens + EXCLUDED.output_tokens,
								cache_creation_tokens = portfolio.provider_usage.cache_creation_tokens + EXCLUDED.cache_creation_tokens,
								cache_read_tokens = portfolio.provider_usage.cache_read_tokens + EXCLUDED.cache_read_tokens,
								overload_events = portfolio.provider_usage.overload_events + EXCLUDED.overload_events,
								updated_at = now()
						`, bucket, usage.InputTokens, usage.OutputTokens, usage.CacheCreationTokens, usage.CacheReadTokens, usage.OverloadEvents)
						if err != nil {
							fmt.Fprintf(os.Stderr, "Error updating provider_usage for bucket %s: %v\n", bucket, err)
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

			if err != nil {
				return fmt.Errorf("failed to process transcript files: %w", err)
			}

			fmt.Println("Incremental transcript parsing completed successfully.")
			return nil
		},
	}
}
