package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func handleExec(args []string) bool {
	if len(args) == 0 {
		printExecHelp()
		return true
	}

	action := args[0]
	rest := args[1:]
	format := "json"

	var filtered []string
	for _, a := range rest {
		if strings.HasPrefix(a, "--format=") {
			format = strings.TrimPrefix(a, "--format=")
		} else {
			filtered = append(filtered, a)
		}
	}

	switch action {
	case "navigate", "nav", "goto":
		return execNavigate(filtered, format)
	case "snapshot", "snap":
		return execSnapshot(format)
	case "click":
		return execClick(filtered, format)
	case "type":
		return execType(filtered, format)
	case "extract":
		return execExtract(filtered, format)
	case "wait":
		return execWait(filtered, format)
	case "press-key", "key":
		return execPressKey(filtered, format)
	case "screenshot":
		return execScreenshot(filtered)
	case "evaluate", "eval":
		return execEvaluate(filtered, format)
	case "page-info", "info":
		return execPageInfo(format)
	case "page-text":
		return execGet("/api/v1/executor/page-text", format)
	case "fill-form", "form":
		return execFillForm(filtered, format)
	case "tabs", "tab":
		return execTabs(filtered, format)
	case "batch":
		return execBatch(filtered, format)
	case "scroll":
		return execPost("/api/v1/executor/scroll-to-bottom", nil, format)
	case "back":
		return execPost("/api/v1/executor/go-back", nil, format)
	case "forward":
		return execPost("/api/v1/executor/go-forward", nil, format)
	case "reload":
		return execPost("/api/v1/executor/reload", nil, format)
	case "hover":
		return execIdentifierAction("/api/v1/executor/hover", filtered, format)
	case "select":
		return execSelect(filtered, format)
	case "clickable":
		return execGet("/api/v1/executor/clickable-elements", format)
	case "inputs":
		return execGet("/api/v1/executor/input-elements", format)
	case "help":
		if len(filtered) > 0 {
			body, err := apiGet("/api/v1/executor/help?command=" + filtered[0])
			if err == nil {
				printAPIResponse(body, format)
				return true
			}
			fmt.Fprintf(os.Stderr, "(server unavailable, showing local help)\n\n")
		}
		printExecHelp()
		return true
	default:
		exitWithError(ExitBadArgs, fmt.Sprintf("unknown exec action: %s", action),
			"Run 'browserwing exec help' to see available actions")
	}
	return true
}

func execNavigate(args []string, format string) bool {
	if len(args) == 0 {
		exitWithError(ExitBadArgs, "URL is required", "Usage: browserwing exec navigate <url>")
	}
	return execPost("/api/v1/executor/navigate", map[string]interface{}{
		"url": args[0],
	}, format)
}

func execSnapshot(format string) bool {
	body, err := apiGet("/api/v1/executor/snapshot")
	if err != nil {
		exitWithError(ExitConnectError, err.Error(), "")
	}
	if format == "json" {
		var parsed interface{}
		if json.Unmarshal(body, &parsed) == nil {
			out, _ := json.MarshalIndent(parsed, "", "  ")
			fmt.Println(string(out))
		} else {
			fmt.Println(string(body))
		}
	} else {
		var resp map[string]interface{}
		json.Unmarshal(body, &resp)
		if text, ok := resp["snapshot_text"].(string); ok {
			fmt.Println(text)
		} else if text, ok := resp["snapshot"].(string); ok {
			fmt.Println(text)
		} else {
			fmt.Println(string(body))
		}
	}
	return true
}

func execClick(args []string, format string) bool {
	if len(args) == 0 {
		exitWithError(ExitBadArgs, "identifier is required",
			"Usage: browserwing exec click <@e1|#id|.class|text>")
	}
	return execPost("/api/v1/executor/click", map[string]interface{}{
		"identifier": args[0],
	}, format)
}

func execType(args []string, format string) bool {
	if len(args) < 2 {
		exitWithError(ExitBadArgs, "identifier and text are required",
			"Usage: browserwing exec type <identifier> <text>")
	}
	text := strings.Join(args[1:], " ")
	return execPost("/api/v1/executor/type", map[string]interface{}{
		"identifier": args[0],
		"text":       text,
	}, format)
}

func execExtract(args []string, format string) bool {
	selector := ""
	fields := []string{"text"}
	multiple := false
	for _, a := range args {
		if strings.HasPrefix(a, "--selector=") {
			selector = strings.TrimPrefix(a, "--selector=")
		} else if strings.HasPrefix(a, "--fields=") {
			fields = strings.Split(strings.TrimPrefix(a, "--fields="), ",")
		} else if a == "--multiple" {
			multiple = true
		} else if selector == "" {
			selector = a
		}
	}
	if selector == "" {
		exitWithError(ExitBadArgs, "selector is required",
			"Usage: browserwing exec extract <selector> [--fields=text,href] [--multiple]")
	}
	return execPost("/api/v1/executor/extract", map[string]interface{}{
		"selector": selector,
		"fields":   fields,
		"multiple": multiple,
	}, format)
}

func execWait(args []string, format string) bool {
	if len(args) == 0 {
		exitWithError(ExitBadArgs, "identifier is required",
			"Usage: browserwing exec wait <identifier> [--state=visible] [--timeout=10]")
	}
	identifier := args[0]
	state := "visible"
	timeout := 10
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "--state=") {
			state = strings.TrimPrefix(a, "--state=")
		} else if strings.HasPrefix(a, "--timeout=") {
			if t, err := strconv.Atoi(strings.TrimPrefix(a, "--timeout=")); err == nil {
				timeout = t
			}
		}
	}
	return execPost("/api/v1/executor/wait", map[string]interface{}{
		"identifier": identifier,
		"state":      state,
		"timeout":    timeout,
	}, format)
}

func execPressKey(args []string, format string) bool {
	if len(args) == 0 {
		exitWithError(ExitBadArgs, "key is required",
			"Usage: browserwing exec press-key <Enter|Tab|Escape|Ctrl+S|...>")
	}
	return execPost("/api/v1/executor/press-key", map[string]interface{}{
		"key": args[0],
	}, format)
}

func execScreenshot(args []string) bool {
	output := "screenshot.png"
	for _, a := range args {
		if strings.HasPrefix(a, "--output=") {
			output = strings.TrimPrefix(a, "--output=")
		} else if !strings.HasPrefix(a, "--") {
			output = a
		}
	}
	body, err := apiPost("/api/v1/executor/screenshot", nil)
	if err != nil {
		exitWithError(ExitConnectError, err.Error(), "")
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		exitWithError(ExitGeneralError, "failed to parse screenshot response", "")
	}

	dataField, _ := resp["data"].(map[string]interface{})
	b64 := ""
	if dataField != nil {
		b64, _ = dataField["data"].(string)
		if b64 == "" {
			b64, _ = dataField["screenshot"].(string)
		}
	}
	if b64 == "" {
		b64, _ = resp["screenshot"].(string)
	}
	if b64 == "" {
		exitWithError(ExitGeneralError, "no screenshot data in response", "")
	}

	imgData, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		exitWithError(ExitGeneralError, "failed to decode screenshot", "")
	}
	if err := os.WriteFile(output, imgData, 0644); err != nil {
		exitWithError(ExitGeneralError, fmt.Sprintf("failed to write file: %v", err), "")
	}
	fmt.Fprintf(os.Stderr, "Screenshot saved to %s (%d bytes)\n", output, len(imgData))
	return true
}

func execEvaluate(args []string, format string) bool {
	if len(args) == 0 {
		exitWithError(ExitBadArgs, "JavaScript code is required",
			"Usage: browserwing exec eval '<js code>'\n  Example: browserwing exec eval 'document.title'")
	}
	code := strings.Join(args, " ")
	return execPost("/api/v1/executor/evaluate", map[string]interface{}{
		"script": code,
	}, format)
}

func execPageInfo(format string) bool {
	return execGet("/api/v1/executor/page-info", format)
}

func execFillForm(args []string, format string) bool {
	var fields []map[string]interface{}
	submit := false
	timeout := 10

	for _, a := range args {
		if a == "--submit" {
			submit = true
		} else if strings.HasPrefix(a, "--timeout=") {
			if t, err := strconv.Atoi(strings.TrimPrefix(a, "--timeout=")); err == nil {
				timeout = t
			}
		} else if strings.HasPrefix(a, "--field=") {
			kv := strings.TrimPrefix(a, "--field=")
			parts := strings.SplitN(kv, ":", 2)
			if len(parts) == 2 {
				fields = append(fields, map[string]interface{}{
					"name":  parts[0],
					"value": parts[1],
				})
			}
		}
	}
	if len(fields) == 0 {
		exitWithError(ExitBadArgs, "at least one --field=name:value is required",
			"Usage: browserwing exec fill-form --field=email:user@test.com --field=password:123 [--submit]")
	}
	return execPost("/api/v1/executor/fill-form", map[string]interface{}{
		"fields":  fields,
		"submit":  submit,
		"timeout": timeout,
	}, format)
}

func execTabs(args []string, format string) bool {
	action := "list"
	if len(args) > 0 {
		action = args[0]
	}
	payload := map[string]interface{}{"action": action}
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "--index=") {
			if idx, err := strconv.Atoi(strings.TrimPrefix(a, "--index=")); err == nil {
				payload["index"] = idx
			}
		} else if strings.HasPrefix(a, "--url=") {
			payload["url"] = strings.TrimPrefix(a, "--url=")
		}
	}
	return execPost("/api/v1/executor/tabs", payload, format)
}

func execBatch(args []string, format string) bool {
	if len(args) == 0 {
		exitWithError(ExitBadArgs, "JSON file or inline JSON is required",
			"Usage: browserwing exec batch <file.json>\n  or:   browserwing exec batch '{\"operations\":[...]}'")
	}
	input := args[0]
	var payload interface{}

	if strings.HasPrefix(input, "{") || strings.HasPrefix(input, "[") {
		if err := json.Unmarshal([]byte(input), &payload); err != nil {
			exitWithError(ExitBadArgs, fmt.Sprintf("invalid JSON: %v", err), "")
		}
	} else {
		data, err := os.ReadFile(input)
		if err != nil {
			exitWithError(ExitBadArgs, fmt.Sprintf("failed to read file: %v", err), "")
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			exitWithError(ExitBadArgs, fmt.Sprintf("invalid JSON in file: %v", err), "")
		}
	}
	return execPost("/api/v1/executor/batch", payload, format)
}

func execSelect(args []string, format string) bool {
	if len(args) < 2 {
		exitWithError(ExitBadArgs, "identifier and value are required",
			"Usage: browserwing exec select <identifier> <value>")
	}
	return execPost("/api/v1/executor/select", map[string]interface{}{
		"identifier": args[0],
		"value":      args[1],
	}, format)
}

func execIdentifierAction(path string, args []string, format string) bool {
	if len(args) == 0 {
		exitWithError(ExitBadArgs, "identifier is required", "")
	}
	return execPost(path, map[string]interface{}{
		"identifier": args[0],
	}, format)
}

// --- generic helpers ---

func execGet(path string, format string) bool {
	body, err := apiGet(path)
	if err != nil {
		exitWithError(ExitConnectError, err.Error(), "")
	}
	printAPIResponse(body, format)
	return true
}

func execPost(path string, payload interface{}, format string) bool {
	body, err := apiPost(path, payload)
	if err != nil {
		exitWithError(ExitConnectError, err.Error(), "")
	}
	printAPIResponse(body, format)
	return true
}

func printAPIResponse(body []byte, format string) {
	if format == "json" {
		var parsed interface{}
		if json.Unmarshal(body, &parsed) == nil {
			out, _ := json.MarshalIndent(parsed, "", "  ")
			fmt.Println(string(out))
		} else {
			fmt.Println(string(body))
		}
		return
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		fmt.Println(string(body))
		return
	}
	if msg, ok := resp["message"].(string); ok {
		success, _ := resp["success"].(bool)
		if success {
			fmt.Printf("[OK] %s\n", msg)
		} else {
			fmt.Printf("[FAIL] %s\n", msg)
		}
	}
	if data, ok := resp["data"]; ok {
		formatOutput(data, format)
	}
}

func printExecHelp() {
	fmt.Print(`BROWSER CONTROL (exec)

  browserwing exec <action> [args] [--format=json|table]

NAVIGATION:
  navigate <url>              Open a URL
  back                        Go back in history
  forward                     Go forward in history
  reload                      Reload current page
  scroll                      Scroll to bottom

PAGE ANALYSIS:
  snapshot                    Get accessibility tree with element RefIDs (@e1, @e2...)
  page-info                   Get current URL and title
  page-text                   Get all visible text
  clickable                   List all clickable elements
  inputs                      List all input elements

INTERACTION:
  click <identifier>          Click element (@e1, #id, .class, text)
  type <identifier> <text>    Type text into element
  select <identifier> <value> Select dropdown option
  hover <identifier>          Hover over element
  press-key <key>             Press keyboard key (Enter, Tab, Escape, Ctrl+S...)
  wait <id> [--state=visible] [--timeout=10]  Wait for element state

DATA EXTRACTION:
  extract <selector> [--fields=text,href] [--multiple]  Extract data from elements

FORMS:
  fill-form --field=name:value [--field=email:val] [--submit]  Fill form fields

TABS:
  tabs list                   List all browser tabs
  tabs new [--url=<url>]      Open a new tab
  tabs switch --index=<N>     Switch to tab by index (0-based)
  tabs close --index=<N>      Close a tab by index

ADVANCED:
  screenshot [--output=file]  Take screenshot (saves to screenshot.png by default)
  eval '<js code>'            Execute JavaScript in page context
  batch <file.json>           Execute batch operations from JSON file
  help [command]              Show executor API help

IDENTIFIER FORMATS:
  @e1           RefID from snapshot (most reliable)
  #my-button    CSS selector (ID)
  .submit-btn   CSS selector (class)
  Login         Text content match
  //button      XPath expression

EXAMPLES:
  browserwing exec navigate https://example.com
  browserwing exec snapshot --format=table
  browserwing exec click @e3
  browserwing exec type @e5 "hello world"
  browserwing exec extract ".product" --fields=text,href --multiple
  browserwing exec press-key Enter
  browserwing exec screenshot --output=page.png
  browserwing exec eval 'document.title'
  browserwing exec fill-form --field=user:admin --field=pass:123 --submit
  browserwing exec tabs new --url=https://google.com
`)
}
