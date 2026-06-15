package agent

import (
	"context"
	"strings"
	"testing"
)

/*
P4.7.5 contract basis:
- docs/P4_7_5_PLAYBOT_BLUEPRINT_QUALITY_DESIGN.md section 12 splits generate,
  optimize, execute, and repair_proposal. optimize may only produce a proposed
  refinement; repair_proposal may only return a draft and must not apply assets.
- docs/P4_7_5_PLAYBOT_GO_AGENT_IMPLEMENTATION_PLAN.md section 4.5 assigns these
  mode-boundary tests to playbot-agent/internal/agent.

Current expected red state:
- playbot-agent/internal/agent has no production orchestrator yet, so these tests
  fail with missing symbols.

Targeted verification:
- cd ..\playbot-agent
- go test ./internal/agent -run TestP475 -count=1
*/

func TestP475OptimizeCreatesOnlyProposedRefinement(t *testing.T) {
	result, err := Run(context.Background(), PlaybotJob{
		SchemaVersion: "p4.7.5",
		Mode:          "optimize",
		RequestID:     "optimize-contract",
		ExistingBlueprint: map[string]any{
			"title":       "existing active case",
			"description": "old description",
			"steps": []map[string]any{{
				"action": "click",
				"target": map[string]any{"text": "Save"},
			}},
		},
		UserInstruction: "make the save assertion stronger",
		RecordingSource: RecordingSource{
			SourcePageScriptID: "ps_123",
			ActionTrace: []map[string]any{{
				"type": "click",
				"target": map[string]any{
					"text": "Save",
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Run optimize returned error: %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("status = %q, want success: %+v", result.Status, result)
	}
	if result.RefinedBlueprint == nil {
		t.Fatalf("optimize result missing proposed refined_blueprint: %+v", result)
	}
	if result.ActiveTestCaseChanged || len(result.AssetMutations) != 0 {
		t.Fatalf("optimize must not directly mutate active assets: %+v", result)
	}
}

func TestP475RepairProposalDoesNotApplyAssetChanges(t *testing.T) {
	result, err := Run(context.Background(), PlaybotJob{
		SchemaVersion: "p4.7.5",
		Mode:          "repair_proposal",
		RequestID:     "repair-contract",
		ExistingBlueprint: map[string]any{
			"title": "failing case",
			"steps": []map[string]any{{
				"action": "click",
				"target": map[string]any{"text": "Submit"},
			}},
		},
		ExecutionReport: map[string]any{
			"status": "failed",
			"steps": []map[string]any{{
				"index": 0,
				"error": "element not found",
			}},
		},
		RecordingSource: RecordingSource{
			SourcePageScriptID: "ps_123",
			ActionTrace: []map[string]any{{
				"type": "click",
				"target": map[string]any{
					"text": "Submit",
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Run repair_proposal returned error: %v", err)
	}
	if result.RepairProposal == nil {
		t.Fatalf("repair_proposal result missing draft proposal: %+v", result)
	}
	if result.ActiveTestCaseChanged || result.PageScriptChanged || result.RecordingSessionChanged || result.RecordingArtifactChanged || len(result.AssetMutations) != 0 {
		t.Fatalf("repair_proposal must not apply asset changes: %+v", result)
	}
}

func TestP475RepairProposalCannotInventStepsWithoutRecordedFacts(t *testing.T) {
	result, err := Run(context.Background(), PlaybotJob{
		SchemaVersion:     "p4.7.5",
		Mode:              "repair_proposal",
		RequestID:         "repair-no-facts",
		ExistingBlueprint: map[string]any{"title": "no facts", "steps": []map[string]any{{"action": "click", "target": map[string]any{"text": "Pay"}}}},
		ExecutionReport:   map[string]any{"status": "failed"},
	})
	if err == nil {
		t.Fatalf("repair_proposal without recording facts returned nil error and result %+v", result)
	}
	if !strings.Contains(err.Error(), "recording_fact_required") {
		t.Fatalf("repair_proposal no-facts error = %v, want recording_fact_required", err)
	}
}
