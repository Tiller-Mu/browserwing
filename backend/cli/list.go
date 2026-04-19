package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
)

func handleList(args []string) bool {
	format := "table"
	for _, arg := range args {
		if strings.HasPrefix(arg, "--format=") {
			format = strings.TrimPrefix(arg, "--format=")
		}
	}

	body, err := apiGet("/api/v1/scripts?page_size=100")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// API returns {"scripts": [...], "total": N, ...}
	var resp struct {
		Scripts []map[string]interface{} `json:"scripts"`
		Total   int                      `json:"total"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		os.Exit(1)
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
			if v, ok := s["variables"].(map[string]interface{}); ok && len(v) > 0 {
				item["variables"] = v
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
