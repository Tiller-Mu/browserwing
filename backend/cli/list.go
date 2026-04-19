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
		out, _ := json.MarshalIndent(scripts, "", "  ")
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
