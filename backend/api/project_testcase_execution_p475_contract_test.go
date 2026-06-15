package api

import (
	"net/http"
	"testing"
)

/*
P4.7.5 contract basis:
- docs/P4_7_5_PLAYBOT_BLUEPRINT_QUALITY_DESIGN.md sections 5.3, 12.3, and 15
  require RunTestCase to remain on the Go testcase_executor.Runner path. The
  independent Go agent and Python execution_engine must not participate in
  formal execution, and P4.7.5 must not introduce a Playwright spec runner.
- docs/P4_7_5_PLAYBOT_GO_AGENT_IMPLEMENTATION_PLAN.md section 4.6 assigns these
  execution-boundary red tests to backend/api.

Current expected red state:
- Handler has no Go agent test seam yet, so the test first fails while demanding
  the boundary be injectable and observable; after the seam exists, the contract
  verifies RunTestCase still uses only the Go runner.

Targeted verification:
- cd backend
- go test ./api -run TestP475RunTestCase -count=1
*/

func TestP475RunTestCaseUsesGoRunnerOnly(t *testing.T) {
	env := newGenerateContractEnv(t)
	runner := newContractFakeTestCaseRunner(t, contractFakePassedResult())
	env.installFakeTestCaseRunner(t, runner)
	agent := newP475FakePlaybotAgent(t, p475AgentGenerateSuccess(p475ValidGeneratedBlueprint("must not be used by run")))
	env.installP475FakePlaybotAgent(t, agent)
	project, version, page := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:     "run uses Go runner only",
		Status:    "active",
		Blueprint: p475ValidGeneratedBlueprint("run uses Go runner only"),
	})

	res := env.runTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{})

	env.requireStatus(t, res, http.StatusOK)
	if runner.CallCount() != 1 {
		t.Fatalf("Go testcase runner calls = %d, want 1", runner.CallCount())
	}
	if agent.CallCount() != 0 {
		t.Fatalf("Go Playbot agent calls during RunTestCase = %d, want 0", agent.CallCount())
	}
	env.requirePlaybotCalls(t, 0)
	env.requireTestExecutionCount(t, testCase.ID, 1)
}
