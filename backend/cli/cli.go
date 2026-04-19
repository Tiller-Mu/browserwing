// Package cli implements the BrowserWing CLI interface, allowing users and AI agents
// to run scripts directly from the terminal: `browserwing run <name> --key=value`
package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

const defaultBaseURL = "http://localhost:18050"

func getBaseURL() string {
	if url := os.Getenv("BROWSERWING_URL"); url != "" {
		return strings.TrimRight(url, "/")
	}
	return defaultBaseURL
}

func apiGet(path string) ([]byte, error) {
	url := getBaseURL() + path
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to BrowserWing server at %s: %w\nMake sure the server is running", getBaseURL(), err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func apiPost(path string, payload interface{}) ([]byte, error) {
	url := getBaseURL() + path
	client := &http.Client{Timeout: 120 * time.Second}
	data, _ := json.Marshal(payload)
	resp, err := client.Post(url, "application/json", strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("cannot connect to BrowserWing server at %s: %w\nMake sure the server is running", getBaseURL(), err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// Execute is the main entry point for CLI mode.
// Returns true if a CLI subcommand was handled, false if the server should start.
func Execute(args []string) bool {
	if len(args) < 2 {
		return false
	}

	subcmd := args[1]
	switch subcmd {
	case "run":
		return handleRun(args[2:])
	case "list", "ls":
		return handleList(args[2:])
	case "help":
		printHelp()
		return true
	default:
		return false
	}
}

func printHelp() {
	fmt.Println(`BrowserWing CLI

Usage:
  browserwing [command]

Commands:
  run <name|id> [--key=value...]   Run a script by name or ID
  list                             List all available scripts
  help                             Show this help

Run Options:
  --format=<json|table|csv>        Output format (default: table)
  --<key>=<value>                  Pass variables to the script

Environment:
  BROWSERWING_URL                  Server URL (default: http://localhost:18050)

Examples:
  browserwing run "bilibili-hot" --limit=10 --format=json
  browserwing list
  browserwing run my-script --keyword="test" --format=csv`)
}

// --- Output Formatting ---

func formatOutput(data interface{}, format string) {
	switch format {
	case "json":
		out, _ := json.MarshalIndent(data, "", "  ")
		fmt.Println(string(out))
	case "csv":
		formatCSV(data, os.Stdout)
	default:
		formatTable(data, os.Stdout)
	}
}

func formatTable(data interface{}, w io.Writer) {
	rows := toRows(data)
	if len(rows) == 0 {
		fmt.Fprintln(w, "(no data)")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	// Header
	if len(rows) > 0 {
		keys := getKeys(rows[0])
		fmt.Fprintln(tw, strings.Join(keys, "\t"))
		sep := make([]string, len(keys))
		for i, k := range keys {
			sep[i] = strings.Repeat("-", len(k))
		}
		fmt.Fprintln(tw, strings.Join(sep, "\t"))

		for _, row := range rows {
			vals := make([]string, len(keys))
			for i, k := range keys {
				vals[i] = fmt.Sprintf("%v", row[k])
			}
			fmt.Fprintln(tw, strings.Join(vals, "\t"))
		}
	}
	tw.Flush()
}

func formatCSV(data interface{}, w io.Writer) {
	rows := toRows(data)
	if len(rows) == 0 {
		return
	}

	writer := csv.NewWriter(w)
	keys := getKeys(rows[0])
	writer.Write(keys)
	for _, row := range rows {
		vals := make([]string, len(keys))
		for i, k := range keys {
			vals[i] = fmt.Sprintf("%v", row[k])
		}
		writer.Write(vals)
	}
	writer.Flush()
}

func toRows(data interface{}) []map[string]interface{} {
	switch v := data.(type) {
	case []interface{}:
		rows := make([]map[string]interface{}, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				rows = append(rows, m)
			}
		}
		return rows
	case map[string]interface{}:
		return []map[string]interface{}{v}
	default:
		return nil
	}
}

func getKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
