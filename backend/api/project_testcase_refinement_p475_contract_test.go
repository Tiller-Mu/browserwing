package api

import (
	"net/http"
	"testing"
)

/*
P4.7.5 contract basis:
- docs/P4_7_5_PLAYBOT_BLUEPRINT_QUALITY_DESIGN.md sections 5, 7, 12.2, and 14
  require optimize to call the independent Go agent, create only a proposed
  LLMRefinement, and reject invalid proposed Blueprints without changing the
  active TestCase.
- docs/P4_7_5_PLAYBOT_GO_AGENT_IMPLEMENTATION_PLAN.md section 4.5 assigns these
  optimize red tests to backend/api.

Current expected red state:
- refinement still calls services/playbot Python CLI and Handler lacks the Go
  agent injection seam required by P4.7.5.

Targeted verification:
- cd backend
- go test ./api -run TestP475Optimize -count=1
*/

func TestP475OptimizeUsesGoAgentAndCreatesOnlyProposedRefinement(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:       "active case before optimize",
		Description: "original description",
		Status:      "active",
		Blueprint:   p475ValidGeneratedBlueprint("active case before optimize"),
	})
	before := env.snapshotTestCase(t, testCase.ID)
	agent := newP475FakePlaybotAgent(t, p475AgentOptimizeSuccess(p475ValidGeneratedBlueprint("optimized proposed case")))
	env.installP475FakePlaybotAgent(t, agent)

	res := env.postRefineTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
		"prompt": "strengthen the save assertion",
	})

	env.requireStatus(t, res, http.StatusOK)
	if agent.CallCount() != 1 {
		t.Fatalf("Go Playbot agent calls = %d, want 1", agent.CallCount())
	}
	env.requirePlaybotCalls(t, 0)
	requireP475JSONContains(t, agent.LastJobJSON(t), `"mode":"optimize"`)
	env.requireRefinementCount(t, testCase.ID, 1)
	env.requireTestCaseUnchanged(t, before, "optimize must create only proposed LLMRefinement without mutating active TestCase")
}

func TestP475OptimizeRejectsInvalidProposedBlueprintWithoutChangingActiveCase(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:       "active case before invalid optimize",
		Description: "original description",
		Status:      "active",
		Blueprint:   p475ValidGeneratedBlueprint("active case before invalid optimize"),
	})
	before := env.snapshotTestCase(t, testCase.ID)
	invalid := p475GeneratedBlueprint("invalid proposed blueprint", []map[string]any{{
		"action": "navigate",
		"value":  "/orders",
	}})
	agent := newP475FakePlaybotAgent(t, p475AgentOptimizeSuccess(invalid))
	env.installP475FakePlaybotAgent(t, agent)

	res := env.postRefineTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
		"prompt": "propose invalid navigate value-only blueprint",
	})

	env.requireStatus(t, res, http.StatusBadRequest)
	env.requireRefinementCount(t, testCase.ID, 0)
	env.requireTestCaseUnchanged(t, before, "invalid proposed Blueprint must not change active TestCase")
}

func p475AgentOptimizeSuccess(refined map[string]any) map[string]any {
	return map[string]any{
		"schema_version":     "p4.7.5",
		"status":             "success",
		"code":               "",
		"refined_blueprint":  refined,
		"summary":            "proposed optimization only",
		"risk_notes":         "review before applying",
		"context_trace":      p475ContextTrace(),
		"asset_mutations":    []any{},
		"active_case_change": false,
	}
}
