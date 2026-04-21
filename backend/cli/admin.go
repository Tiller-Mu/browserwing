package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// --- config (LLM configuration) ---

func handleConfig(args []string) bool {
	if len(args) == 0 {
		printConfigHelp()
		return true
	}
	action := args[0]
	rest := args[1:]
	format := extractFormat(&rest, "table")

	switch action {
	case "list", "ls":
		return execGet("/api/v1/llm-configs", format)
	case "add":
		return configAdd(rest, format)
	case "test":
		return configTest(rest, format)
	case "delete", "rm":
		if len(rest) == 0 {
			exitWithError(ExitBadArgs, "config ID is required", "Usage: browserwing config delete <id>")
		}
		body, err := apiDelete("/api/v1/llm-configs/" + rest[0])
		if err != nil {
			exitWithError(ExitConnectError, err.Error(), "")
		}
		printAPIResponse(body, format)
		return true
	case "help":
		printConfigHelp()
		return true
	default:
		exitWithError(ExitBadArgs, fmt.Sprintf("unknown config action: %s", action),
			"Available: list, add, test, delete")
	}
	return true
}

func configAdd(args []string, format string) bool {
	payload := map[string]interface{}{}
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			kv := strings.TrimPrefix(a, "--")
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) == 2 {
				key := strings.ReplaceAll(parts[0], "-", "_")
				switch key {
				case "default":
					payload["is_default"] = parts[1] == "true" || parts[1] == "1"
				case "active":
					payload["is_active"] = parts[1] == "true" || parts[1] == "1"
				default:
					payload[key] = parts[1]
				}
			}
		}
	}
	required := []string{"name", "provider", "api_key", "model"}
	for _, r := range required {
		if _, ok := payload[r]; !ok {
			exitWithError(ExitBadArgs, fmt.Sprintf("--%s is required", strings.ReplaceAll(r, "_", "-")),
				"Usage: browserwing config add --name=my-llm --provider=openai --api-key=sk-xxx --model=gpt-4o")
		}
	}
	if _, ok := payload["is_active"]; !ok {
		payload["is_active"] = true
	}
	return execPost("/api/v1/llm-configs", payload, format)
}

func configTest(args []string, format string) bool {
	if len(args) == 0 {
		exitWithError(ExitBadArgs, "config name is required", "Usage: browserwing config test <name>")
	}
	return execPost("/api/v1/llm-configs/test", map[string]interface{}{
		"name": args[0],
	}, format)
}

func printConfigHelp() {
	fmt.Print(`LLM CONFIGURATION

  browserwing config <action> [options]

ACTIONS:
  list                        List all LLM configurations
  add [options]               Add a new LLM configuration
  test <name>                 Test LLM connection
  delete <id>                 Delete a configuration

ADD OPTIONS:
  --name=<name>               Config name (required)
  --provider=<provider>       Provider: openai, anthropic, deepseek (required)
  --api-key=<key>             API key (required)
  --model=<model>             Model name, e.g. gpt-4o (required)
  --base-url=<url>            Custom API base URL
  --default=true              Set as default config

EXAMPLES:
  browserwing config list
  browserwing config add --name=my-gpt --provider=openai --api-key=sk-xxx --model=gpt-4o --default=true
  browserwing config add --name=deepseek --provider=deepseek --api-key=sk-xxx --model=deepseek-chat --base-url=https://api.deepseek.com/v1
  browserwing config test my-gpt
  browserwing config delete <config-id>
`)
}

// --- browser (instance management) ---

func handleBrowser(args []string) bool {
	if len(args) == 0 {
		return execGet("/api/v1/browser/instances", "table")
	}
	action := args[0]
	rest := args[1:]
	format := extractFormat(&rest, "json")

	switch action {
	case "list", "ls":
		return execGet("/api/v1/browser/instances", format)
	case "start":
		id := "default"
		if len(rest) > 0 {
			id = rest[0]
		}
		return execPost("/api/v1/browser/instances/"+id+"/start", nil, format)
	case "stop":
		id := "default"
		if len(rest) > 0 {
			id = rest[0]
		}
		return execPost("/api/v1/browser/instances/"+id+"/stop", nil, format)
	case "help":
		fmt.Print(`BROWSER INSTANCE MANAGEMENT

  browserwing browser <action> [instance-id]

ACTIONS:
  list            List all browser instances
  start [id]      Start a browser instance (default: "default")
  stop [id]       Stop a browser instance

EXAMPLES:
  browserwing browser list
  browserwing browser start
  browserwing browser stop
`)
		return true
	default:
		exitWithError(ExitBadArgs, fmt.Sprintf("unknown browser action: %s", action),
			"Available: list, start, stop")
	}
	return true
}

// --- cookie management ---

func handleCookie(args []string) bool {
	if len(args) == 0 {
		return execGet("/api/v1/cookies/browser", "json")
	}
	action := args[0]
	rest := args[1:]
	format := extractFormat(&rest, "json")

	switch action {
	case "list", "ls":
		storeID := "browser"
		if len(rest) > 0 {
			storeID = rest[0]
		}
		return execGet("/api/v1/cookies/"+storeID, format)
	case "save":
		return execPost("/api/v1/browser/cookies/save", nil, format)
	case "import":
		return cookieImport(rest, format)
	case "delete", "rm":
		return cookieDelete(rest, format)
	case "help":
		fmt.Print(`COOKIE MANAGEMENT

  browserwing cookie <action> [options]

ACTIONS:
  list [store-id]             List cookies (default store: "browser")
  save                        Save current browser cookies to database
  import <file.json>          Import cookies from JSON file
  delete [options]             Delete a cookie

IMPORT FILE FORMAT:
  {
    "url": "https://example.com",
    "cookies": [
      {"name": "session", "value": "abc", "domain": ".example.com"}
    ]
  }

DELETE OPTIONS:
  --name=<name>               Cookie name (required)
  --domain=<domain>           Cookie domain (required)
  --path=<path>               Cookie path (default: "/")
  --store=<id>                Cookie store ID (default: "browser")

EXAMPLES:
  browserwing cookie list
  browserwing cookie save
  browserwing cookie import cookies.json
  browserwing cookie delete --name=session --domain=.example.com
`)
		return true
	default:
		exitWithError(ExitBadArgs, fmt.Sprintf("unknown cookie action: %s", action),
			"Available: list, save, import, delete")
	}
	return true
}

func cookieImport(args []string, format string) bool {
	if len(args) == 0 {
		exitWithError(ExitBadArgs, "JSON file path is required",
			"Usage: browserwing cookie import <cookies.json>")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		exitWithError(ExitBadArgs, fmt.Sprintf("failed to read file: %v", err), "")
	}
	var payload interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		exitWithError(ExitBadArgs, fmt.Sprintf("invalid JSON: %v", err), "")
	}
	return execPost("/api/v1/browser/cookies/import", payload, format)
}

func cookieDelete(args []string, format string) bool {
	payload := map[string]interface{}{"id": "browser", "path": "/"}
	for _, a := range args {
		if strings.HasPrefix(a, "--name=") {
			payload["name"] = strings.TrimPrefix(a, "--name=")
		} else if strings.HasPrefix(a, "--domain=") {
			payload["domain"] = strings.TrimPrefix(a, "--domain=")
		} else if strings.HasPrefix(a, "--path=") {
			payload["path"] = strings.TrimPrefix(a, "--path=")
		} else if strings.HasPrefix(a, "--store=") {
			payload["id"] = strings.TrimPrefix(a, "--store=")
		}
	}
	if _, ok := payload["name"]; !ok {
		exitWithError(ExitBadArgs, "--name is required",
			"Usage: browserwing cookie delete --name=session --domain=.example.com")
	}
	return execPost("/api/v1/browser/cookies/delete", payload, format)
}

// --- script management (create, get, delete, export) ---

func handleScript(args []string) bool {
	if len(args) == 0 {
		printScriptHelp()
		return true
	}
	action := args[0]
	rest := args[1:]
	format := extractFormat(&rest, "json")

	switch action {
	case "get", "show":
		if len(rest) == 0 {
			exitWithError(ExitBadArgs, "script ID is required", "Usage: browserwing script get <id>")
		}
		return execGet("/api/v1/scripts/"+rest[0], format)
	case "create":
		return scriptCreate(rest, format)
	case "delete", "rm":
		if len(rest) == 0 {
			exitWithError(ExitBadArgs, "script ID is required", "Usage: browserwing script delete <id>")
		}
		body, err := apiDelete("/api/v1/scripts/" + rest[0])
		if err != nil {
			exitWithError(ExitConnectError, err.Error(), "")
		}
		printAPIResponse(body, format)
		return true
	case "export":
		return scriptExport(rest)
	case "export-executor":
		return scriptExportExecutor(rest)
	case "export-admin":
		return scriptExportAdmin(rest)
	case "summary":
		return execGet("/api/v1/scripts/summary", format)
	case "history":
		return execGet("/api/v1/script-executions?page=1&page_size=20", format)
	case "result":
		return execGet("/api/v1/scripts/play/result", format)
	case "help":
		printScriptHelp()
		return true
	default:
		exitWithError(ExitBadArgs, fmt.Sprintf("unknown script action: %s", action),
			"Available: get, create, delete, export, export-executor, export-admin, summary, history, result")
	}
	return true
}

func scriptCreate(args []string, format string) bool {
	if len(args) == 0 {
		exitWithError(ExitBadArgs, "JSON file is required",
			"Usage: browserwing script create <script.json>")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		exitWithError(ExitBadArgs, fmt.Sprintf("failed to read file: %v", err), "")
	}
	var payload interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		exitWithError(ExitBadArgs, fmt.Sprintf("invalid JSON: %v", err), "")
	}
	return execPost("/api/v1/scripts", payload, format)
}

func scriptExport(args []string) bool {
	output := ""
	var ids []string
	for _, a := range args {
		if strings.HasPrefix(a, "--output=") {
			output = strings.TrimPrefix(a, "--output=")
		} else if strings.HasPrefix(a, "--ids=") {
			ids = strings.Split(strings.TrimPrefix(a, "--ids="), ",")
		} else if !strings.HasPrefix(a, "--") {
			ids = append(ids, a)
		}
	}
	body, err := apiPost("/api/v1/scripts/export/skill", map[string]interface{}{
		"script_ids": ids,
	})
	if err != nil {
		exitWithError(ExitConnectError, err.Error(), "")
	}
	return writeSkillOutput(body, output, "SKILL.md")
}

func scriptExportExecutor(args []string) bool {
	output := ""
	for _, a := range args {
		if strings.HasPrefix(a, "--output=") {
			output = strings.TrimPrefix(a, "--output=")
		}
	}
	body, err := apiGet("/api/v1/executor/export/skill")
	if err != nil {
		exitWithError(ExitConnectError, err.Error(), "")
	}
	return writeSkillOutput(body, output, "SKILL_EXECUTOR.md")
}

func scriptExportAdmin(args []string) bool {
	output := ""
	for _, a := range args {
		if strings.HasPrefix(a, "--output=") {
			output = strings.TrimPrefix(a, "--output=")
		}
	}
	body, err := apiGet("/api/v1/admin/export/skill")
	if err != nil {
		exitWithError(ExitConnectError, err.Error(), "")
	}
	return writeSkillOutput(body, output, "SKILL_ADMIN.md")
}

func writeSkillOutput(body []byte, output string, defaultName string) bool {
	content := string(body)
	var resp map[string]interface{}
	if json.Unmarshal(body, &resp) == nil {
		if md, ok := resp["content"].(string); ok {
			content = md
		} else if md, ok := resp["skill"].(string); ok {
			content = md
		}
	}
	if output != "" {
		if err := os.WriteFile(output, []byte(content), 0644); err != nil {
			exitWithError(ExitGeneralError, fmt.Sprintf("failed to write file: %v", err), "")
		}
		fmt.Fprintf(os.Stderr, "Exported to %s (%d bytes)\n", output, len(content))
	} else {
		fmt.Print(content)
	}
	return true
}

func printScriptHelp() {
	fmt.Print(`SCRIPT MANAGEMENT

  browserwing script <action> [options]

ACTIONS:
  get <id>                    Get script details
  create <file.json>          Create script from JSON file
  delete <id>                 Delete a script
  export [ids...] [--output=SKILL.md]    Export scripts as SKILL.md
  export-executor [--output=SKILL_EXECUTOR.md]  Export executor API as SKILL
  export-admin [--output=SKILL_ADMIN.md]        Export admin API as SKILL
  summary                     Get all scripts summary
  history                     List recent execution history
  result                      Get last execution result

EXAMPLES:
  browserwing script get bilibili-hot
  browserwing script create my-script.json
  browserwing script delete my-script-id
  browserwing script export --output=SKILL.md
  browserwing script export script-1 script-2 --output=SKILL.md
  browserwing script export-executor --output=SKILL_EXECUTOR.md
  browserwing script summary --format=json
`)
}

// --- explore (AI exploration) ---

func handleExplore(args []string) bool {
	if len(args) == 0 {
		printExploreHelp()
		return true
	}
	action := args[0]
	rest := args[1:]
	format := extractFormat(&rest, "json")

	switch action {
	case "start":
		return exploreStart(rest, format)
	case "stop":
		if len(rest) == 0 {
			exitWithError(ExitBadArgs, "session ID is required", "Usage: browserwing explore stop <session-id>")
		}
		return execPost("/api/v1/ai-explore/"+rest[0]+"/stop", nil, format)
	case "script":
		if len(rest) == 0 {
			exitWithError(ExitBadArgs, "session ID is required", "Usage: browserwing explore script <session-id>")
		}
		return execGet("/api/v1/ai-explore/"+rest[0]+"/script", format)
	case "save":
		if len(rest) == 0 {
			exitWithError(ExitBadArgs, "session ID is required", "Usage: browserwing explore save <session-id>")
		}
		return execPost("/api/v1/ai-explore/"+rest[0]+"/save", nil, format)
	case "help":
		printExploreHelp()
		return true
	default:
		exitWithError(ExitBadArgs, fmt.Sprintf("unknown explore action: %s", action),
			"Available: start, stop, script, save")
	}
	return true
}

func exploreStart(args []string, format string) bool {
	payload := map[string]interface{}{}
	for _, a := range args {
		if strings.HasPrefix(a, "--url=") {
			payload["start_url"] = strings.TrimPrefix(a, "--url=")
		} else if strings.HasPrefix(a, "--task=") {
			payload["task_desc"] = strings.TrimPrefix(a, "--task=")
		} else if strings.HasPrefix(a, "--llm=") {
			payload["llm_config_id"] = strings.TrimPrefix(a, "--llm=")
		}
	}
	if _, ok := payload["start_url"]; !ok {
		exitWithError(ExitBadArgs, "--url is required",
			"Usage: browserwing explore start --url=https://example.com --task=\"search for AI\"")
	}
	if _, ok := payload["task_desc"]; !ok {
		exitWithError(ExitBadArgs, "--task is required",
			"Usage: browserwing explore start --url=https://example.com --task=\"search for AI\"")
	}
	return execPost("/api/v1/ai-explore/start", payload, format)
}

func printExploreHelp() {
	fmt.Print(`AI EXPLORATION

  Use AI to autonomously browse websites and generate scripts.

  browserwing explore <action> [options]

ACTIONS:
  start [options]             Start AI exploration
  stop <session-id>           Stop an exploration session
  script <session-id>         Get the generated script
  save <session-id>           Save generated script to library

START OPTIONS:
  --url=<url>                 Starting URL (required)
  --task=<description>        Task description (required)
  --llm=<config-name>         LLM config to use

EXAMPLES:
  browserwing explore start --url=https://bilibili.com --task="search for AI and get video results"
  browserwing explore stop abc123
  browserwing explore script abc123
  browserwing explore save abc123
`)
}

// --- health check ---

func handleHealth(args []string) bool {
	format := "json"
	if len(args) > 0 {
		for _, a := range args {
			if strings.HasPrefix(a, "--format=") {
				format = strings.TrimPrefix(a, "--format=")
			}
		}
	}
	return execGet("/health", format)
}

// --- prompt management ---

func handlePrompt(args []string) bool {
	if len(args) == 0 {
		return execGet("/api/v1/prompts", "json")
	}
	action := args[0]
	rest := args[1:]
	format := extractFormat(&rest, "json")

	switch action {
	case "list", "ls":
		return execGet("/api/v1/prompts", format)
	case "get":
		if len(rest) == 0 {
			exitWithError(ExitBadArgs, "prompt ID is required",
				"System prompts: system-extractor, system-formfiller, system-aiagent, system-ai-explorer")
		}
		return execGet("/api/v1/prompts/"+rest[0], format)
	case "update", "set":
		return promptUpdate(rest, format)
	case "help":
		fmt.Print(`PROMPT MANAGEMENT

  browserwing prompt <action> [options]

ACTIONS:
  list                        List all system prompts
  get <id>                    Get a specific prompt
  update <id> --content="..." Update a prompt

SYSTEM PROMPT IDS:
  system-extractor            Data extraction prompt
  system-formfiller           Form filling prompt
  system-aiagent              AI agent prompt
  system-ai-explorer          AI exploration prompt

EXAMPLES:
  browserwing prompt list
  browserwing prompt get system-extractor
  browserwing prompt update system-extractor --content="Custom prompt..."
`)
		return true
	default:
		exitWithError(ExitBadArgs, fmt.Sprintf("unknown prompt action: %s", action),
			"Available: list, get, update")
	}
	return true
}

func promptUpdate(args []string, format string) bool {
	if len(args) == 0 {
		exitWithError(ExitBadArgs, "prompt ID is required", "Usage: browserwing prompt update <id> --content=\"...\"")
	}
	id := args[0]
	content := ""
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "--content=") {
			content = strings.TrimPrefix(a, "--content=")
		}
	}
	if content == "" {
		exitWithError(ExitBadArgs, "--content is required", "")
	}
	body, err := apiPut("/api/v1/prompts/"+id, map[string]interface{}{
		"content": content,
	})
	if err != nil {
		exitWithError(ExitConnectError, err.Error(), "")
	}
	printAPIResponse(body, format)
	return true
}

// --- MCP status ---

func handleMCP(args []string) bool {
	if len(args) == 0 {
		return execGet("/api/v1/mcp/status", "json")
	}
	action := args[0]
	rest := args[1:]
	format := extractFormat(&rest, "json")

	switch action {
	case "status":
		return execGet("/api/v1/mcp/status", format)
	case "commands", "tools":
		return execGet("/api/v1/mcp/commands", format)
	case "help":
		fmt.Print(`MCP (MODEL CONTEXT PROTOCOL)

  browserwing mcp <action>

ACTIONS:
  status          Show MCP server status
  commands        List all registered MCP tools

EXAMPLES:
  browserwing mcp status
  browserwing mcp commands
`)
		return true
	default:
		exitWithError(ExitBadArgs, fmt.Sprintf("unknown mcp action: %s", action),
			"Available: status, commands")
	}
	return true
}

// --- shared helpers ---

func extractFormat(args *[]string, defaultFmt string) string {
	format := defaultFmt
	var filtered []string
	for _, a := range *args {
		if strings.HasPrefix(a, "--format=") {
			format = strings.TrimPrefix(a, "--format=")
		} else {
			filtered = append(filtered, a)
		}
	}
	*args = filtered
	return format
}
