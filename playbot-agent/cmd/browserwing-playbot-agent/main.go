package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/browserwing/browserwing/playbot-agent/internal/compiler"
	"github.com/browserwing/browserwing/playbot-agent/internal/protocol"
	"github.com/browserwing/browserwing/playbot-agent/internal/quality"
)

func main() {
	mode := flag.String("mode", "", "playbot mode")
	input := flag.String("input", "", "path to PlaybotJob JSON")
	flag.Parse()

	result := run(*mode, *input)
	data, err := json.Marshal(result)
	if err != nil {
		fallback := protocol.PlaybotResult{SchemaVersion: protocol.SchemaVersion, Status: protocol.StatusFailed, Code: "playbot_result_marshal_failed"}
		data, _ = json.Marshal(fallback)
	}
	fmt.Fprint(os.Stdout, string(data))
	fmt.Fprintf(os.Stderr, "browserwing-playbot-agent request=%s status=%s code=%s\n", redacted(result.ContextTrace.SourcePageScriptID), result.Status, result.Code)
}

func run(mode string, inputPath string) protocol.PlaybotResult {
	if strings.TrimSpace(inputPath) == "" {
		return failed("playbot_job_input_required")
	}
	raw, err := os.ReadFile(inputPath)
	if err != nil {
		return failed("playbot_job_read_failed")
	}
	job, err := protocol.DecodeAndValidatePlaybotJob(raw)
	if err != nil {
		return failed(err.Error())
	}
	requestedMode := normalizeMode(mode)
	job.Mode = normalizeMode(job.Mode)
	if requestedMode != "" && job.Mode != "" && requestedMode != job.Mode {
		return failed("playbot_job_mode_mismatch")
	}
	if job.Mode == "" {
		job.Mode = requestedMode
	}

	trace := protocol.ContextTrace{
		SourcePageScriptID:       job.RecordingSource.SourcePageScriptID,
		SourceRecordingSessionID: job.RecordingSource.SourceRecordingSessionID,
		SourceHash:               "sha256:local-agent",
		UsedFields:               []string{"action_trace", "dom_snapshot", "recording_meta"},
		Truncated:                []string{},
	}
	if job.Mode == protocol.ModeGenerate {
		diagnostics := quality.ValidateRecordingQuality(toQualityInput(job))
		if len(diagnostics.Items) > 0 {
			errors := make([]map[string]any, 0, len(diagnostics.Items))
			for _, item := range diagnostics.Items {
				errors = append(errors, map[string]any{"code": item.Code, "message": item.Message, "retryable": item.Retryable})
			}
			return protocol.PlaybotResult{
				SchemaVersion: protocol.SchemaVersion,
				Status:        protocol.StatusFailed,
				Code:          diagnostics.FirstCode(),
				QualityErrors: errors,
				ContextTrace:  trace,
			}
		}
	}
	switch job.Mode {
	case protocol.ModeGenerate:
		blueprint, err := compileRecordedJob(job)
		if err != nil {
			return protocol.PlaybotResult{
				SchemaVersion: protocol.SchemaVersion,
				Status:        protocol.StatusFailed,
				Code:          err.Error(),
				ContextTrace:  trace,
			}
		}
		return protocol.PlaybotResult{
			SchemaVersion: protocol.SchemaVersion,
			Status:        protocol.StatusSuccess,
			TestCases:     []map[string]any{blueprint},
			ContextTrace:  trace,
		}
	case protocol.ModeOptimize:
		refined, summary, riskNotes, err := optimizeBlueprint(job)
		if err != nil {
			return protocol.PlaybotResult{
				SchemaVersion: protocol.SchemaVersion,
				Status:        protocol.StatusFailed,
				Code:          err.Error(),
				ContextTrace:  trace,
			}
		}
		return protocol.PlaybotResult{
			SchemaVersion:    protocol.SchemaVersion,
			Status:           protocol.StatusSuccess,
			RefinedBlueprint: refined,
			Summary:          summary,
			RiskNotes:        riskNotes,
			ContextTrace:     trace,
		}
	case protocol.ModeRepairProposal:
		if !hasRepairProposalFacts(job) {
			return protocol.PlaybotResult{
				SchemaVersion: protocol.SchemaVersion,
				Status:        protocol.StatusFailed,
				Code:          "recording_fact_required",
				ContextTrace:  trace,
			}
		}
		return protocol.PlaybotResult{
			SchemaVersion:  protocol.SchemaVersion,
			Status:         protocol.StatusSuccess,
			RepairProposal: map[string]any{"summary": "draft repair proposal", "source": job.RecordingSource.SourcePageScriptID},
			ContextTrace:   trace,
		}
	}
	return protocol.PlaybotResult{
		SchemaVersion: protocol.SchemaVersion,
		Status:        protocol.StatusFailed,
		Code:          "playbot_agent_mode_not_implemented",
		ContextTrace:  trace,
	}
}

func failed(code string) protocol.PlaybotResult {
	return protocol.PlaybotResult{SchemaVersion: protocol.SchemaVersion, Status: protocol.StatusFailed, Code: code}
}

func normalizeMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "repair-proposal":
		return protocol.ModeRepairProposal
	default:
		return strings.TrimSpace(mode)
	}
}

func hasRepairProposalFacts(job protocol.PlaybotJob) bool {
	return len(job.RecordingSource.ActionTrace) > 0
}

func compileRecordedJob(job protocol.PlaybotJob) (map[string]any, error) {
	title := strings.TrimSpace(job.UserInstruction)
	if title == "" {
		title = firstNonEmpty(job.PageContext.Description, "Generated Playbot case")
	}
	plan := compiler.SemanticPlan{
		Title:       title,
		Description: "Generated from recorded Playbot flow",
		Steps:       make([]compiler.SemanticStep, 0, len(job.RecordingSource.ActionTrace)),
	}
	for _, action := range job.RecordingSource.ActionTrace {
		plan.Steps = append(plan.Steps, compiler.SemanticStep{
			Action: finalActionFromRecorded(action.Type),
			Value:  action.Value,
			URL:    action.URL,
			TargetHint: compiler.TargetHint{
				Role:             action.Target.Role,
				Text:             action.Target.Text,
				Placeholder:      action.Target.Placeholder,
				Label:            action.Target.Label,
				Selector:         action.Target.Selector,
				RecordedSelector: action.Target.RecordedSelector,
				RefID:            firstNonEmpty(action.Target.RefID, action.RefID),
			},
			IntentReason: action.IntentReason,
		})
	}
	blueprint, err := compiler.CompileBlueprint(plan, compiler.CompileContext{BaseURL: job.PageContext.URL})
	if err != nil {
		return nil, err
	}
	blueprint.AuthContext = job.RecordingSource.RecordingMeta.AuthContext
	return mapFromJSON(blueprint)
}

func finalActionFromRecorded(actionType string) string {
	switch strings.TrimSpace(actionType) {
	case "input":
		return "fill"
	default:
		return strings.TrimSpace(actionType)
	}
}

func optimizeBlueprint(job protocol.PlaybotJob) (map[string]any, string, string, error) {
	if len(job.ExistingBlueprint) == 0 {
		return nil, "", "", fmt.Errorf("playbot_existing_blueprint_required")
	}
	instruction := strings.TrimSpace(job.UserInstruction)
	hasExecutionContext := len(job.ExecutionReport) > 0
	hasRecordingFacts := len(job.RecordingSource.ActionTrace) > 0 || len(job.RecordingSource.DOMSnapshot) > 0
	if instruction == "" && !hasExecutionContext && !hasRecordingFacts {
		return nil, "", "", fmt.Errorf("playbot_optimize_context_required")
	}
	refined := cloneMap(job.ExistingBlueprint)
	description := strings.TrimSpace(stringFromAny(refined["description"]))
	additions := make([]string, 0, 3)
	if instruction != "" {
		additions = append(additions, "Optimization request: "+instruction)
	}
	if status := strings.TrimSpace(stringFromAny(job.ExecutionReport["status"])); status != "" {
		additions = append(additions, "Execution context: "+status)
	} else if hasExecutionContext {
		additions = append(additions, "Execution context: provided")
	}
	if hasRecordingFacts {
		additions = append(additions, fmt.Sprintf("Recording facts considered: %d action(s)", len(job.RecordingSource.ActionTrace)))
	}
	newDescription := strings.TrimSpace(strings.Join(append([]string{description}, additions...), "\n"))
	if newDescription == description {
		return nil, "", "", fmt.Errorf("playbot_optimize_no_change")
	}
	refined["description"] = newDescription
	if strings.TrimSpace(stringFromAny(refined["title"])) == "" {
		refined["title"] = "Playbot optimized proposal"
	}
	return refined, "Playbot proposed optimization from the supplied prompt and context.", "Review generated proposal before applying.", nil
}

func mapFromJSON(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func cloneMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func toQualityInput(job protocol.PlaybotJob) quality.RecordingQualityInput {
	actions := make([]quality.RecordedAction, 0, len(job.RecordingSource.ActionTrace))
	for _, action := range job.RecordingSource.ActionTrace {
		actions = append(actions, quality.RecordedAction{
			Type:  action.Type,
			Value: action.Value,
			URL:   action.URL,
			Target: quality.RecordedTarget{
				Role:             action.Target.Role,
				Text:             action.Target.Text,
				Placeholder:      action.Target.Placeholder,
				Label:            action.Target.Label,
				Selector:         action.Target.Selector,
				RecordedSelector: action.Target.RecordedSelector,
				RefID:            firstNonEmpty(action.Target.RefID, action.RefID),
			},
		})
	}
	return quality.RecordingQualityInput{
		Actions:  actions,
		Snapshot: toQualitySnapshot(job.RecordingSource.DOMSnapshot),
		Meta: quality.RecordingMeta{
			SchemaVersion: job.RecordingSource.RecordingMeta.SchemaVersion,
			RecordingKind: job.RecordingSource.RecordingMeta.RecordingKind,
			AuthContext:   job.RecordingSource.RecordingMeta.AuthContext,
			TargetURL:     job.RecordingSource.RecordingMeta.TargetURL,
		},
	}
}

func toQualitySnapshot(raw map[string]any) quality.DOMSnapshot {
	elementsRaw, _ := raw["elements"].([]any)
	elements := make([]quality.DOMElement, 0, len(elementsRaw))
	for _, item := range elementsRaw {
		obj, _ := item.(map[string]any)
		if obj == nil {
			continue
		}
		elements = append(elements, quality.DOMElement{
			Role:             stringFromAny(obj["role"]),
			Text:             stringFromAny(obj["text"]),
			Placeholder:      stringFromAny(obj["placeholder"]),
			RecordedSelector: stringFromAny(obj["recorded_selector"]),
			RefID:            stringFromAny(obj["ref_id"]),
		})
	}
	return quality.DOMSnapshot{Elements: elements}
}

func stringFromAny(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func redacted(text string) string {
	if strings.Contains(text, `C:\Users\`) || strings.Contains(text, "sk-") {
		return "<redacted>"
	}
	return text
}
