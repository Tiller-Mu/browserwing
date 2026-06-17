package playbotagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	schemaVersion = "p4.7.5"

	statusSuccess         = "success"
	statusFailed          = "failed"
	statusContextRequired = "context_required"
)

type BinaryClientOptions struct {
	CommandPath     string
	SecretEnvName   string
	WorkingDir      string
	EngineDir       string
	RedactLocalPath bool
}

type BinaryClient struct {
	commandPath     string
	secretEnvName   string
	workingDir      string
	redactLocalPath bool
}

type RunOptions struct {
	EventSink func(Event)
}

type Job struct {
	SchemaVersion          string                   `json:"schema_version"`
	Mode                   string                   `json:"mode"`
	RequestID              string                   `json:"request_id,omitempty"`
	ProjectScope           map[string]any           `json:"project_scope,omitempty"`
	PageContext            map[string]any           `json:"page_context,omitempty"`
	RecordingSource        RecordingSource          `json:"recording_source,omitempty"`
	BackendApprovedContext []BackendApprovedContext `json:"backend_approved_context,omitempty"`
	ExistingBlueprint      map[string]any           `json:"existing_blueprint,omitempty"`
	ExecutionReport        map[string]any           `json:"execution_report,omitempty"`
	ContextWarnings        []map[string]any         `json:"context_warnings,omitempty"`
	UserInstruction        string                   `json:"user_instruction,omitempty"`
	LLMRuntimeConfig       LLMRuntimeConfig         `json:"llm_runtime_config,omitempty"`
	Limits                 map[string]any           `json:"limits,omitempty"`
	SecretChannel          SecretChannel            `json:"-"`
}

type RecordingSource struct {
	SourcePageScriptID       string           `json:"source_page_script_id,omitempty"`
	SourceRecordingSessionID string           `json:"source_recording_session_id,omitempty"`
	ActionTrace              []map[string]any `json:"action_trace,omitempty"`
	DOMSnapshot              map[string]any   `json:"dom_snapshot,omitempty"`
	RecordingMeta            map[string]any   `json:"recording_meta,omitempty"`
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

type SecretChannel struct {
	EnvName string
	Value   string
}

type Result struct {
	SchemaVersion    string           `json:"schema_version"`
	Status           string           `json:"status"`
	Code             string           `json:"code,omitempty"`
	TestCases        []map[string]any `json:"test_cases,omitempty"`
	RefinedBlueprint map[string]any   `json:"refined_blueprint,omitempty"`
	QualityErrors    []map[string]any `json:"quality_errors,omitempty"`
	RequestedContext []map[string]any `json:"requested_context,omitempty"`
	ContextTrace     map[string]any   `json:"context_trace,omitempty"`
	Warnings         []map[string]any `json:"warnings,omitempty"`
	RepairProposal   map[string]any   `json:"repair_proposal,omitempty"`
	Retryable        bool             `json:"retryable,omitempty"`
	Summary          string           `json:"summary,omitempty"`
	RiskNotes        string           `json:"risk_notes,omitempty"`
	VisibleSummary   string           `json:"visible_summary,omitempty"`
	ModelOutput      map[string]any   `json:"model_output,omitempty"`
}

type Event struct {
	SchemaVersion  string         `json:"schema_version"`
	RunID          string         `json:"run_id,omitempty"`
	RequestID      string         `json:"request_id,omitempty"`
	Seq            int64          `json:"seq"`
	Phase          string         `json:"phase"`
	Level          string         `json:"level,omitempty"`
	Message        string         `json:"message,omitempty"`
	VisibleMessage string         `json:"visible_message,omitempty"`
	Data           map[string]any `json:"data,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

func NewBinaryClient(opts BinaryClientOptions) (*BinaryClient, error) {
	commandPath := strings.TrimSpace(opts.CommandPath)
	if commandPath == "" {
		commandPath = strings.TrimSpace(os.Getenv("BROWSERWING_PLAYBOT_AGENT_PATH"))
	}
	if commandPath == "" {
		commandPath = "browserwing-playbot-agent"
	}
	if isPythonPlaybotCommand(commandPath, opts.EngineDir) {
		return nil, fmt.Errorf("playbot_agent_python_engine_forbidden")
	}
	secretEnvName := strings.TrimSpace(opts.SecretEnvName)
	if secretEnvName == "" {
		secretEnvName = "BROWSERWING_PLAYBOT_LLM_API_KEY"
	}
	return &BinaryClient{
		commandPath:     commandPath,
		secretEnvName:   secretEnvName,
		workingDir:      strings.TrimSpace(opts.WorkingDir),
		redactLocalPath: opts.RedactLocalPath,
	}, nil
}

func (c *BinaryClient) Run(ctx context.Context, job Job) (Result, error) {
	return c.RunWithEvents(ctx, job, RunOptions{})
}

func (c *BinaryClient) RunWithEvents(ctx context.Context, job Job, opts RunOptions) (Result, error) {
	if strings.TrimSpace(job.SchemaVersion) == "" {
		job.SchemaVersion = schemaVersion
	}
	if strings.TrimSpace(job.LLMRuntimeConfig.SecretChannel.Kind) == "" && strings.TrimSpace(job.SecretChannel.Value) != "" {
		job.LLMRuntimeConfig.SecretChannel = SecretChannelRef{Kind: "env", Name: c.secretEnvName}
	}
	data, err := json.Marshal(job)
	if err != nil {
		return Result{}, err
	}
	if containsForbiddenPayload(string(data), job.SecretChannel.Value) {
		return Result{}, fmt.Errorf("playbot_agent_job_secret_leak")
	}

	tmpDir, err := os.MkdirTemp("", "browserwing-playbot-agent-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmpDir)
	inputPath := filepath.Join(tmpDir, "job.json")
	if err := os.WriteFile(inputPath, data, 0o600); err != nil {
		return Result{}, err
	}

	args := []string{"--mode", job.Mode, "--input", inputPath}
	var eventsPath string
	if opts.EventSink != nil {
		eventsPath = filepath.Join(tmpDir, "events.jsonl")
		args = append(args, "--events", eventsPath)
	}
	cmd := exec.CommandContext(ctx, c.commandPath, args...)
	if c.workingDir != "" {
		cmd.Dir = c.workingDir
	}
	envName := firstNonEmpty(job.SecretChannel.EnvName, c.secretEnvName)
	cmd.Env = buildAgentEnvironment(envName, job.SecretChannel.Value)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if opts.EventSink == nil {
		if err := cmd.Run(); err != nil {
			return Result{}, fmt.Errorf("playbot_agent_process_failed: %s: %s", c.redact(err.Error(), job.SecretChannel.Value), c.redact(stderr.String(), job.SecretChannel.Value))
		}
	} else {
		done := make(chan struct{})
		tailDone := make(chan struct{})
		go func() {
			defer close(tailDone)
			c.tailEvents(ctx, eventsPath, opts.EventSink, done)
		}()
		err := cmd.Run()
		close(done)
		<-tailDone
		if err != nil {
			return Result{}, fmt.Errorf("playbot_agent_process_failed: %s: %s", c.redact(err.Error(), job.SecretChannel.Value), c.redact(stderr.String(), job.SecretChannel.Value))
		}
	}
	var result Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return Result{}, fmt.Errorf("playbot_agent_invalid_stdout: %w", err)
	}
	if err := validateResult(result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (c *BinaryClient) tailEvents(ctx context.Context, path string, sink func(Event), done <-chan struct{}) {
	var offset int64
	var buffer string
	draining := false
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()

	for {
		data, nextOffset, ok := readEventFileChunk(path, offset)
		if ok {
			offset = nextOffset
			buffer = consumeEventLines(buffer+string(data), sink)
		}
		if draining {
			if strings.TrimSpace(buffer) != "" {
				sink(Event{
					SchemaVersion: schemaVersion,
					Phase:         "agent_event_stream_invalid",
					Level:         "warning",
					Message:       "Agent event stream ended with an incomplete JSONL line.",
					CreatedAt:     time.Now().UTC(),
				})
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-done:
			draining = true
		case <-ticker.C:
		}
	}
}

func readEventFileChunk(path string, offset int64) ([]byte, int64, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, offset, false
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, false
	}
	data, err := io.ReadAll(file)
	if err != nil || len(data) == 0 {
		return nil, offset, false
	}
	return data, offset + int64(len(data)), true
}

func consumeEventLines(text string, sink func(Event)) string {
	lines := strings.Split(text, "\n")
	remainder := lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			sink(Event{
				SchemaVersion: schemaVersion,
				Phase:         "agent_event_stream_invalid",
				Level:         "warning",
				Message:       "Agent event stream contained an invalid JSONL event.",
				CreatedAt:     time.Now().UTC(),
			})
			continue
		}
		sink(event)
	}
	return remainder
}

func validateResult(result Result) error {
	if strings.TrimSpace(result.SchemaVersion) != schemaVersion {
		return fmt.Errorf("playbot_result_invalid_schema_version")
	}
	switch strings.TrimSpace(result.Status) {
	case statusSuccess, statusFailed, statusContextRequired:
		return nil
	default:
		return fmt.Errorf("playbot_result_invalid_status")
	}
}

func isPythonPlaybotCommand(commandPath string, engineDir string) bool {
	lowerCommand := strings.ToLower(strings.TrimSpace(commandPath))
	lowerEngine := strings.ToLower(strings.TrimSpace(engineDir))
	return strings.Contains(filepath.Base(lowerCommand), "python") ||
		strings.Contains(lowerCommand, "playbot-engine") ||
		strings.Contains(lowerEngine, "playbot-engine")
}

func buildAgentEnvironment(secretEnvName string, secretValue string) []string {
	env := make([]string, 0, 8)
	seen := map[string]bool{}
	for _, name := range []string{"PATH", "Path", "SystemRoot", "WINDIR", "COMSPEC", "PATHEXT"} {
		if key, value, ok := lookupEnvironment(name); ok {
			appendEnvironmentValue(&env, seen, key, value)
		}
	}
	if strings.TrimSpace(secretValue) != "" {
		appendEnvironmentValue(&env, seen, secretEnvName, secretValue)
	}
	return env
}

func lookupEnvironment(name string) (string, string, bool) {
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(key, name) {
			return key, value, true
		}
	}
	return "", "", false
}

func appendEnvironmentValue(env *[]string, seen map[string]bool, key string, value string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	normalized := strings.ToLower(key)
	if seen[normalized] {
		return
	}
	*env = append(*env, key+"="+value)
	seen[normalized] = true
}

func containsForbiddenPayload(text string, secrets ...string) bool {
	for _, secret := range secrets {
		if strings.TrimSpace(secret) != "" && strings.Contains(text, secret) {
			return true
		}
	}
	var payload any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return containsForbiddenLocalPath(text)
	}
	return containsForbiddenPayloadValue(payload)
}

func containsForbiddenPayloadValue(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if isForbiddenPayloadKey(key) || containsForbiddenPayloadValue(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsForbiddenPayloadValue(child) {
				return true
			}
		}
	case string:
		return containsForbiddenLocalPath(typed)
	}
	return false
}

func isForbiddenPayloadKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	switch normalized {
	case "apikey", "cookie", "cookies", "localstorage", "sessionstorage", "profilepath":
		return true
	default:
		return false
	}
}

func containsForbiddenLocalPath(text string) bool {
	return containsWindowsAbsolutePath(text)
}

func (c *BinaryClient) redact(text string, secrets ...string) string {
	redacted := text
	for _, secret := range secrets {
		if strings.TrimSpace(secret) != "" {
			redacted = strings.ReplaceAll(redacted, secret, "<redacted>")
		}
	}
	if c.redactLocalPath {
		redacted = redactWindowsUserPaths(redacted)
	}
	return redacted
}

func redactWindowsUserPaths(text string) string {
	idx := windowsAbsolutePathIndex(text)
	if idx < 0 {
		return text
	}
	return text[:idx] + "<redacted-local-path>"
}

func containsWindowsAbsolutePath(text string) bool {
	return windowsAbsolutePathIndex(text) >= 0
}

func windowsAbsolutePathIndex(text string) int {
	for idx := 0; idx+2 < len(text); idx++ {
		if !isASCIIAlpha(text[idx]) || text[idx+1] != ':' {
			continue
		}
		if idx > 0 && isASCIIAlphaNumeric(text[idx-1]) {
			continue
		}
		if text[idx+2] == '\\' || text[idx+2] == '/' {
			return idx
		}
	}
	return -1
}

func isASCIIAlphaNumeric(value byte) bool {
	return isASCIIAlpha(value) || (value >= '0' && value <= '9')
}

func isASCIIAlpha(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
