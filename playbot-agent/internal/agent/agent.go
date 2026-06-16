package agent

import (
	"context"
	"fmt"
	"strings"
)

type PlaybotJob struct {
	SchemaVersion     string
	Mode              string
	RequestID         string
	ExistingBlueprint map[string]any
	ExecutionReport   map[string]any
	UserInstruction   string
	RecordingSource   RecordingSource
}

type RecordingSource struct {
	SourcePageScriptID string
	ActionTrace        []map[string]any
	DOMSnapshot        map[string]any
	RecordingMeta      map[string]any
}

type PlaybotResult struct {
	SchemaVersion            string         `json:"schema_version"`
	Status                   string         `json:"status"`
	Code                     string         `json:"code,omitempty"`
	RefinedBlueprint         map[string]any `json:"refined_blueprint,omitempty"`
	RepairProposal           map[string]any `json:"repair_proposal,omitempty"`
	ActiveTestCaseChanged    bool           `json:"active_case_change,omitempty"`
	PageScriptChanged        bool           `json:"page_script_changed,omitempty"`
	RecordingSessionChanged  bool           `json:"recording_session_changed,omitempty"`
	RecordingArtifactChanged bool           `json:"recording_artifact_changed,omitempty"`
	AssetMutations           []any          `json:"asset_mutations,omitempty"`
}

func Run(_ context.Context, job PlaybotJob) (PlaybotResult, error) {
	switch job.Mode {
	case "optimize":
		refined, err := optimizeBlueprint(job)
		if err != nil {
			return PlaybotResult{SchemaVersion: "p4.7.5", Status: "failed", Code: err.Error()}, nil
		}
		return PlaybotResult{
			SchemaVersion:    "p4.7.5",
			Status:           "success",
			RefinedBlueprint: refined,
			AssetMutations:   []any{},
		}, nil
	case "repair_proposal":
		if len(job.RecordingSource.ActionTrace) == 0 {
			return PlaybotResult{}, fmt.Errorf("recording_fact_required")
		}
		return PlaybotResult{
			SchemaVersion:  "p4.7.5",
			Status:         "success",
			RepairProposal: map[string]any{"summary": "draft repair proposal", "source": job.RecordingSource.SourcePageScriptID},
			AssetMutations: []any{},
		}, nil
	default:
		return PlaybotResult{SchemaVersion: "p4.7.5", Status: "failed", Code: "playbot_agent_mode_not_implemented"}, nil
	}
}

func optimizeBlueprint(job PlaybotJob) (map[string]any, error) {
	if len(job.ExistingBlueprint) == 0 {
		return nil, fmt.Errorf("playbot_existing_blueprint_required")
	}
	instruction := strings.TrimSpace(job.UserInstruction)
	hasExecutionContext := len(job.ExecutionReport) > 0
	hasRecordingFacts := len(job.RecordingSource.ActionTrace) > 0 || len(job.RecordingSource.DOMSnapshot) > 0
	if instruction == "" && !hasExecutionContext && !hasRecordingFacts {
		return nil, fmt.Errorf("playbot_optimize_context_required")
	}
	refined := cloneMap(job.ExistingBlueprint)
	description, _ := refined["description"].(string)
	additions := make([]string, 0, 3)
	if instruction != "" {
		additions = append(additions, "Optimization request: "+instruction)
	}
	if status, _ := job.ExecutionReport["status"].(string); strings.TrimSpace(status) != "" {
		additions = append(additions, "Execution context: "+strings.TrimSpace(status))
	} else if hasExecutionContext {
		additions = append(additions, "Execution context: provided")
	}
	if hasRecordingFacts {
		additions = append(additions, fmt.Sprintf("Recording facts considered: %d action(s)", len(job.RecordingSource.ActionTrace)))
	}
	newDescription := strings.TrimSpace(strings.Join(append([]string{strings.TrimSpace(description)}, additions...), "\n"))
	if newDescription == strings.TrimSpace(description) {
		return nil, fmt.Errorf("playbot_optimize_no_change")
	}
	refined["description"] = newDescription
	return refined, nil
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
