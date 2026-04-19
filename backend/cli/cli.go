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

var Version = "dev"

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
	case "help", "--help", "-h":
		printHelp()
		return true
	case "version", "--version", "-v":
		printVersion()
		return true
	default:
		return false
	}
}

const banner = `
  ____                                __        ___             
 | __ ) _ __ _____      _____  _ __ \ \      / (_)_ __   __ _ 
 |  _ \| '__/ _ \ \ /\ / / __|/ _ \ \ \ /\ / /| | '_ \ / _' |
 | |_) | | | (_) \ V  V /\__ \  __/  \ V  V / | | | | | (_| |
 |____/|_|  \___/ \_/\_/ |___/\___|   \_/\_/  |_|_| |_|\__, |
                                                         |___/ `

func printVersion() {
	fmt.Printf("BrowserWing %s\n", Version)
}

func printHelp() {
	fmt.Print(banner)
	fmt.Printf("\n  %s — Intelligent Browser Automation Platform\n\n", Version)

	fmt.Print(`USAGE:
  browserwing <command> [options]

COMMANDS:
  run <name|id> [options]    Execute a script and return extracted data
  list | ls    [options]     List all available scripts
  help                       Show this help message
  version                    Show version info

RUN OPTIONS:
  --format=<json|table|csv>  Output format (default: json)
  --no-headless              Show browser window (default: headless)
  --<key>=<value>            Pass variables to the script

LIST OPTIONS:
  --format=<json|table|csv>  Output format (default: table)

ENVIRONMENT:
  BROWSERWING_URL            Server URL (default: http://localhost:18050)

`)

	fmt.Print(`EXAMPLES:

  # Get Bilibili trending videos (outputs JSON for piping)
  browserwing run bilibili-hot

  # Get GitHub trending repos as a table
  browserwing run github-trending --format=table

  # Search JD.com with a keyword
  browserwing run jd-search --keyword="机械键盘" --format=json

  # Run with visible browser for debugging
  browserwing run zhihu-hot --no-headless

  # List all scripts in JSON (for AI agents)
  browserwing ls --format=json

  # Pipe output to other tools
  browserwing run hackernews-top --format=json | jq '.[0:5]'
  browserwing run sinafinance-rank --format=csv > stocks.csv

`)

	fmt.Print(`AI AGENT INTEGRATION:

  BrowserWing CLI is designed for AI agent consumption.
  Use --format=json for structured output that's easy to parse.

  Typical workflow:
    1. browserwing ls --format=json    # discover available scripts
    2. browserwing run <name>          # execute and get data as JSON
    3. Parse the JSON output for further processing

  MCP (Model Context Protocol) is also supported via the web API.
  See: http://localhost:18050/api/v1/mcp for the MCP endpoint.

`)
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
