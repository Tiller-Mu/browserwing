package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func handleRun(args []string) bool {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Error: script name or ID is required")
		fmt.Fprintln(os.Stderr, "Usage: browserwing run <name|id> [--key=value...]")
		os.Exit(1)
	}

	scriptRef := args[0]
	params := make(map[string]string)
	format := "table"

	for _, arg := range args[1:] {
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		kv := strings.TrimPrefix(arg, "--")
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
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Execute the script
	fmt.Fprintf(os.Stderr, "Running script: %s ...\n", scriptRef)

	payload := map[string]interface{}{
		"params": params,
	}

	body, err := apiPost(fmt.Sprintf("/api/v1/scripts/%s/play", scriptID), payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		os.Exit(1)
	}

	success, _ := result["success"].(bool)
	if !success {
		msg, _ := result["error"].(string)
		if msg == "" {
			msg, _ = result["message"].(string)
		}
		fmt.Fprintf(os.Stderr, "Script execution failed: %s\n", msg)
		os.Exit(1)
	}

	// Get extracted data
	extractedData, _ := result["extracted_data"].(map[string]interface{})
	if len(extractedData) == 0 {
		fmt.Fprintf(os.Stderr, "✓ Script completed successfully (no data extracted)\n")
		return true
	}

	// Find the main data to display
	displayData := findDisplayData(extractedData)
	fmt.Fprintf(os.Stderr, "✓ Script completed successfully\n\n")
	formatOutput(displayData, format)
	return true
}

func resolveScriptID(ref string) (string, error) {
	body, err := apiGet("/api/v1/scripts")
	if err != nil {
		return "", err
	}

	var scripts []map[string]interface{}
	if err := json.Unmarshal(body, &scripts); err != nil {
		return "", fmt.Errorf("failed to parse scripts list: %w", err)
	}

	// Try exact ID match first
	for _, s := range scripts {
		if id, _ := s["id"].(string); id == ref {
			return id, nil
		}
	}

	// Try name match (case-insensitive)
	for _, s := range scripts {
		name, _ := s["name"].(string)
		if strings.EqualFold(name, ref) {
			return s["id"].(string), nil
		}
	}

	// Try partial name match
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

// findDisplayData extracts the primary data from extracted_data map.
// If there's only one key, use its value. Otherwise return the whole map.
func findDisplayData(data map[string]interface{}) interface{} {
	if len(data) == 1 {
		for _, v := range data {
			return v
		}
	}
	return data
}
