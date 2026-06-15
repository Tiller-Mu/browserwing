package testcase_executor

import (
	"os"
	"strings"
	"testing"
)

/*
P4.7.5 contract basis:
- docs/P4_7_5_PLAYBOT_BLUEPRINT_QUALITY_DESIGN.md sections 5.3, 12.3, and 15
  keep formal execution on BrowserWing Blueprint via Go runner and explicitly
  exclude native Playwright spec execution in P4.7.5.
- docs/P4_7_5_PLAYBOT_GO_AGENT_IMPLEMENTATION_PLAN.md section 4.6 assigns the
  runner boundary check to backend/services/testcase_executor.

Current expected red state:
- this is mainly a boundary regression guard and may already pass; the P4.7.5
  red suite as a whole remains red until the new Go agent and backend adapter
  are implemented.

Targeted verification:
- cd backend
- go test ./services/testcase_executor -run TestP475 -count=1
*/

func TestP475NoPlaywrightSpecRunnerIsIntroduced(t *testing.T) {
	data, err := os.ReadFile("runner.go")
	if err != nil {
		t.Fatalf("read runner.go: %v", err)
	}
	text := strings.ToLower(string(data))
	for _, forbidden := range []string{"playwright", ".spec.ts", ".spec.py", "pytest"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("P4.7.5 formal execution must not introduce Playwright spec runner token %q in runner.go", forbidden)
		}
	}
}
