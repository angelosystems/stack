package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// LogEntry represents a single JSON line in a .jsonl log file from Claude Code.
type LogEntry struct {
	Type           string         `json:"type"`
	Timestamp      string         `json:"timestamp"`
	Message        *MessageDetail `json:"message"`
	Error          any            `json:"error"`
	ApiErrorStatus int            `json:"apiErrorStatus"`
	Subtype        string         `json:"subtype"`
}

type MessageDetail struct {
	Model string       `json:"model"`
	Role  string       `json:"role"`
	Usage *UsageDetail `json:"usage"`
}

type UsageDetail struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// ProviderMetrics stores token and error metrics for a given model provider.
type ProviderMetrics struct {
	Provider             string  `json:"provider"`
	InputTokens          int64   `json:"input_tokens"`
	OutputTokens         int64   `json:"output_tokens"`
	TotalTokens          int64   `json:"total_tokens"`
	RequestCount         int64   `json:"request_count"`
	ErrorCount429        int64   `json:"error_count_429"`
	ErrorCountOverloaded int64   `json:"error_count_overloaded"`
	ProxyCeilingRate     float64 `json:"proxy_ceiling_rate"`
	HonestLabel          string  `json:"honest_label"`
}

// cmdParseTranscripts creates the parse-transcripts subcommand for master-kanban.
func cmdParseTranscripts() *cobra.Command {
	var (
		projectsDir   string
		stateFilePath string
		asJSON        bool
	)

	c := &cobra.Command{
		Use:   "parse-transcripts",
		Short: "Parses Claude Code transcript JSONL files incrementally for tokens and 429 events",
		RunE: func(cmd *cobra.Command, args []string) error {
			metrics, err := ParseTranscriptsIncremental(projectsDir, stateFilePath)
			if err != nil {
				return err
			}

			if asJSON {
				b, err := json.MarshalIndent(metrics, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(b))
				return nil
			}

			fmt.Println("=== INCREMENTAL TRANSCRIPT METRICS REPORT ===")
			for _, m := range metrics {
				fmt.Printf("Provider: %s\n", m.Provider)
				fmt.Printf("  Requests:         %d\n", m.RequestCount)
				fmt.Printf("  Tokens (Input):   %d\n", m.InputTokens)
				fmt.Printf("  Tokens (Output):  %d\n", m.OutputTokens)
				fmt.Printf("  Tokens (Total):   %d\n", m.TotalTokens)
				fmt.Printf("  429 Errors:       %d\n", m.ErrorCount429)
				fmt.Printf("  Overloaded:       %d\n", m.ErrorCountOverloaded)
				fmt.Printf("  429-Rate Proxy:   %.4f (%% of requests hit rate limit / overload)\n", m.ProxyCeilingRate*100)
				fmt.Printf("  Label:            %s\n", m.HonestLabel)
				fmt.Println("---------------------------------------------")
			}
			return nil
		},
	}

	c.Flags().StringVar(&projectsDir, "dir", "/root/.claude/projects", "Directory containing .jsonl projects transcripts")
	c.Flags().StringVar(&stateFilePath, "state", "/tmp/claude_parser_offsets.json", "Path to state file tracking byte offsets")
	c.Flags().BoolVar(&asJSON, "json", false, "Output results in JSON format")

	return c
}

// ParseTranscriptsIncremental reads recursively .jsonl files under projectsDir
// using stateFilePath to track and seek file offsets.
func ParseTranscriptsIncremental(projectsDir, stateFilePath string) (map[string]*ProviderMetrics, error) {
	// 1. Read existing offsets
	offsets := make(map[string]int64)
	if stateFilePath != "" {
		if data, err := os.ReadFile(stateFilePath); err == nil {
			_ = json.Unmarshal(data, &offsets)
		}
	}

	// 2. Aggregate results
	metrics := map[string]*ProviderMetrics{
		"anthropic": {
			Provider:    "anthropic",
			HonestLabel: "honest approximation of the unknown rate-limit ceiling via 429-rate as a proxy",
		},
		"zai": {
			Provider:    "zai",
			HonestLabel: "honest approximation of the unknown rate-limit ceiling via 429-rate as a proxy",
		},
	}

	// 3. Find files recursively
	var files []string
	_ = filepath.Walk(projectsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".jsonl") {
			files = append(files, path)
		}
		return nil
	})

	// 4. Process each file
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}

		size := info.Size()
		startOffset := offsets[file]

		// Reset offset if file was truncated or rotated
		if startOffset > size {
			startOffset = 0
		}

		if startOffset == size {
			// No new data in this file
			continue
		}

		f, err := os.Open(file)
		if err != nil {
			continue
		}

		if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
			f.Close()
			continue
		}

		reader := bufio.NewReader(f)
		currentOffset := startOffset
		activeModel := "claude-sonnet-4-6" // default fallback model for Claude sessions

		for {
			lineBytes, err := reader.ReadBytes('\n')
			if err != nil && len(lineBytes) == 0 {
				break
			}

			// Save the exact number of bytes read
			bytesRead := int64(len(lineBytes))

			// Trim spaces/newlines for parsing
			lineStr := strings.TrimSpace(string(lineBytes))
			if len(lineStr) > 0 {
				var entry LogEntry
				if errJson := json.Unmarshal([]byte(lineStr), &entry); errJson == nil {
					// Update active model from non-synthetic model messages
					if entry.Message != nil && entry.Message.Model != "" && entry.Message.Model != "<synthetic>" {
						activeModel = entry.Message.Model
					}

					provider := "anthropic"
					if strings.Contains(activeModel, "glm") {
						provider = "zai"
					}

					provMetrics := metrics[provider]
					if provMetrics == nil {
						provMetrics = &ProviderMetrics{
							Provider:    provider,
							HonestLabel: "honest approximation of the unknown rate-limit ceiling via 429-rate as a proxy",
						}
						metrics[provider] = provMetrics
					}

					// Parse Token Usage
					if entry.Message != nil && entry.Message.Usage != nil {
						usage := entry.Message.Usage
						provMetrics.InputTokens += usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
						provMetrics.OutputTokens += usage.OutputTokens
						provMetrics.TotalTokens += usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens + usage.OutputTokens
						provMetrics.RequestCount++
					}

					// Parse 429/Overloaded Errors
					is429 := false
					isOverload := false

					if entry.ApiErrorStatus == 429 {
						is429 = true
					} else if strErr, ok := entry.Error.(string); ok && strErr == "rate_limit" {
						is429 = true
					}

					// Check overloaded error format: "overloaded_error" or "Overloaded"
					if entry.Subtype == "api_error" || entry.Type == "system" {
						rawErrorBytes, errRaw := json.Marshal(entry.Error)
						if errRaw == nil {
							errStr := string(rawErrorBytes)
							if strings.Contains(errStr, "overloaded_error") || strings.Contains(errStr, "Overloaded") {
								isOverload = true
							}
						}
					}

					// Additional fallback checks on raw line
					if !is429 && !isOverload {
						if strings.Contains(lineStr, `"error":"rate_limit"`) || strings.Contains(lineStr, `"apiErrorStatus":429`) {
							is429 = true
						} else if strings.Contains(lineStr, "overloaded_error") || strings.Contains(lineStr, "Overloaded") {
							isOverload = true
						}
					}

					if is429 {
						provMetrics.ErrorCount429++
					}
					if isOverload {
						provMetrics.ErrorCountOverloaded++
					}
				}
			}

			currentOffset += bytesRead
			if err == io.EOF {
				break
			}
		}
		f.Close()

		// Save the offset for the file
		offsets[file] = currentOffset
	}

	// 5. Save offsets state
	if stateFilePath != "" {
		if data, err := json.Marshal(offsets); err == nil {
			_ = os.WriteFile(stateFilePath, data, 0644)
		}
	}

	// 6. Compute proxy ceiling rate
	for _, m := range metrics {
		if m.RequestCount > 0 {
			m.ProxyCeilingRate = float64(m.ErrorCount429+m.ErrorCountOverloaded) / float64(m.RequestCount)
		} else {
			m.ProxyCeilingRate = 0.0
		}
		// Honest label
		m.HonestLabel = "honest approximation of the unknown rate-limit ceiling via 429-rate as a proxy"
	}

	return metrics, nil
}
