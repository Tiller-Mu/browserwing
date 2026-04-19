// Command export-scripts generates the builtin-scripts.json file.
// Usage: go run ./cmd/export-scripts > ../builtin-scripts.json
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/browserwing/browserwing/builtin"
	"github.com/browserwing/browserwing/models"
)

type compactAction struct {
	Type         string `json:"type"`
	URL          string `json:"url,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	JSCode       string `json:"js_code,omitempty"`
	VariableName string `json:"variable_name,omitempty"`
	Selector     string `json:"selector,omitempty"`
	Value        string `json:"value,omitempty"`
	XPath        string `json:"xpath,omitempty"`
}

type compactScript struct {
	ID                    string                 `json:"id"`
	Name                  string                 `json:"name"`
	Description           string                 `json:"description"`
	URL                   string                 `json:"url"`
	Tags                  []string               `json:"tags"`
	Group                 string                 `json:"group"`
	CanFetch              bool                   `json:"can_fetch,omitempty"`
	RequiresLogin         bool                   `json:"requires_login,omitempty"`
	IsMCPCommand          bool                   `json:"is_mcp_command,omitempty"`
	MCPCommandName        string                 `json:"mcp_command_name,omitempty"`
	MCPCommandDescription string                 `json:"mcp_command_description,omitempty"`
	MCPInputSchema        map[string]interface{} `json:"mcp_input_schema,omitempty"`
	Variables             map[string]string      `json:"variables,omitempty"`
	Actions               []compactAction        `json:"actions"`
}

func toCompact(s models.Script) compactScript {
	actions := make([]compactAction, len(s.Actions))
	for i, a := range s.Actions {
		actions[i] = compactAction{
			Type:         a.Type,
			URL:          a.URL,
			Duration:     a.Duration,
			JSCode:       a.JSCode,
			VariableName: a.VariableName,
			Selector:     a.Selector,
			Value:        a.Value,
			XPath:        a.XPath,
		}
	}
	return compactScript{
		ID:                    s.ID,
		Name:                  s.Name,
		Description:           s.Description,
		URL:                   s.URL,
		Tags:                  s.Tags,
		Group:                 s.Group,
		CanFetch:              s.CanFetch,
		RequiresLogin:         s.RequiresLogin,
		IsMCPCommand:          s.IsMCPCommand,
		MCPCommandName:        s.MCPCommandName,
		MCPCommandDescription: s.MCPCommandDescription,
		MCPInputSchema:        s.MCPInputSchema,
		Variables:             s.Variables,
		Actions:               actions,
	}
}

func main() {
	scripts := builtin.GetBuiltinScripts()
	compact := make([]compactScript, len(scripts))
	for i, s := range scripts {
		compact[i] = toCompact(s)
	}
	data, err := json.MarshalIndent(compact, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}
