package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP47RecorderInjectedScriptCarriesExplicitLLMConfigInAIRequests(t *testing.T) {
	scriptBytes, err := os.ReadFile(filepath.Join("scripts", "recorder.js"))
	if err != nil {
		t.Fatalf("read production recorder.js: %v", err)
	}
	assignments := p47AIExtractionRequestAssignments(string(scriptBytes))
	if len(assignments) == 0 {
		t.Fatal("production recorder.js does not assign window.__aiExtractionRequest__; recording-page AI requests must be observable")
	}
	for i, assignment := range assignments {
		if !p47HasLLMConfigIDProperty(assignment) {
			t.Fatalf("window.__aiExtractionRequest__ assignment %d does not carry selected llm_config_id:\n%s", i+1, assignment)
		}
	}
}

func p47AIExtractionRequestAssignments(script string) []string {
	const marker = "window.__aiExtractionRequest__ = {"
	var assignments []string
	for offset := 0; ; {
		start := strings.Index(script[offset:], marker)
		if start < 0 {
			return assignments
		}
		start += offset
		end := strings.Index(script[start:], "};")
		if end < 0 {
			assignments = append(assignments, script[start:])
			return assignments
		}
		end += start + len("};")
		assignments = append(assignments, script[start:end])
		offset = end
	}
}

func p47HasLLMConfigIDProperty(assignment string) bool {
	compact := strings.ReplaceAll(assignment, " ", "")
	compact = strings.ReplaceAll(compact, "\t", "")
	compact = strings.ReplaceAll(compact, "\r", "")
	compact = strings.ReplaceAll(compact, "\n", "")
	return strings.Contains(compact, "llm_config_id:") ||
		strings.Contains(compact, `"llm_config_id":`) ||
		strings.Contains(compact, `'llm_config_id':`)
}
