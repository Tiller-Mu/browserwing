package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
)

var placeholderRe = regexp.MustCompile(`\$\{(\w+)\}`)

func handleList(args []string) bool {
	format := "table"
	for _, arg := range args {
		if strings.HasPrefix(arg, "--format=") {
			format = strings.TrimPrefix(arg, "--format=")
		}
	}

	body, err := apiGet("/api/v1/scripts?page_size=100")
	if err != nil {
		exitWithError(ExitConnectError, err.Error(), "Make sure the server is running")
	}

	var resp struct {
		Scripts []map[string]interface{} `json:"scripts"`
		Total   int                      `json:"total"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		exitWithError(ExitGeneralError, fmt.Sprintf("failed to parse response: %v", err), "")
	}
	scripts := resp.Scripts

	if len(scripts) == 0 {
		fmt.Println("No scripts found. Create scripts via the web UI first.")
		return true
	}

	switch format {
	case "json":
		compact := make([]map[string]interface{}, 0, len(scripts))
		for _, s := range scripts {
			item := map[string]interface{}{
				"id":   s["id"],
				"name": s["name"],
			}
			if d, ok := s["description"].(string); ok && d != "" {
				item["description"] = d
			}
			if v, ok := s["requires_login"].(bool); ok && v {
				item["requires_login"] = true
			}
			params := extractParams(s)
			if len(params) > 0 {
				item["params"] = params
			}
			if v, ok := s["mcp_command_name"].(string); ok && v != "" {
				item["mcp_command_name"] = v
			}
			if tags, ok := s["tags"].([]interface{}); ok && len(tags) > 0 {
				item["tags"] = tags
			}
			if a, ok := s["actions"].([]interface{}); ok {
				item["steps"] = len(a)
			}
			compact = append(compact, item)
		}
		out, _ := json.MarshalIndent(compact, "", "  ")
		fmt.Println(string(out))
	case "csv":
		fmt.Println("id,name,description,actions")
		for _, s := range scripts {
			actions := "0"
			if a, ok := s["actions"].([]interface{}); ok {
				actions = fmt.Sprintf("%d", len(a))
			}
			desc := strings.ReplaceAll(fmt.Sprintf("%v", s["description"]), ",", " ")
			fmt.Printf("%s,%s,%s,%s\n", s["id"], s["name"], desc, actions)
		}
	default:
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tDESCRIPTION\tSTEPS")
		fmt.Fprintln(tw, "---\t---\t---\t---")
		for _, s := range scripts {
			id, _ := s["id"].(string)
			name, _ := s["name"].(string)
			desc, _ := s["description"].(string)
			actions := 0
			if a, ok := s["actions"].([]interface{}); ok {
				actions = len(a)
			}
			if len(id) > 12 {
				id = id[:12] + "…"
			}
			if len(desc) > 40 {
				desc = desc[:40] + "…"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", id, name, desc, actions)
		}
		tw.Flush()
	}

	return true
}

// extractParams collects parameter info from mcp_input_schema, variables, and action placeholders.
func extractParams(s map[string]interface{}) map[string]string {
	params := make(map[string]string)

	// 1. From mcp_input_schema (has descriptions)
	if schema, ok := s["mcp_input_schema"].(map[string]interface{}); ok {
		if props, ok := schema["properties"].(map[string]interface{}); ok {
			for k, v := range props {
				if detail, ok := v.(map[string]interface{}); ok {
					desc, _ := detail["description"].(string)
					if desc == "" {
						desc, _ = detail["type"].(string)
					}
					params[k] = desc
				}
			}
		}
	}

	// 2. From variables (default values)
	if vars, ok := s["variables"].(map[string]interface{}); ok {
		for k, v := range vars {
			if _, exists := params[k]; !exists {
				params[k] = fmt.Sprintf("default: %v", v)
			}
		}
	}

	// 3. Scan actions for ${placeholder} patterns
	if actions, ok := s["actions"].([]interface{}); ok {
		for _, a := range actions {
			action, ok := a.(map[string]interface{})
			if !ok {
				continue
			}
			for _, field := range []string{"url", "value", "selector", "xpath", "js_code"} {
				if str, ok := action[field].(string); ok {
					matches := placeholderRe.FindAllStringSubmatch(str, -1)
					for _, m := range matches {
						name := m[1]
						if _, exists := params[name]; !exists {
							params[name] = ""
						}
					}
				}
			}
		}
	}

	return params
}
