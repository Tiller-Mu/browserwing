// Command export-scripts generates the builtin-scripts/ directory with
// categorized JSON files and an index.json for concurrent loading.
// It also generates the legacy single builtin-scripts.json for backward compat.
//
// Usage: go run ./cmd/export-scripts
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/browserwing/browserwing/builtin"
	"github.com/browserwing/browserwing/models"
)

type compactAction struct {
	Type         string   `json:"type"`
	URL          string   `json:"url,omitempty"`
	Duration     int      `json:"duration,omitempty"`
	JSCode       string   `json:"js_code,omitempty"`
	VariableName string   `json:"variable_name,omitempty"`
	Selector     string   `json:"selector,omitempty"`
	Value        string   `json:"value,omitempty"`
	XPath        string   `json:"xpath,omitempty"`
	FilePaths    []string `json:"file_paths,omitempty"`
	Multiple     bool     `json:"multiple,omitempty"`
	Key          string   `json:"key,omitempty"`
}

type compactScript struct {
	ID                    string                 `json:"id"`
	Name                  string                 `json:"name"`
	Description           string                 `json:"description"`
	URL                   string                 `json:"url"`
	Tags                  []string               `json:"tags"`
	Group                 string                 `json:"group"`
	Category              string                 `json:"category,omitempty"`
	CanFetch              bool                   `json:"can_fetch,omitempty"`
	RequiresLogin         bool                   `json:"requires_login,omitempty"`
	IsMCPCommand          bool                   `json:"is_mcp_command,omitempty"`
	MCPCommandName        string                 `json:"mcp_command_name,omitempty"`
	MCPCommandDescription string                 `json:"mcp_command_description,omitempty"`
	MCPInputSchema        map[string]interface{} `json:"mcp_input_schema,omitempty"`
	Variables             map[string]string      `json:"variables,omitempty"`
	Actions               []compactAction        `json:"actions"`
}

var categoryMap = map[string]string{
	"bilibili": "social", "zhihu": "social", "weibo": "social",
	"douyin": "social", "tieba": "social", "xiaohongshu": "social",
	"twitter": "social", "reddit": "social", "facebook": "social",
	"instagram": "social", "bluesky": "social",
	"jike": "social", "zsxq": "social",

	"douban": "entertainment", "hupu": "entertainment", "imdb": "entertainment",
	"youtube": "entertainment", "bilibili-ranking": "entertainment",
	"steam": "entertainment", "pixiv": "entertainment",
	"apple-podcasts": "entertainment", "tiktok": "entertainment",

	"github": "tech", "hackernews": "tech", "v2ex": "tech",
	"stackoverflow": "tech", "linux-do": "tech", "juejin": "tech",
	"producthunt": "tech", "hf": "tech", "devto": "tech",
	"lobsters": "tech", "arxiv": "tech", "lesswrong": "tech",
	"gitee": "tech", "nowcoder": "tech",

	"36kr": "news", "toutiao": "news", "bbc": "news",
	"bloomberg": "news", "reuters": "news", "google-news": "news",
	"weixin": "news",

	"eastmoney": "finance", "sinafinance": "finance", "xueqiu": "finance",
	"binance": "finance", "yahoo-finance": "finance", "barchart": "finance",
	"ths": "finance", "tdx": "finance",

	"jd": "shopping", "taobao": "shopping", "smzdm": "shopping",
	"1688": "shopping", "amazon": "shopping", "xianyu": "shopping",
	"coupang": "shopping",

	"boss": "jobs", "linkedin": "jobs", "maimai": "jobs",

	"google-scholar": "search", "google-trends": "search",
	"google": "search", "baidu-scholar": "search",
	"wikipedia": "search", "wanfang": "search", "cnki": "search",

	"weread": "reading", "medium": "reading", "substack": "reading",
	"dictionary": "reading",

	"sinablog": "social",

	"gov-policy": "news", "gov-law": "news",

	"ctrip": "other", "jianyu": "other", "ke": "other",
}

func classifyScript(name string) string {
	lower := strings.ToLower(name)
	bestPrefix := ""
	bestCat := "other"
	for prefix, cat := range categoryMap {
		if strings.HasPrefix(lower, prefix) && len(prefix) > len(bestPrefix) {
			bestPrefix = prefix
			bestCat = cat
		}
	}
	return bestCat
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
			FilePaths:    a.FilePaths,
			Multiple:     a.Multiple,
			Key:          a.Key,
		}
	}
	cat := classifyScript(s.Name)
	return compactScript{
		ID:                    s.ID,
		Name:                  s.Name,
		Description:           s.Description,
		URL:                   s.URL,
		Tags:                  s.Tags,
		Group:                 s.Group,
		Category:              cat,
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

	// Classify into categories
	categories := make(map[string][]compactScript)
	var allCompact []compactScript
	for _, s := range scripts {
		c := toCompact(s)
		allCompact = append(allCompact, c)
		categories[c.Category] = append(categories[c.Category], c)
	}

	// Output directory (relative to CWD, run from project root)
	outDir := "builtin-scripts"
	os.MkdirAll(outDir, 0o755)

	// Write category files
	var fileNames []string
	for cat, catScripts := range categories {
		fileName := cat + ".json"
		fileNames = append(fileNames, fileName)
		data, _ := json.MarshalIndent(catScripts, "", "  ")
		outPath := filepath.Join(outDir, fileName)
		os.WriteFile(outPath, data, 0o644)
		fmt.Fprintf(os.Stderr, "  %s: %d scripts\n", fileName, len(catScripts))
	}

	// Write index.json
	indexData, _ := json.MarshalIndent(map[string]interface{}{
		"files": fileNames,
		"total": len(allCompact),
	}, "", "  ")
	os.WriteFile(filepath.Join(outDir, "index.json"), indexData, 0o644)

	// Write legacy single file
	legacyData, _ := json.MarshalIndent(allCompact, "", "  ")
	os.WriteFile("builtin-scripts.json", legacyData, 0o644)

	fmt.Fprintf(os.Stderr, "\nExported %d scripts into %d categories + legacy file\n", len(allCompact), len(categories))
}
