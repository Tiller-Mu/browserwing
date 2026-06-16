package protocol

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const (
	SchemaVersion = "p4.7.5"

	ModeGenerate       = "generate"
	ModeOptimize       = "optimize"
	ModeRepairProposal = "repair_proposal"

	StatusSuccess         = "success"
	StatusFailed          = "failed"
	StatusContextRequired = "context_required"
)

type PlaybotJob struct {
	SchemaVersion          string                   `json:"schema_version"`
	Mode                   string                   `json:"mode"`
	RequestID              string                   `json:"request_id"`
	ProjectScope           ProjectScope             `json:"project_scope,omitempty"`
	PageContext            PageContext              `json:"page_context,omitempty"`
	RecordingSource        RecordingSource          `json:"recording_source,omitempty"`
	BackendApprovedContext []BackendApprovedContext `json:"backend_approved_context,omitempty"`
	ExistingBlueprint      map[string]any           `json:"existing_blueprint,omitempty"`
	ExecutionReport        map[string]any           `json:"execution_report,omitempty"`
	ContextWarnings        []map[string]any         `json:"context_warnings,omitempty"`
	UserInstruction        string                   `json:"user_instruction,omitempty"`
	LLMRuntimeConfig       LLMRuntimeConfig         `json:"llm_runtime_config,omitempty"`
	Limits                 map[string]any           `json:"limits,omitempty"`
}

type ProjectScope struct {
	ProjectID uint `json:"project_id,omitempty"`
	VersionID uint `json:"version_id,omitempty"`
	PageID    uint `json:"page_id,omitempty"`
}

type PageContext struct {
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
}

type RecordingSource struct {
	SourcePageScriptID       string           `json:"source_page_script_id,omitempty"`
	SourceRecordingSessionID string           `json:"source_recording_session_id,omitempty"`
	ActionTrace              []RecordedAction `json:"action_trace,omitempty"`
	DOMSnapshot              map[string]any   `json:"dom_snapshot,omitempty"`
	RecordingMeta            RecordingMeta    `json:"recording_meta,omitempty"`
}

type RecordedAction struct {
	Type         string         `json:"type,omitempty"`
	Target       RecordedTarget `json:"target,omitempty"`
	Value        string         `json:"value,omitempty"`
	URL          string         `json:"url,omitempty"`
	IntentReason string         `json:"intent_reason,omitempty"`
	RefID        string         `json:"ref_id,omitempty"`
}

type RecordedTarget struct {
	Role             string `json:"role,omitempty"`
	Text             string `json:"text,omitempty"`
	Placeholder      string `json:"placeholder,omitempty"`
	Label            string `json:"label,omitempty"`
	Selector         string `json:"selector,omitempty"`
	RecordedSelector string `json:"recorded_selector,omitempty"`
	RefID            string `json:"ref_id,omitempty"`
}

type RecordingMeta struct {
	SchemaVersion  int    `json:"schema_version,omitempty"`
	RecordingKind  string `json:"recording_kind,omitempty"`
	AuthContext    string `json:"auth_context,omitempty"`
	TargetURL      string `json:"target_url,omitempty"`
	CapturedAt     string `json:"captured_at,omitempty"`
	SessionVersion string `json:"session_version,omitempty"`
}

type BackendApprovedContext struct {
	Kind    string `json:"kind"`
	Scope   string `json:"scope"`
	Source  string `json:"source"`
	Payload any    `json:"payload"`
}

type LLMRuntimeConfig struct {
	Provider        string           `json:"provider,omitempty"`
	Endpoint        string           `json:"endpoint,omitempty"`
	Model           string           `json:"model,omitempty"`
	ConfigID        string           `json:"config_id,omitempty"`
	TimeoutMs       int              `json:"timeout_ms,omitempty"`
	RetryCount      int              `json:"retry_count,omitempty"`
	RedactedSummary string           `json:"redacted_summary,omitempty"`
	SecretChannel   SecretChannelRef `json:"secret_channel,omitempty"`
}

type SecretChannelRef struct {
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
}

type LLMRuntimeConfigInput struct {
	Provider   string
	Endpoint   string
	Model      string
	ConfigID   string
	APIKey     string
	TimeoutMs  int
	RetryCount int
}

type SecretChannel struct {
	kind  string
	name  string
	value string
}

type PlaybotResult struct {
	SchemaVersion    string           `json:"schema_version"`
	Status           string           `json:"status"`
	Code             string           `json:"code,omitempty"`
	TestCases        []map[string]any `json:"test_cases,omitempty"`
	RefinedBlueprint map[string]any   `json:"refined_blueprint,omitempty"`
	Summary          string           `json:"summary,omitempty"`
	RiskNotes        string           `json:"risk_notes,omitempty"`
	QualityErrors    []map[string]any `json:"quality_errors,omitempty"`
	RequestedContext []map[string]any `json:"requested_context,omitempty"`
	ContextTrace     ContextTrace     `json:"context_trace,omitempty"`
	Warnings         []map[string]any `json:"warnings,omitempty"`
	RepairProposal   map[string]any   `json:"repair_proposal,omitempty"`
	Retryable        bool             `json:"retryable,omitempty"`
}

type ContextTrace struct {
	SourcePageScriptID       string   `json:"source_page_script_id,omitempty"`
	SourceRecordingSessionID string   `json:"source_recording_session_id,omitempty"`
	SourceHash               string   `json:"source_hash,omitempty"`
	UsedFields               []string `json:"used_fields,omitempty"`
	Truncated                []string `json:"truncated,omitempty"`
}

func MarshalPlaybotJob(job PlaybotJob) ([]byte, error) {
	if job.SchemaVersion == "" {
		job.SchemaVersion = SchemaVersion
	}
	raw, err := json.Marshal(job)
	if err != nil {
		return nil, err
	}
	if containsForbiddenProtocolMaterial(string(raw)) {
		return nil, fmt.Errorf("playbot_job_secret_leak")
	}
	return raw, nil
}

func DecodeAndValidatePlaybotJob(raw []byte) (PlaybotJob, error) {
	if containsForbiddenProtocolMaterial(string(raw)) {
		return PlaybotJob{}, fmt.Errorf("playbot_job_secret_leak")
	}
	var job PlaybotJob
	if err := json.Unmarshal(raw, &job); err != nil {
		return PlaybotJob{}, err
	}
	if strings.TrimSpace(job.SchemaVersion) != SchemaVersion {
		return PlaybotJob{}, fmt.Errorf("playbot_job_invalid_schema_version")
	}
	for _, item := range job.BackendApprovedContext {
		if strings.TrimSpace(item.Kind) == "" || strings.TrimSpace(item.Scope) == "" || strings.TrimSpace(item.Source) == "" || item.Payload == nil {
			return PlaybotJob{}, fmt.Errorf("playbot_job_backend_approved_context_invalid")
		}
	}
	return job, nil
}

func DecodeAndValidatePlaybotResult(raw []byte) (PlaybotResult, error) {
	var result PlaybotResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return PlaybotResult{}, err
	}
	if strings.TrimSpace(result.SchemaVersion) != SchemaVersion {
		return PlaybotResult{}, fmt.Errorf("playbot_result_invalid_schema_version")
	}
	switch result.Status {
	case StatusSuccess, StatusFailed, StatusContextRequired:
	default:
		return PlaybotResult{}, fmt.Errorf("playbot_result_invalid_status")
	}
	return result, nil
}

func BuildLLMRuntimeConfig(input LLMRuntimeConfigInput) (LLMRuntimeConfig, SecretChannel, error) {
	if strings.TrimSpace(input.APIKey) == "" {
		return LLMRuntimeConfig{}, SecretChannel{}, fmt.Errorf("playbot_llm_secret_required")
	}
	envName := "BROWSERWING_PLAYBOT_LLM_API_KEY"
	cfg := LLMRuntimeConfig{
		Provider:        strings.TrimSpace(input.Provider),
		Endpoint:        strings.TrimSpace(input.Endpoint),
		Model:           strings.TrimSpace(input.Model),
		ConfigID:        strings.TrimSpace(input.ConfigID),
		TimeoutMs:       input.TimeoutMs,
		RetryCount:      input.RetryCount,
		RedactedSummary: strings.TrimSpace(input.Provider + " " + input.Model),
		SecretChannel:   SecretChannelRef{Kind: "env", Name: envName},
	}
	channel := SecretChannel{kind: "env", name: envName, value: input.APIKey}
	return cfg, channel, nil
}

func (c SecretChannel) ResolveForProcess() (string, error) {
	if c.value != "" {
		return c.value, nil
	}
	if strings.TrimSpace(c.name) == "" {
		return "", fmt.Errorf("playbot_secret_channel_invalid")
	}
	value := os.Getenv(c.name)
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("playbot_secret_channel_empty")
	}
	return value, nil
}

func containsForbiddenProtocolMaterial(text string) bool {
	var payload any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return containsForbiddenProtocolString(text)
	}
	return containsForbiddenProtocolValue(payload)
}

func containsForbiddenProtocolValue(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if isForbiddenProtocolKey(key) || containsForbiddenProtocolValue(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsForbiddenProtocolValue(child) {
				return true
			}
		}
	case string:
		return containsForbiddenProtocolString(typed)
	}
	return false
}

func isForbiddenProtocolKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	switch normalized {
	case "apikey", "cookie", "cookies", "localstorage", "sessionstorage", "profilepath":
		return true
	default:
		return false
	}
}

func containsForbiddenProtocolString(text string) bool {
	if strings.Contains(strings.ToLower(text), `c:\users\`) {
		return true
	}
	return false
}
