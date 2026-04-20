package cli

import (
	"encoding/json"
	"fmt"
	"strings"
)

func handleRun(args []string) bool {
	if len(args) == 0 {
		exitWithError(ExitBadArgs,
			"script name or ID is required",
			"Usage: browserwing run <name|id> [--key=value...]\n  Examples: browserwing run bilibili-hot")
	}

	scriptRef := args[0]
	params := make(map[string]string)
	format := "json"
	headless := true

	for _, arg := range args[1:] {
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		kv := strings.TrimPrefix(arg, "--")
		if kv == "no-headless" || kv == "no-headless=true" {
			headless = false
			continue
		}
		if kv == "headless" || kv == "headless=true" {
			headless = true
			continue
		}
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			if parts[0] == "format" {
				format = parts[1]
			} else {
				params[parts[0]] = parts[1]
			}
		}
	}

	// Resolve script ID by name if needed
	scriptID, err := resolveScriptID(scriptRef)
	if err != nil {
		if strings.Contains(err.Error(), "cannot connect") {
			exitWithError(ExitConnectError, err.Error(), "Make sure the server is running")
		}
		exitWithError(ExitScriptNotFound, err.Error(), "Use 'browserwing ls' to see available scripts")
	}

	spinMsg := fmt.Sprintf("Running script: %s", scriptRef)
	if headless {
		spinMsg += " (headless)"
	}
	sp := newSpinner(spinMsg)
	sp.Start()

	payload := map[string]interface{}{
		"params":   params,
		"headless": headless,
	}

	body, err := apiPost(fmt.Sprintf("/api/v1/scripts/%s/play", scriptID), payload)
	if err != nil {
		sp.Stop("[FAIL]")
		exitWithError(ExitConnectError, err.Error(), "")
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		exitWithError(ExitGeneralError, fmt.Sprintf("failed to parse response: %v", err), "")
	}

	// API returns {"message": ..., "result": {"success": bool, "extracted_data": {...}}}
	result, _ := resp["result"].(map[string]interface{})
	if result == nil {
		// fallback: maybe response is the result itself
		result = resp
	}

	success, _ := result["success"].(bool)
	if !success {
		sp.Stop("[FAIL]")
		msg, _ := result["message"].(string)
		if msg == "" {
			msg, _ = resp["error"].(string)
		}
		if msg == "" {
			msg = "unknown error"
		}
		exitWithError(ExitScriptFailed, fmt.Sprintf("script execution failed: %s", msg), "")
	}

	extractedData, _ := result["extracted_data"].(map[string]interface{})
	if len(extractedData) == 0 {
		sp.Stop("[OK] No data extracted")
		return true
	}

	displayData := findDisplayData(extractedData)
	sp.Stop("[OK] Done")
	formatOutput(displayData, format)
	return true
}

func resolveScriptID(ref string) (string, error) {
	body, err := apiGet("/api/v1/scripts?page_size=100")
	if err != nil {
		return "", err
	}

	var resp struct {
		Scripts []map[string]interface{} `json:"scripts"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("failed to parse scripts list: %w", err)
	}
	scripts := resp.Scripts

	for _, s := range scripts {
		if id, _ := s["id"].(string); id == ref {
			return id, nil
		}
	}

	for _, s := range scripts {
		name, _ := s["name"].(string)
		if strings.EqualFold(name, ref) {
			return s["id"].(string), nil
		}
	}

	// Try MCP command name match
	for _, s := range scripts {
		mcpName, _ := s["mcp_command_name"].(string)
		if strings.EqualFold(mcpName, ref) {
			return s["id"].(string), nil
		}
	}

	var candidates []map[string]interface{}
	for _, s := range scripts {
		name, _ := s["name"].(string)
		if strings.Contains(strings.ToLower(name), strings.ToLower(ref)) {
			candidates = append(candidates, s)
		}
	}

	if len(candidates) == 1 {
		return candidates[0]["id"].(string), nil
	}
	if len(candidates) > 1 {
		names := make([]string, len(candidates))
		for i, c := range candidates {
			names[i] = fmt.Sprintf("  - %s (id: %s)", c["name"], c["id"])
		}
		return "", fmt.Errorf("ambiguous script name %q, matches:\n%s", ref, strings.Join(names, "\n"))
	}

	return "", fmt.Errorf("script not found: %q\nUse 'browserwing list' to see available scripts", ref)
}

func findDisplayData(data map[string]interface{}) interface{} {
	if len(data) == 1 {
		for _, v := range data {
			return v
		}
	}
	return data
}
