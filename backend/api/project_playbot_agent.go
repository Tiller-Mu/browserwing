package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/services/playbotagent"
)

const p475SchemaVersion = "p4.7.5"

type p475RecordingSource struct {
	PageScriptID string
	ActionTrace  []map[string]any
	DOMSnapshot  map[string]any
	Meta         map[string]any
	AuthContext  string
	SourceHash   string
}

func buildP475RecordingSource(script models.PageScript) (p475RecordingSource, error) {
	actions, err := parseP475ActionTrace(script.ActionTrace)
	if err != nil {
		return p475RecordingSource{}, err
	}
	snapshot, err := parseP475Object(script.DOMSnapshot, "页面快照 JSON 非法")
	if err != nil {
		return p475RecordingSource{}, err
	}
	meta, parsedMeta, err := parseP475RecordingMetaObject(script.RecordingMetaJSON)
	if err != nil {
		return p475RecordingSource{}, err
	}
	authContext := strings.TrimSpace(parsedMeta.AuthContext)
	if authContext == "" {
		authContext = authContextClean
	}
	return p475RecordingSource{
		PageScriptID: fmt.Sprintf("ps_%d", script.ID),
		ActionTrace:  sanitizeP475ActionTrace(actions),
		DOMSnapshot:  sanitizeP475Object(snapshot),
		Meta:         sanitizeP475Object(meta),
		AuthContext:  authContext,
		SourceHash:   p475SourceHash(script.ActionTrace, script.DOMSnapshot, script.RecordingMetaJSON),
	}, nil
}

func buildP475GenerateJob(projectID, versionID, pageID uint, version models.ProjectVersion, page models.TestPage, source p475RecordingSource, instruction string, llmConfig *models.LLMConfigModel) (map[string]any, playbotagent.SecretChannel) {
	secretEnv := "BROWSERWING_PLAYBOT_LLM_API_KEY"
	job := map[string]any{
		"schema_version": p475SchemaVersion,
		"mode":           "generate",
		"request_id":     fmt.Sprintf("generate-%d-%d-%d", projectID, versionID, pageID),
		"project_scope": map[string]any{
			"project_id": projectID,
			"version_id": versionID,
			"page_id":    pageID,
		},
		"page_context": map[string]any{
			"url":         buildPageURL(version.BaseURL, page.Path),
			"description": page.Description,
		},
		"recording_source": map[string]any{
			"source_page_script_id": source.PageScriptID,
			"action_trace":          source.ActionTrace,
			"dom_snapshot":          source.DOMSnapshot,
			"recording_meta":        source.Meta,
		},
		"user_instruction": strings.TrimSpace(instruction),
		"llm_runtime_config": map[string]any{
			"provider":         llmConfig.Provider,
			"endpoint":         llmConfig.BaseURL,
			"model":            llmConfig.Model,
			"config_id":        llmConfig.ID,
			"redacted_summary": strings.TrimSpace(llmConfig.Provider + " " + llmConfig.Model),
			"secret_channel": map[string]any{
				"kind": "env",
				"name": secretEnv,
			},
		},
		"limits": map[string]any{
			"max_actions":   50,
			"max_dom_nodes": 500,
		},
	}
	return job, playbotagent.SecretChannel{EnvName: secretEnv, Value: llmConfig.APIKey}
}

func buildP475OptimizeJob(projectID, versionID, pageID uint, version models.ProjectVersion, page models.TestPage, testCase models.TestCase, currentBlueprint map[string]any, prompt string, snapshot any, intentPlan any, contextWarnings []map[string]string, executionReport any, llmConfig *models.LLMConfigModel) (map[string]any, playbotagent.SecretChannel) {
	secretEnv := "BROWSERWING_PLAYBOT_LLM_API_KEY"
	recordingSource := map[string]any{}
	if actions := actionTraceFromParsed(intentPlan); len(actions) > 0 {
		recordingSource["action_trace"] = sanitizeP475ActionTrace(actions)
	}
	if obj, ok := snapshot.(map[string]any); ok {
		recordingSource["dom_snapshot"] = sanitizeP475Object(obj)
	}
	job := map[string]any{
		"schema_version": p475SchemaVersion,
		"mode":           "optimize",
		"request_id":     fmt.Sprintf("optimize-%d-%d-%d-%d", projectID, versionID, pageID, testCase.ID),
		"project_scope": map[string]any{
			"project_id": projectID,
			"version_id": versionID,
			"page_id":    pageID,
		},
		"page_context": map[string]any{
			"url":         buildPageURL(version.BaseURL, page.Path),
			"description": page.Description,
		},
		"recording_source":   recordingSource,
		"existing_blueprint": currentBlueprint,
		"user_instruction":   strings.TrimSpace(prompt),
		"llm_runtime_config": map[string]any{
			"provider":         llmConfig.Provider,
			"endpoint":         llmConfig.BaseURL,
			"model":            llmConfig.Model,
			"config_id":        llmConfig.ID,
			"redacted_summary": strings.TrimSpace(llmConfig.Provider + " " + llmConfig.Model),
			"secret_channel": map[string]any{
				"kind": "env",
				"name": secretEnv,
			},
		},
	}
	if report, ok := executionReport.(map[string]any); ok {
		job["execution_report"] = sanitizeP475Object(report)
	}
	if len(contextWarnings) > 0 {
		items := make([]map[string]any, 0, len(contextWarnings))
		for _, warning := range contextWarnings {
			items = append(items, map[string]any{
				"code":    strings.TrimSpace(warning["code"]),
				"message": strings.TrimSpace(warning["message"]),
			})
		}
		job["context_warnings"] = items
	}
	return job, playbotagent.SecretChannel{EnvName: secretEnv, Value: llmConfig.APIKey}
}

func (h *ProjectHandlers) runP475AgentWithContextRetry(ctx context.Context, job map[string]any, secret playbotagent.SecretChannel, source p475RecordingSource) (map[string]any, error) {
	result, err := h.playbotAgent.run(ctx, job, secret)
	if err != nil {
		return nil, err
	}
	if stringFromAny(result["status"]) != "context_required" || !boolFromAny(result["retryable"]) {
		return result, nil
	}
	approved, ok := buildP475ApprovedContexts(result["requested_context"], source)
	if !ok {
		return result, nil
	}
	retryJob := cloneP475Map(job)
	retryJob["backend_approved_context"] = approved
	second, err := h.playbotAgent.run(ctx, retryJob, secret)
	if err != nil {
		return nil, err
	}
	return second, nil
}

func buildP475ApprovedContexts(raw any, source p475RecordingSource) ([]map[string]any, bool) {
	requests, ok := raw.([]any)
	if !ok || len(requests) == 0 {
		return nil, false
	}
	approved := make([]map[string]any, 0, len(requests))
	for _, item := range requests {
		req, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		kind := strings.TrimSpace(stringFromAny(req["kind"]))
		scope := strings.TrimSpace(stringFromAny(req["scope"]))
		if kind == "" || scope == "" {
			return nil, false
		}
		var payload any
		var sourceName string
		switch kind {
		case "dom_snapshot":
			payload = source.DOMSnapshot
			sourceName = "page_script.dom_snapshot"
		case "action_trace":
			payload = source.ActionTrace
			sourceName = "page_script.action_trace"
		case "recording_meta":
			payload = source.Meta
			sourceName = "page_script.recording_meta"
		case "page_context":
			payload = map[string]any{"source_page_script_id": source.PageScriptID}
			sourceName = "page_script.page_context"
		default:
			return nil, false
		}
		if isP475EmptyPayload(payload) {
			return nil, false
		}
		approved = append(approved, map[string]any{
			"kind":    kind,
			"scope":   scope,
			"source":  sourceName,
			"payload": payload,
		})
	}
	return approved, true
}

func parseP475ActionTrace(raw string) ([]map[string]any, error) {
	parsed, err := parseRequiredJSON(raw, "主流程录制 JSON 非法")
	if err != nil {
		return nil, err
	}
	return actionTraceFromParsed(parsed), nil
}

func actionTraceFromParsed(parsed any) []map[string]any {
	switch typed := parsed.(type) {
	case []any:
		return anySliceToMapSlice(typed)
	case map[string]any:
		for _, key := range []string{"actions", "steps"} {
			if items, ok := typed[key].([]any); ok {
				return anySliceToMapSlice(items)
			}
		}
	}
	return nil
}

func anySliceToMapSlice(items []any) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if obj, ok := item.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}

func parseP475Object(raw string, message string) (map[string]any, error) {
	parsed, err := parseRequiredJSON(raw, message)
	if err != nil {
		return nil, err
	}
	obj, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s", message)
	}
	return obj, nil
}

func parseP475RecordingMetaObject(raw string) (map[string]any, p45RecordingMeta, error) {
	meta, _, err := parseRecordingMetaJSON(raw)
	if err != nil {
		return nil, p45RecordingMeta{}, err
	}
	obj, err := parseP475Object(raw, "recording_meta JSON is invalid")
	if err != nil {
		return nil, p45RecordingMeta{}, err
	}
	return obj, meta, nil
}

func sanitizeP475ActionTrace(actions []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(actions))
	for _, action := range actions {
		if sanitized, ok := sanitizeP475Value(action).(map[string]any); ok {
			out = append(out, sanitized)
		}
	}
	return out
}

func sanitizeP475Object(obj map[string]any) map[string]any {
	if sanitized, ok := sanitizeP475Value(obj).(map[string]any); ok {
		return sanitized
	}
	return map[string]any{}
}

func sanitizeP475Value(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if p475ForbiddenKey(key) {
				continue
			}
			sanitized := sanitizeP475Value(item)
			if sanitized == nil {
				continue
			}
			out[key] = sanitized
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			sanitized := sanitizeP475Value(item)
			if sanitized != nil {
				out = append(out, sanitized)
			}
		}
		return out
	case string:
		if p475ForbiddenString(typed) {
			return nil
		}
		return typed
	default:
		return value
	}
}

func p475ForbiddenKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	for _, token := range []string{"cookie", "localstorage", "sessionstorage", "api_key", "apikey", "profile_path"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func p475ForbiddenString(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, `c:\users\`)
}

func p475SourceHash(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		hash.Write([]byte(part))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func cloneP475Map(input map[string]any) map[string]any {
	data, _ := json.Marshal(input)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

func isP475EmptyPayload(payload any) bool {
	switch typed := payload.(type) {
	case nil:
		return true
	case map[string]any:
		return len(typed) == 0
	case []map[string]any:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	case string:
		return strings.TrimSpace(typed) == ""
	default:
		return false
	}
}
