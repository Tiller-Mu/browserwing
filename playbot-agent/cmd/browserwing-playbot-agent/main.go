package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/browserwing/browserwing/playbot-agent/internal/compiler"
	"github.com/browserwing/browserwing/playbot-agent/internal/llmclient"
	"github.com/browserwing/browserwing/playbot-agent/internal/protocol"
	"github.com/browserwing/browserwing/playbot-agent/internal/quality"
)

func main() {
	mode := flag.String("mode", "", "playbot mode")
	input := flag.String("input", "", "path to PlaybotJob JSON")
	events := flag.String("events", "", "path to PlaybotEvent JSONL side-channel")
	flag.Parse()

	writer := newEventWriter(*events)
	defer writer.Close()
	result := run(context.Background(), *mode, *input, writer)
	data, err := json.Marshal(result)
	if err != nil {
		fallback := protocol.PlaybotResult{SchemaVersion: protocol.SchemaVersion, Status: protocol.StatusFailed, Code: "playbot_result_marshal_failed"}
		data, _ = json.Marshal(fallback)
	}
	fmt.Fprint(os.Stdout, string(data))
	fmt.Fprintf(os.Stderr, "browserwing-playbot-agent request=%s status=%s code=%s\n", redacted(result.ContextTrace.SourcePageScriptID), result.Status, result.Code)
}

func run(ctx context.Context, mode string, inputPath string, events *eventWriter) protocol.PlaybotResult {
	events.Emit(protocol.PlaybotEvent{
		Phase:   "job_read",
		Level:   "info",
		Message: "Reading Playbot job.",
	})
	if strings.TrimSpace(inputPath) == "" {
		events.Emit(protocol.PlaybotEvent{Phase: "failed", Level: "error", Message: "Playbot job input is required."})
		return failed("playbot_job_input_required")
	}
	raw, err := os.ReadFile(inputPath)
	if err != nil {
		events.Emit(protocol.PlaybotEvent{Phase: "failed", Level: "error", Message: "Playbot job could not be read."})
		return failed("playbot_job_read_failed")
	}
	job, err := protocol.DecodeAndValidatePlaybotJob(raw)
	if err != nil {
		events.Emit(protocol.PlaybotEvent{Phase: "failed", Level: "error", Message: "Playbot job contract validation failed."})
		return failed(err.Error())
	}
	events.SetRequestID(job.RequestID)
	requestedMode := normalizeMode(mode)
	job.Mode = normalizeMode(job.Mode)
	if requestedMode != "" && job.Mode != "" && requestedMode != job.Mode {
		events.Emit(protocol.PlaybotEvent{Phase: "failed", Level: "error", Message: "Playbot job mode does not match CLI mode."})
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
		events.Emit(protocol.PlaybotEvent{
			Phase:   "recording_quality",
			Level:   "info",
			Message: "Validating recorded flow quality.",
			Data:    map[string]any{"action_count": len(job.RecordingSource.ActionTrace)},
		})
		diagnostics := quality.ValidateRecordingQuality(toQualityInput(job))
		if len(diagnostics.Items) > 0 {
			errors := make([]map[string]any, 0, len(diagnostics.Items))
			for _, item := range diagnostics.Items {
				errors = append(errors, map[string]any{"code": item.Code, "message": item.Message, "retryable": item.Retryable})
			}
			events.Emit(protocol.PlaybotEvent{
				Phase:   "failed",
				Level:   "error",
				Message: "Recording quality is insufficient.",
				Data:    map[string]any{"code": diagnostics.FirstCode()},
			})
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
		if _, err := compileRecordedJob(job); err != nil {
			events.Emit(protocol.PlaybotEvent{Phase: "failed", Level: "error", Message: "Recorded Blueprint facts failed validation.", Data: map[string]any{"code": err.Error()}})
			return protocol.PlaybotResult{
				SchemaVersion: protocol.SchemaVersion,
				Status:        protocol.StatusFailed,
				Code:          err.Error(),
				ContextTrace:  trace,
			}
		}
		modelOutput, semanticPlan, modelErr := maybeGenerateVisibleModelOutput(ctx, job, events)
		if modelErr != nil {
			return protocol.PlaybotResult{
				SchemaVersion: protocol.SchemaVersion,
				Status:        protocol.StatusFailed,
				Code:          modelErr.Error(),
				ContextTrace:  trace,
			}
		}
		events.Emit(protocol.PlaybotEvent{
			Phase:   "compile_blueprint",
			Level:   "info",
			Message: "Compiling executable Blueprint from validated semantic plan.",
		})
		blueprint, err := compileGenerateBlueprint(job, semanticPlan)
		if err != nil {
			events.Emit(protocol.PlaybotEvent{Phase: "failed", Level: "error", Message: "Blueprint compilation failed.", Data: map[string]any{"code": err.Error()}})
			return protocol.PlaybotResult{
				SchemaVersion: protocol.SchemaVersion,
				Status:        protocol.StatusFailed,
				Code:          err.Error(),
				ContextTrace:  trace,
			}
		}
		return protocol.PlaybotResult{
			SchemaVersion:  protocol.SchemaVersion,
			Status:         protocol.StatusSuccess,
			TestCases:      []map[string]any{blueprint},
			ContextTrace:   trace,
			VisibleSummary: visibleSummaryFromModelOutput(modelOutput, "Recorded flow was converted into one executable test case."),
			ModelOutput:    modelOutput,
		}
	case protocol.ModeOptimize:
		if err := validateOptimizePrerequisites(job); err != nil {
			events.Emit(protocol.PlaybotEvent{Phase: "failed", Level: "error", Message: "Optimization proposal failed.", Data: map[string]any{"code": err.Error()}})
			return protocol.PlaybotResult{
				SchemaVersion: protocol.SchemaVersion,
				Status:        protocol.StatusFailed,
				Code:          err.Error(),
				ContextTrace:  trace,
			}
		}
		events.Emit(protocol.PlaybotEvent{
			Phase:   "failed",
			Level:   "error",
			Message: "Optimization proposals are not implemented by the Go agent yet.",
			Data:    map[string]any{"code": "playbot_optimize_not_implemented"},
		})
		return protocol.PlaybotResult{
			SchemaVersion: protocol.SchemaVersion,
			Status:        protocol.StatusFailed,
			Code:          "playbot_optimize_not_implemented",
			ContextTrace:  trace,
		}
	case protocol.ModeRepairProposal:
		if !hasRepairProposalFacts(job) {
			events.Emit(protocol.PlaybotEvent{Phase: "failed", Level: "error", Message: "Repair proposal requires recorded facts."})
			return protocol.PlaybotResult{
				SchemaVersion: protocol.SchemaVersion,
				Status:        protocol.StatusFailed,
				Code:          "recording_fact_required",
				ContextTrace:  trace,
			}
		}
		modelOutput, _, modelErr := maybeGenerateVisibleModelOutput(ctx, job, events)
		if modelErr != nil {
			return protocol.PlaybotResult{
				SchemaVersion: protocol.SchemaVersion,
				Status:        protocol.StatusFailed,
				Code:          modelErr.Error(),
				ContextTrace:  trace,
			}
		}
		return protocol.PlaybotResult{
			SchemaVersion:  protocol.SchemaVersion,
			Status:         protocol.StatusSuccess,
			RepairProposal: map[string]any{"summary": "draft repair proposal", "source": job.RecordingSource.SourcePageScriptID},
			ContextTrace:   trace,
			VisibleSummary: visibleSummaryFromModelOutput(modelOutput, "Draft repair proposal is ready for review."),
			ModelOutput:    modelOutput,
		}
	}
	events.Emit(protocol.PlaybotEvent{Phase: "failed", Level: "error", Message: "Playbot mode is not implemented."})
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

type eventWriter struct {
	file      *os.File
	writer    *bufio.Writer
	seq       int64
	requestID string
}

func newEventWriter(path string) *eventWriter {
	if strings.TrimSpace(path) == "" {
		return &eventWriter{}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return &eventWriter{}
	}
	return &eventWriter{file: file, writer: bufio.NewWriter(file)}
}

func (w *eventWriter) SetRequestID(requestID string) {
	w.requestID = strings.TrimSpace(requestID)
}

func (w *eventWriter) Emit(event protocol.PlaybotEvent) {
	if w == nil || w.writer == nil {
		return
	}
	w.seq++
	event.SchemaVersion = protocol.SchemaVersion
	event.Seq = w.seq
	if strings.TrimSpace(event.RequestID) == "" {
		event.RequestID = w.requestID
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = w.writer.Write(data)
	_ = w.writer.WriteByte('\n')
	_ = w.writer.Flush()
}

func (w *eventWriter) Close() {
	if w == nil {
		return
	}
	if w.writer != nil {
		_ = w.writer.Flush()
	}
	if w.file != nil {
		_ = w.file.Close()
	}
}

func maybeGenerateVisibleModelOutput(ctx context.Context, job protocol.PlaybotJob, events *eventWriter) (map[string]any, json.RawMessage, error) {
	if !boolLimit(job.Limits, "enable_llm") {
		output := localVisibleModelOutput(job)
		emitModelOutput(events, output)
		return output, nil, nil
	}
	events.Emit(protocol.PlaybotEvent{
		Phase:   "llm_request",
		Level:   "info",
		Message: "Requesting visible model output.",
		Data:    map[string]any{"model": job.LLMRuntimeConfig.Model, "provider": job.LLMRuntimeConfig.Provider},
	})
	timeout := time.Duration(job.LLMRuntimeConfig.TimeoutMs) * time.Millisecond
	req := llmclient.Request{
		Endpoint: job.LLMRuntimeConfig.Endpoint,
		Model:    job.LLMRuntimeConfig.Model,
		APIKey:   llmclient.ResolveAPIKey(job.LLMRuntimeConfig.SecretChannel.Name),
		Timeout:  timeout,
		System:   playbotVisibleOutputSystemPrompt(),
		User:     playbotVisibleOutputUserPrompt(job),
	}
	response, err := llmclient.NewOpenAICompatibleClient().GenerateVisibleOutput(ctx, req)
	if err != nil {
		events.Emit(protocol.PlaybotEvent{Phase: "failed", Level: "error", Message: "The model did not return usable visible output.", Data: map[string]any{"code": err.Error()}})
		return nil, nil, err
	}
	output := responseToModelOutput(response)
	emitModelOutput(events, output)
	return output, response.SemanticPlan, nil
}

func emitModelOutput(events *eventWriter, output map[string]any) {
	if len(output) == 0 {
		return
	}
	data := map[string]any{}
	if steps, ok := output["candidate_steps"]; ok {
		data["candidate_steps"] = steps
	}
	if assumptions, ok := output["assumptions"]; ok {
		data["assumptions"] = assumptions
	}
	if riskNotes, ok := output["risk_notes"]; ok {
		data["risk_notes"] = riskNotes
	}
	events.Emit(protocol.PlaybotEvent{
		Phase:          "llm_visible_output",
		Level:          "info",
		Message:        "Model visible output is available.",
		VisibleMessage: stringFromAny(output["visible_message"]),
		Data:           data,
	})
}

func localVisibleModelOutput(job protocol.PlaybotJob) map[string]any {
	steps := make([]map[string]any, 0, len(job.RecordingSource.ActionTrace))
	for _, action := range job.RecordingSource.ActionTrace {
		steps = append(steps, map[string]any{
			"action":         finalActionFromRecorded(action.Type),
			"target_summary": targetSummary(action.Target),
			"value_summary":  valueSummary(action),
			"reason":         strings.TrimSpace(action.IntentReason),
		})
	}
	message := "已读取录制流程，准备生成可执行测试用例。"
	if len(steps) > 0 {
		message = fmt.Sprintf("已读取 %d 个录制步骤，正在生成可执行测试用例。", len(steps))
	}
	return map[string]any{
		"visible_message": message,
		"candidate_steps": steps,
		"assumptions":     []string{},
		"risk_notes":      []string{},
	}
}

func responseToModelOutput(response llmclient.Response) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(response.VisibleMessage) != "" {
		out["visible_message"] = response.VisibleMessage
	}
	if len(response.CandidateSteps) > 0 {
		steps := make([]map[string]any, 0, len(response.CandidateSteps))
		for _, step := range response.CandidateSteps {
			steps = append(steps, map[string]any{
				"action":         step.Action,
				"target_summary": step.TargetSummary,
				"value_summary":  maskCandidateValueSummary(step.ValueSummary),
				"reason":         step.Reason,
			})
		}
		out["candidate_steps"] = steps
	}
	if len(response.Assumptions) > 0 {
		out["assumptions"] = response.Assumptions
	}
	if len(response.RiskNotes) > 0 {
		out["risk_notes"] = response.RiskNotes
	}
	return out
}

func visibleSummaryFromModelOutput(output map[string]any, fallback string) string {
	if text := strings.TrimSpace(stringFromAny(output["visible_message"])); text != "" {
		return text
	}
	return strings.TrimSpace(fallback)
}

func playbotVisibleOutputSystemPrompt() string {
	return strings.Join([]string{
		"You are BrowserWing Playbot.",
		"Return only JSON.",
		"Do not include chain-of-thought, hidden reasoning, raw prompts, API keys, cookies, storage values, or local file paths.",
		"Explain the generation in user-visible language, list candidate steps with sensitive input values masked, and return a semantic_plan that only references recorded actions and targets.",
		"Prefer stable target_hint fields from recorded actions, especially ref_id, recorded_selector, or selector; only use text, label, or placeholder when no stable field is available.",
		"For recorded input values, use the literal placeholder <recorded_input_value>; BrowserWing will hydrate the recorded value after validating the plan.",
	}, " ")
}

func playbotVisibleOutputUserPrompt(job protocol.PlaybotJob) string {
	type promptAction struct {
		Action        string            `json:"action"`
		TargetSummary string            `json:"target_summary"`
		TargetHint    map[string]string `json:"target_hint"`
		Value         string            `json:"value,omitempty"`
		Reason        string            `json:"reason,omitempty"`
	}
	actions := make([]promptAction, 0, len(job.RecordingSource.ActionTrace))
	for _, action := range job.RecordingSource.ActionTrace {
		value := ""
		if actionRequiresValue(finalActionFromRecorded(action.Type)) && strings.TrimSpace(action.Value) != "" {
			value = "<recorded_input_value>"
		}
		actions = append(actions, promptAction{
			Action:        finalActionFromRecorded(action.Type),
			TargetSummary: targetSummary(action.Target),
			TargetHint:    promptTargetHint(action),
			Value:         value,
			Reason:        strings.TrimSpace(action.IntentReason),
		})
	}
	payload := map[string]any{
		"mode":             job.Mode,
		"user_instruction": job.UserInstruction,
		"page": map[string]any{
			"url":         job.PageContext.URL,
			"description": job.PageContext.Description,
		},
		"actions": actions,
		"expected_json": map[string]any{
			"visible_message": "short user-visible explanation",
			"candidate_steps": []map[string]string{{"action": "click", "target_summary": "Save button", "value_summary": "", "reason": "why this step matters"}},
			"assumptions":     []string{},
			"risk_notes":      []string{},
			"semantic_plan": map[string]any{
				"title":       "generated test case title",
				"description": "generated test case description",
				"steps": []map[string]any{{
					"action":        "click",
					"target_hint":   map[string]string{"ref_id": "recorded_action_0", "recorded_selector": "button.save", "text": "Save button"},
					"intent_reason": "why this executable step is needed",
				}},
			},
		},
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func promptTargetHint(action protocol.RecordedAction) map[string]string {
	target := action.Target
	out := map[string]string{}
	appendValue := func(key string, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		out[key] = value
	}
	appendValue("ref_id", firstNonEmpty(target.RefID, action.RefID))
	appendValue("recorded_selector", target.RecordedSelector)
	appendValue("selector", target.Selector)
	appendValue("role", target.Role)
	appendValue("text", target.Text)
	appendValue("label", target.Label)
	appendValue("placeholder", target.Placeholder)
	return out
}

func targetSummary(target protocol.RecordedTarget) string {
	for _, value := range []string{target.Text, target.Label, target.Placeholder, target.Role, target.RecordedSelector, target.Selector, target.RefID} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "recorded target"
}

func valueSummary(action protocol.RecordedAction) string {
	if !actionRequiresValue(finalActionFromRecorded(action.Type)) || strings.TrimSpace(action.Value) == "" {
		return ""
	}
	return "已录制输入值"
}

func maskCandidateValueSummary(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "已录制输入值"
}

func actionRequiresValue(action string) bool {
	switch action {
	case "fill", "select", "expect_text":
		return true
	default:
		return false
	}
}

func boolLimit(limits map[string]any, key string) bool {
	switch typed := limits[key].(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
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

func compileGenerateBlueprint(job protocol.PlaybotJob, semanticPlan json.RawMessage) (map[string]any, error) {
	if !boolLimit(job.Limits, "enable_llm") {
		return compileRecordedJob(job)
	}
	plan, err := decodeModelSemanticPlan(semanticPlan)
	if err != nil {
		return nil, err
	}
	anchored, err := anchorModelSemanticPlanToRecording(plan, job)
	if err != nil {
		return nil, err
	}
	blueprint, err := compiler.CompileBlueprint(anchored, compiler.CompileContext{BaseURL: job.PageContext.URL})
	if err != nil {
		return nil, err
	}
	blueprint.AuthContext = job.RecordingSource.RecordingMeta.AuthContext
	return mapFromJSON(blueprint)
}

func decodeModelSemanticPlan(raw json.RawMessage) (compiler.SemanticPlan, error) {
	if len(strings.TrimSpace(string(raw))) == 0 || string(raw) == "null" {
		return compiler.SemanticPlan{}, fmt.Errorf("playbot_llm_semantic_plan_required")
	}
	var plan compiler.SemanticPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return compiler.SemanticPlan{}, fmt.Errorf("playbot_llm_semantic_plan_invalid")
	}
	if len(plan.Steps) == 0 {
		return compiler.SemanticPlan{}, fmt.Errorf("playbot_llm_semantic_plan_empty")
	}
	return plan, nil
}

type recordedPlanAnchor struct {
	action string
	target compiler.TargetHint
	value  string
	url    string
}

func anchorModelSemanticPlanToRecording(plan compiler.SemanticPlan, job protocol.PlaybotJob) (compiler.SemanticPlan, error) {
	anchors := recordedPlanAnchors(job)
	approvedURLs := approvedNavigationURLs(job)
	anchored := compiler.SemanticPlan{
		Title:       firstNonEmpty(plan.Title, job.UserInstruction, job.PageContext.Description, "Generated Playbot case"),
		Description: firstNonEmpty(plan.Description, "Generated from Playbot model semantic plan"),
		Steps:       make([]compiler.SemanticStep, 0, len(plan.Steps)),
	}
	for _, step := range plan.Steps {
		step.Action = finalActionFromRecorded(step.Action)
		switch step.Action {
		case "navigate":
			rawURL := firstNonEmpty(step.URL, step.Value)
			if strings.TrimSpace(rawURL) == "" && len(approvedURLs) == 1 {
				rawURL = approvedURLs[0]
			}
			if !isApprovedNavigationURL(rawURL, approvedURLs, job.PageContext.URL) {
				return compiler.SemanticPlan{}, fmt.Errorf("playbot_llm_semantic_plan_unapproved_url")
			}
			step.URL = rawURL
			step.Value = ""
		case "click", "expect_visible", "fill", "select", "expect_text", "wait":
			if step.Action == "wait" && !semanticTargetHintHasLocator(step.TargetHint) {
				anchored.Steps = append(anchored.Steps, step)
				continue
			}
			anchor, ok := findRecordedPlanAnchor(step.Action, step.TargetHint, anchors)
			if !ok {
				return compiler.SemanticPlan{}, fmt.Errorf("playbot_llm_semantic_plan_unapproved_target")
			}
			step.TargetHint = anchor.target
			if actionRequiresValue(step.Action) {
				if !semanticValueUsesRecordedInput(step.Value, anchor.value) {
					return compiler.SemanticPlan{}, fmt.Errorf("playbot_llm_semantic_plan_unapproved_value")
				}
				if strings.TrimSpace(anchor.value) == "" {
					return compiler.SemanticPlan{}, fmt.Errorf("playbot_llm_semantic_plan_missing_recorded_value")
				}
				step.Value = anchor.value
			} else {
				step.Value = ""
			}
		default:
			return compiler.SemanticPlan{}, fmt.Errorf("blueprint_unsupported_action")
		}
		anchored.Steps = append(anchored.Steps, step)
	}
	return anchored, nil
}

func recordedPlanAnchors(job protocol.PlaybotJob) []recordedPlanAnchor {
	anchors := make([]recordedPlanAnchor, 0, len(job.RecordingSource.ActionTrace))
	for _, action := range job.RecordingSource.ActionTrace {
		anchors = append(anchors, recordedPlanAnchor{
			action: finalActionFromRecorded(action.Type),
			target: compiler.TargetHint{
				Role:             action.Target.Role,
				Text:             action.Target.Text,
				Placeholder:      action.Target.Placeholder,
				Label:            action.Target.Label,
				Selector:         action.Target.Selector,
				RecordedSelector: action.Target.RecordedSelector,
				RefID:            firstNonEmpty(action.Target.RefID, action.RefID),
			},
			value: action.Value,
			url:   action.URL,
		})
	}
	return anchors
}

func approvedNavigationURLs(job protocol.PlaybotJob) []string {
	seen := map[string]struct{}{}
	var urls []string
	appendURL := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		urls = append(urls, value)
	}
	appendURL(job.PageContext.URL)
	appendURL(job.RecordingSource.RecordingMeta.TargetURL)
	for _, action := range job.RecordingSource.ActionTrace {
		appendURL(action.URL)
	}
	return urls
}

func isApprovedNavigationURL(raw string, approved []string, baseURL string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	resolvedRaw := resolveComparableURL(baseURL, raw)
	for _, allowed := range approved {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		if raw == allowed || resolvedRaw == resolveComparableURL(baseURL, allowed) {
			return true
		}
	}
	return false
}

func resolveComparableURL(baseURL, raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.IsAbs() {
		return parsed.String()
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return raw
	}
	return base.ResolveReference(parsed).String()
}

func findRecordedPlanAnchor(action string, hint compiler.TargetHint, anchors []recordedPlanAnchor) (recordedPlanAnchor, bool) {
	if !semanticTargetHintHasLocator(hint) {
		return recordedPlanAnchor{}, false
	}
	matches := make([]recordedPlanAnchor, 0, 1)
	for _, anchor := range anchors {
		if anchor.action == action && semanticTargetHintMatches(hint, anchor.target) {
			matches = append(matches, anchor)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	if len(matches) > 1 {
		return recordedPlanAnchor{}, false
	}
	if action == "expect_visible" || action == "expect_text" {
		for _, anchor := range anchors {
			if semanticTargetHintMatches(hint, anchor.target) {
				matches = append(matches, anchor)
			}
		}
	}
	if len(matches) != 1 {
		return recordedPlanAnchor{}, false
	}
	return matches[0], true
}

func semanticTargetHintMatches(candidate compiler.TargetHint, recorded compiler.TargetHint) bool {
	if semanticTargetHintHasStableLocator(candidate) {
		return semanticStableTargetHintMatches(candidate, recorded)
	}
	if bothEqual(candidate.Text, recorded.Text) ||
		bothEqual(candidate.Label, recorded.Label) ||
		bothEqual(candidate.Placeholder, recorded.Placeholder) {
		return true
	}
	return bothEqual(candidate.Role, recorded.Role) && bothEqual(candidate.Text, recorded.Text)
}

func semanticTargetHintHasStableLocator(hint compiler.TargetHint) bool {
	return strings.TrimSpace(hint.RefID) != "" ||
		strings.TrimSpace(hint.RecordedSelector) != "" ||
		strings.TrimSpace(hint.Selector) != ""
}

func semanticStableTargetHintMatches(candidate compiler.TargetHint, recorded compiler.TargetHint) bool {
	for _, pair := range []struct {
		candidate string
		recorded  string
	}{
		{candidate: candidate.RefID, recorded: recorded.RefID},
		{candidate: candidate.RecordedSelector, recorded: recorded.RecordedSelector},
		{candidate: candidate.Selector, recorded: recorded.Selector},
	} {
		value := strings.TrimSpace(pair.candidate)
		if value == "" {
			continue
		}
		if value != strings.TrimSpace(pair.recorded) {
			return false
		}
	}
	return true
}

func semanticTargetHintHasLocator(hint compiler.TargetHint) bool {
	return strings.TrimSpace(hint.RefID) != "" ||
		strings.TrimSpace(hint.RecordedSelector) != "" ||
		strings.TrimSpace(hint.Selector) != "" ||
		strings.TrimSpace(hint.Text) != "" ||
		strings.TrimSpace(hint.Label) != "" ||
		strings.TrimSpace(hint.Placeholder) != ""
}

func semanticValueUsesRecordedInput(modelValue string, recordedValue string) bool {
	modelValue = strings.TrimSpace(modelValue)
	recordedValue = strings.TrimSpace(recordedValue)
	if modelValue == "" || strings.EqualFold(modelValue, "<recorded_input_value>") || strings.EqualFold(modelValue, "recorded_input_value") || modelValue == "已录制输入值" {
		return true
	}
	return recordedValue != "" && modelValue == recordedValue
}

func bothEqual(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left != "" && right != "" && left == right
}

func finalActionFromRecorded(actionType string) string {
	switch strings.TrimSpace(actionType) {
	case "input":
		return "fill"
	default:
		return strings.TrimSpace(actionType)
	}
}

func validateOptimizePrerequisites(job protocol.PlaybotJob) error {
	if len(job.ExistingBlueprint) == 0 {
		return fmt.Errorf("playbot_existing_blueprint_required")
	}
	instruction := strings.TrimSpace(job.UserInstruction)
	hasExecutionContext := len(job.ExecutionReport) > 0
	hasRecordingFacts := len(job.RecordingSource.ActionTrace) > 0 || len(job.RecordingSource.DOMSnapshot) > 0
	if instruction == "" && !hasExecutionContext && !hasRecordingFacts {
		return fmt.Errorf("playbot_optimize_context_required")
	}
	return nil
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
