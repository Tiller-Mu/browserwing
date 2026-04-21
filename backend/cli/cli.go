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
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pelletier/go-toml/v2"
)

var Version = "dev"

// cliPort can be set by --port flag before subcommand dispatch
var cliPort string

func getBaseURL() string {
	if url := os.Getenv("BROWSERWING_URL"); url != "" {
		return strings.TrimRight(url, "/")
	}
	if cliPort != "" {
		return "http://localhost:" + cliPort
	}
	if port := detectPortFromConfig(); port != "" {
		return "http://localhost:" + port
	}
	return "http://localhost:18050"
}

func detectPortFromConfig() string {
	candidates := []string{
		"config.toml",
		filepath.Join(".", "config.toml"),
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "config.toml"))
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg struct {
			Server struct {
				Port string `toml:"port"`
			} `toml:"server"`
		}
		if err := toml.Unmarshal(data, &cfg); err == nil && cfg.Server.Port != "" {
			return cfg.Server.Port
		}
	}
	return ""
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

func apiDelete(path string) ([]byte, error) {
	url := getBaseURL() + path
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
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

func apiPut(path string, payload interface{}) ([]byte, error) {
	url := getBaseURL() + path
	client := &http.Client{Timeout: 30 * time.Second}
	data, _ := json.Marshal(payload)
	req, err := http.NewRequest("PUT", url, strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
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

	// Extract global flags (--port, --url) before subcommand
	var filteredArgs []string
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "--port=") {
			cliPort = strings.TrimPrefix(arg, "--port=")
		} else if strings.HasPrefix(arg, "--url=") {
			os.Setenv("BROWSERWING_URL", strings.TrimPrefix(arg, "--url="))
		} else {
			filteredArgs = append(filteredArgs, arg)
		}
	}

	if len(filteredArgs) == 0 {
		return false
	}

	subcmd := filteredArgs[0]
	switch subcmd {
	case "run":
		result := handleRun(filteredArgs[1:])
		checkForUpdate()
		return result
	case "list", "ls":
		result := handleList(filteredArgs[1:])
		checkForUpdate()
		return result
	case "exec":
		return handleExec(filteredArgs[1:])
	case "config":
		return handleConfig(filteredArgs[1:])
	case "browser":
		return handleBrowser(filteredArgs[1:])
	case "cookie":
		return handleCookie(filteredArgs[1:])
	case "script":
		return handleScript(filteredArgs[1:])
	case "explore":
		return handleExplore(filteredArgs[1:])
	case "prompt":
		return handlePrompt(filteredArgs[1:])
	case "mcp":
		return handleMCP(filteredArgs[1:])
	case "health":
		return handleHealth(filteredArgs[1:])
	case "doctor":
		return handleDoctor()
	case "help", "--help", "-h":
		printHelp()
		return true
	case "version", "--version", "-v":
		printVersion()
		checkForUpdate()
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

SCRIPT COMMANDS:
  run <name|id> [options]     Execute a script and return extracted data
  list | ls    [options]      List all available scripts

BROWSER CONTROL (executor):
  exec <action> [args]        Direct browser control (navigate, click, type, extract...)

MANAGEMENT:
  config <action>             LLM configuration management
  browser <action>            Browser instance management
  cookie <action>             Cookie management
  script <action>             Script CRUD and export
  explore <action>            AI autonomous exploration
  prompt <action>             System prompt management
  mcp <action>                MCP server status and tools

SYSTEM:
  doctor                      Check server, Chrome, and system health
  health                      Quick service health check
  help                        Show this help message
  version                     Show version info

GLOBAL OPTIONS:
  --port=<port>               Server port (auto-detected from config.toml)
  --url=<url>                 Full server URL (overrides port)

RUN OPTIONS:
  --format=<json|table|csv>   Output format (default: json)
  --no-headless               Show browser window (default: headless)
  --<key>=<value>             Pass variables to the script

LIST OPTIONS:
  --format=<json|table|csv>   Output format (default: table)
  --builtin                   Show only built-in scripts
  --user | --no-builtin       Show only user-created scripts
  --search=<keyword>          Fuzzy search by name/description/id
  --category=<name>           Filter by category (tech, finance, news...)
  --limit=<n>                 Max results per page
  --page=<n>                  Page number (use with --limit)

ENVIRONMENT:
  BROWSERWING_URL             Server URL (overrides all other settings)

`)

	fmt.Print("PASSING PARAMETERS:\n\n")
	fmt.Print("  Some scripts accept parameters (shown in \"params\" field of ls --format=json).\n")
	fmt.Print("  Pass them as --key=value flags after the script name:\n\n")
	fmt.Print("    browserwing run jd-search --keyword=\"机械键盘\"\n")
	fmt.Print("    browserwing run taobao-search --keyword=\"蓝牙耳机\"\n\n")
	fmt.Print("  Parameters replace ${key} placeholders in the script's URL/actions.\n")
	fmt.Print("  Use \"browserwing ls --format=json\" to see which scripts have params.\n")
	fmt.Print(`

EXAMPLES:

  # Run built-in scripts
  browserwing run bilibili-hot
  browserwing run github-trending --format=table
  browserwing run jd-search --keyword="机械键盘"

  # List and search scripts
  browserwing ls --builtin
  browserwing ls --search=stock --format=json

  # Direct browser control
  browserwing exec navigate https://example.com
  browserwing exec snapshot
  browserwing exec click @e3
  browserwing exec type @e5 "search query"
  browserwing exec extract ".item" --fields=text,href --multiple
  browserwing exec screenshot --output=page.png
  browserwing exec eval 'document.title'

  # Admin operations
  browserwing config list
  browserwing config add --name=gpt --provider=openai --api-key=sk-xxx --model=gpt-4o
  browserwing browser list
  browserwing cookie list
  browserwing script export --output=SKILL.md
  browserwing explore start --url=https://bilibili.com --task="search AI"
  browserwing health

  # Pipe to other tools
  browserwing run hackernews-top | jq '.[0:5]'

`)

	fmt.Print(`AI AGENT INTEGRATION:

  BrowserWing CLI is designed for AI agent consumption.
  Use --format=json for structured output that's easy to parse.

  Typical agent workflow:
    1. browserwing ls --format=json                  # discover scripts
    2. browserwing run <name>                         # execute and get data

  Direct browser control workflow:
    1. browserwing exec navigate <url>                # open page
    2. browserwing exec snapshot                      # get page structure
    3. browserwing exec click @e3                     # interact with elements
    4. browserwing exec extract ".result" --multiple  # extract data

  Each subcommand has its own help:
    browserwing exec help
    browserwing config help
    browserwing script help

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
