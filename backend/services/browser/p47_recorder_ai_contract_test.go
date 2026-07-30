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

func TestP476RecorderRedactsSensitiveInputsBeforeRecordingQueue(t *testing.T) {
	scriptBytes, err := os.ReadFile(filepath.Join("scripts", "recorder.js"))
	if err != nil {
		t.Fatalf("read production recorder.js: %v", err)
	}
	script := string(scriptBytes)
	for _, required := range []string{
		"SensitiveInputPolicyV1",
		"sensitive_input",
		"currentpassword",
		"onetimecode",
		"验证码",
		"{{REDACTED_SECRET}}",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("recorder.js is missing sensitive-input contract marker %q", required)
		}
	}
	if !strings.Contains(script, "getAccessibleName(element, !sensitiveInput)") || !strings.Contains(script, "inferObject(element, !sensitiveInput)") {
		t.Fatal("sensitive inputs must not use live element.value as an accessible-name or intent fallback")
	}
	redaction := strings.Index(script, "action = redactSensitiveInputAction(action, element);")
	queueOffset := -1
	if redaction >= 0 {
		queueOffset = strings.Index(script[redaction:], "window.__recordedActions__.push(action);")
	}
	if redaction < 0 || queueOffset < 0 {
		t.Fatalf("sensitive input must be redacted before queueing actions: redaction=%d queueOffset=%d", redaction, queueOffset)
	}
	queue := redaction + queueOffset
	if redaction > queue {
		t.Fatalf("sensitive input must be redacted before queueing actions: redaction=%d queue=%d", redaction, queue)
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
