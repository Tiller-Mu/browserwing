package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
)

var placeholderRe = regexp.MustCompile(`\$\{(\w+)\}`)

func handleList(args []string) bool {
	format := "table"
	filter := ""    // "", "builtin", "user"
	search := ""    // fuzzy name match
	category := ""  // category filter
	limit := 0      // 0 = show all
	page := 1

	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--format="):
			format = strings.TrimPrefix(arg, "--format=")
		case arg == "--builtin":
			filter = "builtin"
		case arg == "--user" || arg == "--no-builtin":
			filter = "user"
		case strings.HasPrefix(arg, "--search="):
			search = strings.TrimPrefix(arg, "--search=")
		case strings.HasPrefix(arg, "--category=") || strings.HasPrefix(arg, "--cat="):
			if strings.HasPrefix(arg, "--cat=") {
				category = strings.TrimPrefix(arg, "--cat=")
			} else {
				category = strings.TrimPrefix(arg, "--category=")
			}
		case strings.HasPrefix(arg, "--limit="):
			fmt.Sscanf(strings.TrimPrefix(arg, "--limit="), "%d", &limit)
		case strings.HasPrefix(arg, "--page="):
			fmt.Sscanf(strings.TrimPrefix(arg, "--page="), "%d", &page)
		case !strings.HasPrefix(arg, "--") && search == "":
			search = arg
		}
	}

	query := url.Values{}
	query.Set("page_size", "200")
	if filter == "builtin" {
		query.Set("is_builtin", "true")
	} else if filter == "user" {
		query.Set("is_builtin", "false")
	}

	body, err := apiGet("/api/v1/scripts?" + query.Encode())
	if err != nil {
		exitWithError(ExitConnectError, err.Error(), "Make sure the server is running")
	}

	var resp struct {
		Scripts      []map[string]interface{} `json:"scripts"`
		Total        int                      `json:"total"`
		BuiltinCount int                      `json:"builtin_count"`
		UserCount    int                      `json:"user_count"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		exitWithError(ExitGeneralError, fmt.Sprintf("failed to parse response: %v", err), "")
	}
	scripts := resp.Scripts

	if search != "" {
		searchLower := strings.ToLower(search)
		var filtered []map[string]interface{}
		for _, s := range scripts {
			name, _ := s["name"].(string)
			desc, _ := s["description"].(string)
			id, _ := s["id"].(string)
			if fuzzyMatch(searchLower, strings.ToLower(name)) ||
				fuzzyMatch(searchLower, strings.ToLower(desc)) ||
				fuzzyMatch(searchLower, strings.ToLower(id)) {
				filtered = append(filtered, s)
			}
		}
		scripts = filtered
	}

	if category != "" {
		catLower := strings.ToLower(category)
		var filtered []map[string]interface{}
		for _, s := range scripts {
			tags, _ := s["tags"].([]interface{})
			group, _ := s["group"].(string)
			matched := strings.Contains(strings.ToLower(group), catLower)
			for _, t := range tags {
				if ts, ok := t.(string); ok && strings.Contains(strings.ToLower(ts), catLower) {
					matched = true
					break
				}
			}
			if matched {
				filtered = append(filtered, s)
			}
		}
		scripts = filtered
	}

	totalMatched := len(scripts)

	if limit > 0 && limit < len(scripts) {
		start := (page - 1) * limit
		if start >= len(scripts) {
			start = 0
		}
		end := start + limit
		if end > len(scripts) {
			end = len(scripts)
		}
		scripts = scripts[start:end]
	}

	if len(scripts) == 0 {
		if search != "" || category != "" || filter != "" {
			fmt.Fprintf(os.Stderr, "No scripts found matching the criteria.\n")
		} else {
			fmt.Fprintln(os.Stderr, "No scripts found. Create scripts via the web UI first.")
		}
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
			if len(id) > 20 {
				id = id[:20] + "…"
			}
			if len(desc) > 45 {
				desc = desc[:45] + "…"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", id, name, desc, actions)
		}
		tw.Flush()
	}

	printListSummary(len(scripts), totalMatched, resp.BuiltinCount, resp.UserCount, limit, page, filter, search, category)
	return true
}

func printListSummary(shown, matched, builtinCount, userCount, limit, page int, filter, search, category string) {
	parts := []string{}
	if shown < matched {
		parts = append(parts, fmt.Sprintf("Showing %d of %d", shown, matched))
	} else {
		parts = append(parts, fmt.Sprintf("%d scripts", shown))
	}
	if filter == "builtin" {
		parts = append(parts, "builtin only")
	} else if filter == "user" {
		parts = append(parts, "user only")
	} else if builtinCount > 0 || userCount > 0 {
		parts = append(parts, fmt.Sprintf("%d builtin + %d user", builtinCount, userCount))
	}
	if search != "" {
		parts = append(parts, fmt.Sprintf("search: %q", search))
	}
	if category != "" {
		parts = append(parts, fmt.Sprintf("category: %s", category))
	}
	fmt.Fprintf(os.Stderr, "\n  %s\n", strings.Join(parts, " | "))

	if limit > 0 && shown < matched {
		totalPages := (matched + limit - 1) / limit
		fmt.Fprintf(os.Stderr, "  Page %d/%d. Next: --limit=%d --page=%d\n", page, totalPages, limit, page+1)
	}
}

func fuzzyMatch(needle, haystack string) bool {
	if strings.Contains(haystack, needle) {
		return true
	}
	words := strings.Fields(needle)
	if len(words) <= 1 {
		return false
	}
	for _, w := range words {
		if !strings.Contains(haystack, w) {
			return false
		}
	}
	return true
}

// extractParams collects parameter info from mcp_input_schema, variables, and action placeholders.
func extractParams(s map[string]interface{}) map[string]string {
	params := make(map[string]string)

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

	if vars, ok := s["variables"].(map[string]interface{}); ok {
		for k, v := range vars {
			if _, exists := params[k]; !exists {
				params[k] = fmt.Sprintf("default: %v", v)
			}
		}
	}

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
