package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/browserwing/browserwing/models"
)

const (
	recordingNormalizerVersion         = "p4.7.6"
	recordingSourceSchemaVersion       = "p4.7.6"
	recordingDraftCompletenessVersion  = 1
	requestCanonicalizerVersion        = "p4.7.6.1"
	maxRecordingNormalizedPayloadBytes = 2 << 20
	sensitiveInputPolicyVersion        = 1
	sensitiveInputPlaceholder          = "{{REDACTED_SECRET}}"
)

// RecordingNormalizer is the sole security and semantic seam between an
// untrusted recorder payload and durable PageScript/Playbot input. The
// persisted view is already secret-safe; recording_source is derived from the
// same normalized value and never from the raw session draft.
type RecordingNormalizer struct{}

type normalizedRecording struct {
	ActionsJSON           string
	ActionCount           int
	DOMSnapshot           string
	RecordingMetaJSON     string
	DraftHash             string
	PageScriptContentHash string
	RecordingSource       map[string]any
	RecordingSourceHash   string
	NormalizerVersion     string
}

type normalizedRecordingArtifacts struct {
	Artifacts []models.RecordingArtifact
	Dropped   int
}

func NewRecordingNormalizer() *RecordingNormalizer { return &RecordingNormalizer{} }

func (n *RecordingNormalizer) NormalizeSync(rawActions, rawDOM json.RawMessage, session models.RecordingSession) (normalizedRecording, error) {
	if len(rawActions) == 0 || strings.TrimSpace(string(rawActions)) == "" || strings.TrimSpace(string(rawActions)) == "null" {
		return normalizedRecording{}, fmt.Errorf("recording_actions_invalid")
	}
	actionsJSON, actionCount, err := normalizeRecordingActionTrace(rawActions, false)
	if err != nil {
		return normalizedRecording{}, fmt.Errorf("recording_actions_invalid")
	}
	var actions []map[string]any
	if err := json.Unmarshal([]byte(actionsJSON), &actions); err != nil {
		return normalizedRecording{}, fmt.Errorf("recording_actions_invalid")
	}
	for _, action := range actions {
		if strings.TrimSpace(stringFromAny(action["type"])) == "" {
			return normalizedRecording{}, fmt.Errorf("recording_actions_invalid")
		}
	}
	if len(rawDOM) == 0 || strings.TrimSpace(string(rawDOM)) == "" || strings.TrimSpace(string(rawDOM)) == "null" {
		rawDOM = json.RawMessage(session.DOMSnapshot)
	}
	domJSON, err := normalizeRecordingJSONObject(rawDOM, true)
	if err != nil {
		return normalizedRecording{}, err
	}
	metaJSON, err := normalizeSessionRecordingMeta(session, nil)
	if err != nil {
		return normalizedRecording{}, err
	}
	return n.normalized(actionsJSON, actionCount, domJSON, metaJSON)
}

func (n *RecordingNormalizer) NormalizeFinal(session models.RecordingSession, rawMeta json.RawMessage) (normalizedRecording, error) {
	if strings.TrimSpace(session.ActionsJSON) == "" {
		return normalizedRecording{}, fmt.Errorf("recording_actions_invalid")
	}
	// Older persisted drafts used an envelope such as {"actions":[...]}. New
	// Sync requests must stay strict, but finalization is also the compatibility
	// seam for those durable drafts.
	actionsJSON, actionCount, err := normalizeRecordingActionTrace(json.RawMessage(session.ActionsJSON), true)
	if err != nil {
		return normalizedRecording{}, fmt.Errorf("recording_actions_invalid")
	}
	domJSON, err := normalizeRecordingJSONObject(json.RawMessage(session.DOMSnapshot), true)
	if err != nil {
		return normalizedRecording{}, err
	}
	metaJSON, err := normalizeSessionRecordingMeta(session, rawMeta)
	if err != nil {
		return normalizedRecording{}, err
	}
	return n.normalized(actionsJSON, actionCount, domJSON, metaJSON)
}

func (n *RecordingNormalizer) NormalizePageScript(script models.PageScript) (normalizedRecording, error) {
	if strings.TrimSpace(script.ActionTrace) == "" {
		return normalizedRecording{}, fmt.Errorf("recording_actions_invalid")
	}
	// Historical PageScript rows used both `actions` and `steps` envelopes.
	// Normalize them here instead of allowing generation to read the raw row.
	actionsJSON, actionCount, err := normalizeRecordingActionTrace(json.RawMessage(script.ActionTrace), true)
	if err != nil {
		return normalizedRecording{}, fmt.Errorf("recording_actions_invalid")
	}
	domJSON, err := normalizeRecordingJSONObject(json.RawMessage(script.DOMSnapshot), true)
	if err != nil {
		return normalizedRecording{}, err
	}
	metaJSON, err := normalizeRecordingMetaJSON(script.RecordingMetaJSON, nil)
	if err != nil {
		return normalizedRecording{}, err
	}
	return n.normalized(actionsJSON, actionCount, domJSON, metaJSON)
}

// normalizeSessionRecordingMeta is the session-specific recording-meta seam.
// A durable session owns the auth-state identity, so caller supplied or stale
// draft metadata cannot redirect a final PageScript to another auth state.
func normalizeSessionRecordingMeta(session models.RecordingSession, override json.RawMessage) (string, error) {
	raw := strings.TrimSpace(session.RecordingMetaJSON)
	if len(override) > 0 && strings.TrimSpace(string(override)) != "null" {
		raw = string(override)
	}
	// Standalone historical scripts may not have a RecordingSession identity.
	// A real session, however, is authoritative for the immutable recording
	// source.  Save must not turn a clean login recording into a saved-auth
	// business flow merely by supplying a different recording_meta payload.
	if session.RecordingKind == "" && session.AuthContext == "" && session.TargetURL == "" && session.SourceAuthStateID == nil {
		if raw == "" {
			return "", fmt.Errorf("recording_source_invalid")
		}
		return normalizeRecordingMetaJSON(raw, nil)
	}
	if raw == "" {
		data, err := json.Marshal(p45RecordingMeta{
			SchemaVersion: 1,
			RecordingKind: session.RecordingKind,
			AuthContext:   session.AuthContext,
			AuthStateID:   session.SourceAuthStateID,
			TargetURL:     session.TargetURL,
		})
		if err != nil {
			return "", fmt.Errorf("recording_source_invalid")
		}
		raw = string(data)
	}
	var supplied p45RecordingMeta
	if err := json.Unmarshal([]byte(raw), &supplied); err != nil || validateRecordingMeta(supplied, false) != nil {
		return "", fmt.Errorf("recording_source_invalid")
	}
	if supplied.RecordingKind != session.RecordingKind || supplied.AuthContext != session.AuthContext || supplied.TargetURL != session.TargetURL || !sameRecordingMetaAuthStateID(supplied.AuthStateID, session.SourceAuthStateID) {
		return "", fmt.Errorf("recording_source_invalid")
	}
	// Keep only mutable timing metadata from a caller supplied payload.  Every
	// source-defining field is serialized from the durable session itself.
	expected := p45RecordingMeta{
		SchemaVersion: 1, RecordingKind: session.RecordingKind, AuthContext: session.AuthContext,
		AuthStateID: session.SourceAuthStateID, TargetURL: session.TargetURL,
		StartedAt: supplied.StartedAt, EndedAt: supplied.EndedAt,
	}
	if err := validateRecordingMeta(expected, false); err != nil {
		return "", fmt.Errorf("recording_source_invalid")
	}
	data, err := json.Marshal(expected)
	if err != nil {
		return "", fmt.Errorf("recording_source_invalid")
	}
	return normalizeRecordingMetaJSON(string(data), session.SourceAuthStateID)
}

func sameRecordingMetaAuthStateID(left, right *uint) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// normalizeRecordingMetaJSON validates the full P4.5 recording-meta contract
// before it can reach PageScript or Playbot, and marshals it back into one
// stable form for hash generation.
func normalizeRecordingMetaJSON(raw string, forcedAuthStateID *uint) (string, error) {
	var meta p45RecordingMeta
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &meta); err != nil || validateRecordingMeta(meta, false) != nil {
		return "", fmt.Errorf("recording_source_invalid")
	}
	if forcedAuthStateID != nil {
		meta.AuthStateID = forcedAuthStateID
	}
	cleanTargetURL, keep := sanitizeRecordingURL(meta.TargetURL)
	if !keep {
		return "", fmt.Errorf("recording_source_invalid")
	}
	meta.TargetURL = cleanTargetURL
	data, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("recording_source_invalid")
	}
	return string(data), nil
}

// NormalizeArtifacts is the only runtime-artifact admission path. Artifacts
// are never allowed to feed back into the recording draft: a stale final
// receipt can contribute a validated download record, but cannot modify the
// ActionTrace, DOM snapshot, meta, or their revisions.
func (n *RecordingNormalizer) NormalizeArtifacts(session models.RecordingSession, raw any, receiptID string) normalizedRecordingArtifacts {
	items := anySlice(raw)
	result := normalizedRecordingArtifacts{Artifacts: make([]models.RecordingArtifact, 0, len(items))}
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok || strings.TrimSpace(receiptID) == "" {
			result.Dropped++
			continue
		}
		if strings.TrimSpace(stringFromAny(obj["artifact_type"])) != "download" || strings.TrimSpace(stringFromAny(obj["storage_backend"])) != "local" {
			result.Dropped++
			continue
		}
		storagePath := filepath.Clean(strings.TrimSpace(stringFromAny(obj["storage_path"])))
		if storagePath == "." || storagePath == ".." || filepath.IsAbs(storagePath) || strings.HasPrefix(storagePath, ".."+string(filepath.Separator)) {
			result.Dropped++
			continue
		}
		fileName := filepath.Base(strings.TrimSpace(stringFromAny(obj["file_name"])))
		mimeType := strings.TrimSpace(stringFromAny(obj["mime_type"]))
		size := int64(0)
		switch value := obj["size_bytes"].(type) {
		case int:
			size = int64(value)
		case int64:
			size = value
		case float64:
			size = int64(value)
		}
		if size < 0 || p475ForbiddenString(storagePath) || p475ForbiddenString(fileName) {
			result.Dropped++
			continue
		}
		fingerprint := hashRecordingParts("download", "local", storagePath, fileName, mimeType, fmt.Sprint(size))
		result.Artifacts = append(result.Artifacts, models.RecordingArtifact{
			ProjectID:           session.ProjectID,
			VersionID:           session.VersionID,
			PageID:              session.PageID,
			RecordingSessionID:  session.ID,
			ArtifactType:        "download",
			StorageBackend:      "local",
			StoragePath:         storagePath,
			FileName:            fileName,
			MimeType:            mimeType,
			SizeBytes:           size,
			Sensitive:           true,
			SourceReceiptID:     strings.TrimSpace(receiptID),
			ArtifactFingerprint: fingerprint,
			CreatedAt:           time.Now().UTC(),
		})
	}
	return result
}

func normalizeRecordingActionTrace(raw json.RawMessage, allowLegacyEnvelope bool) (string, int, error) {
	if actionsJSON, actionCount, err := models.NormalizeRecordingActionsJSON(raw); err == nil {
		return actionsJSON, actionCount, nil
	} else if !allowLegacyEnvelope {
		return "", 0, err
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", 0, err
	}
	for _, key := range []string{"actions", "steps"} {
		if actions, ok := envelope[key]; ok {
			return models.NormalizeRecordingActionsJSON(actions)
		}
	}
	return "", 0, fmt.Errorf("recording action trace is not an array or supported legacy envelope")
}

func (n *RecordingNormalizer) normalized(actionsJSON string, actionCount int, domJSON, metaJSON string) (normalizedRecording, error) {
	if len(actionsJSON)+len(domJSON)+len(metaJSON) > maxRecordingNormalizedPayloadBytes {
		return normalizedRecording{}, fmt.Errorf("recording_source_invalid")
	}
	var actions []map[string]any
	var dom map[string]any
	var meta map[string]any
	if err := json.Unmarshal([]byte(actionsJSON), &actions); err != nil {
		return normalizedRecording{}, fmt.Errorf("recording_actions_invalid")
	}
	if err := json.Unmarshal([]byte(domJSON), &dom); err != nil {
		return normalizedRecording{}, fmt.Errorf("recording_source_invalid")
	}
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		return normalizedRecording{}, fmt.Errorf("recording_source_invalid")
	}
	safeActions := sanitizeRecordingActions(actions)
	safeDOM := sanitizeRecordingObject(dom)
	safeMeta := sanitizeRecordingObject(meta)
	actionsData, _ := json.Marshal(safeActions)
	domData, _ := json.Marshal(safeDOM)
	metaData, _ := json.Marshal(safeMeta)
	draftFingerprint, err := models.RecordingDraftFingerprintV1(actionsData, domData)
	if err != nil {
		return normalizedRecording{}, fmt.Errorf("recording_source_invalid")
	}
	pageScriptHash := hashRecordingParts(string(actionsData), string(domData), string(metaData))
	source := map[string]any{
		"schema_version": recordingSourceSchemaVersion,
		"action_trace":   safeActions,
		"dom_snapshot":   safeDOM,
		"recording_meta": safeMeta,
	}
	sourceData, _ := json.Marshal(source)
	return normalizedRecording{
		ActionsJSON:           string(actionsData),
		ActionCount:           len(safeActions),
		DOMSnapshot:           string(domData),
		RecordingMetaJSON:     string(metaData),
		DraftHash:             hashRecordingParts(draftFingerprint, string(metaData)),
		PageScriptContentHash: pageScriptHash,
		RecordingSource:       source,
		RecordingSourceHash:   hashRecordingParts(string(sourceData)),
		NormalizerVersion:     recordingNormalizerVersion,
	}, nil
}

func normalizeRecordingJSONObject(raw json.RawMessage, allowUnavailable bool) (string, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" {
		if allowUnavailable {
			return `{"unavailable":true}`, nil
		}
		return "", fmt.Errorf("recording_source_invalid")
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("recording_source_invalid")
	}
	if value == nil {
		return "", fmt.Errorf("recording_source_invalid")
	}
	data, err := json.Marshal(sanitizeRecordingObject(value))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func sanitizeRecordingActions(actions []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(actions))
	for _, action := range actions {
		normalized, keep := models.NormalizeRecordingActionMap(action)
		if !keep {
			continue
		}
		if isSensitiveRecordingInput(normalized) {
			normalized["sensitive_input"] = true
			normalized["sensitive_input_policy_version"] = sensitiveInputPolicyVersion
			redactSensitiveActionValues(normalized)
			redactSensitiveActionSemantics(normalized)
		}
		if clean, ok := sanitizeRecordingValue(normalized).(map[string]any); ok {
			out = append(out, clean)
		}
	}
	return out
}

func sanitizeRecordingObject(value map[string]any) map[string]any {
	if clean, ok := sanitizeRecordingValue(value).(map[string]any); ok {
		return clean
	}
	return map[string]any{}
}

func sanitizeRecordingValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if recordingForbiddenKey(key) {
				continue
			}
			if recordingURLKey(key) {
				if rawURL, ok := item.(string); ok {
					if cleanURL, keep := sanitizeRecordingURL(rawURL); keep {
						out[key] = cleanURL
					}
					continue
				}
			}
			if sanitized := sanitizeRecordingValue(item); sanitized != nil {
				out[key] = sanitized
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			if sanitized := sanitizeRecordingValue(item); sanitized != nil {
				out = append(out, sanitized)
			}
		}
		return out
	case string:
		if p475ForbiddenString(typed) || strings.HasPrefix(strings.ToLower(strings.TrimSpace(typed)), "data:") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(typed)), "blob:") {
			return nil
		}
		return typed
	default:
		return value
	}
}

func recordingURLKey(key string) bool {
	switch normalizeSensitiveInputHint(key) {
	case "url", "href", "src", "downloadurl", "targeturl", "sourceurl", "locationurl", "locationhref", "navigateurl", "navigationurl":
		return true
	default:
		return false
	}
}

// sanitizeRecordingURL is the common URL boundary for action traces, DOM
// snapshots, and recording metadata. Download URLs have extra storage rules,
// but credentials must never rely on the download-only path to be removed.
func sanitizeRecordingURL(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", true
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "blob:") || p475ForbiddenString(trimmed) {
		return "", false
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", false
	}
	parsed.User = nil
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		if recordingSensitiveURLQueryKey(key) {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), true
}

func recordingSensitiveURLQueryKey(key string) bool {
	normalized := normalizeSensitiveInputHint(key)
	for _, token := range []string{"token", "accesstoken", "idtoken", "refreshtoken", "signature", "sig", "authorization", "apikey", "password", "secret", "credential", "session"} {
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

func recordingForbiddenKey(key string) bool {
	lower := strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	for _, token := range []string{"cookie", "localstorage", "sessionstorage", "password", "passwd", "token", "authorization", "apikey", "secret", "profilepath", "filepath", "binary", "screenshotcontent", "downloadcontent"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func isSensitiveRecordingInput(action map[string]any) bool {
	if !strings.EqualFold(strings.TrimSpace(stringFromAny(action["type"])), "input") {
		return false
	}
	if sensitive, ok := action["sensitive_input"].(bool); ok && sensitive {
		return true
	}
	attrs := mapFromAny(action["attrs"])
	inputType := strings.ToLower(strings.TrimSpace(stringFromAny(action["input_type"])))
	if inputType == "" {
		inputType = strings.ToLower(strings.TrimSpace(stringFromAny(attrs["type"])))
	}
	if inputType == "password" {
		return true
	}
	autocomplete := normalizeSensitiveInputHint(firstRecordingHint(action, attrs, "autocomplete"))
	switch autocomplete {
	case "currentpassword", "newpassword", "onetimecode", "ccnumber", "cccsc", "ccexp", "ccexpmonth", "ccexpyear":
		return true
	}
	hints := []string{
		firstRecordingHint(action, attrs, "name"),
		firstRecordingHint(action, attrs, "id"),
		firstRecordingHint(action, attrs, "aria-label", "aria_label"),
		firstRecordingHint(action, attrs, "placeholder"),
		firstRecordingHint(mapFromAny(action["accessibility"]), nil, "name"),
		firstRecordingHint(mapFromAny(action["context"]), nil, "form_hint"),
	}
	for _, hint := range hints {
		if sensitiveInputHintMatches(hint) {
			return true
		}
	}
	return false
}

func mapFromAny(value any) map[string]any {
	mapValue, _ := value.(map[string]any)
	return mapValue
}

func firstRecordingHint(action, attrs map[string]any, keys ...string) string {
	for _, key := range keys {
		if action != nil {
			if value := strings.TrimSpace(stringFromAny(action[key])); value != "" {
				return value
			}
		}
		if attrs != nil {
			if value := strings.TrimSpace(stringFromAny(attrs[key])); value != "" {
				return value
			}
		}
	}
	return ""
}

func normalizeSensitiveInputHint(value string) string {
	var normalized strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func sensitiveInputHintMatches(value string) bool {
	normalized := normalizeSensitiveInputHint(value)
	if normalized == "" {
		return false
	}
	for _, token := range []string{
		"password", "passwd", "pwd", "secret", "token", "apikey", "authorization", "authcode", "otp", "onetimecode", "verificationcode",
		"密码", "口令", "验证码", "动态码", "校验码", "卡号", "银行卡", "安全码", "cvv", "cvc",
	} {
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

func redactSensitiveActionValues(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if isSensitiveActionValueKey(key) {
				typed[key] = sensitiveInputPlaceholder
				continue
			}
			redactSensitiveActionValues(item)
		}
	case []any:
		for _, item := range typed {
			redactSensitiveActionValues(item)
		}
	}
}

// redactSensitiveActionSemantics closes the Recorder-independent defence in
// depth path.  A malformed or old runtime payload may have copied a secret
// into an accessible name or inferred intent object instead of a value field.
func redactSensitiveActionSemantics(action map[string]any) {
	if accessibility := mapFromAny(action["accessibility"]); accessibility != nil {
		accessibility["name"] = "sensitive input"
		accessibility["value"] = sensitiveInputPlaceholder
	}
	if intent := mapFromAny(action["intent"]); intent != nil {
		intent["object"] = "sensitive input"
	}
}

func isSensitiveActionValueKey(key string) bool {
	switch normalizeSensitiveInputHint(key) {
	case "value", "inputvalue", "defaultvalue", "currentvalue", "typedvalue":
		return true
	default:
		return false
	}
}

func hashRecordingParts(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func IsRecoverableRecordingDraft(session models.RecordingSession) bool {
	if session.SyncRevision == 0 || strings.TrimSpace(session.SyncPayloadHash) == "" || strings.TrimSpace(session.DraftHash) == "" {
		return false
	}
	if session.DraftCompletenessVersion != recordingDraftCompletenessVersion {
		return false
	}
	if strings.TrimSpace(session.BrowserInstanceID) == "" || strings.TrimSpace(session.RuntimePageID) == "" || strings.TrimSpace(session.RuntimeGeneration) == "" {
		return false
	}
	if strings.TrimSpace(session.ActionsJSON) == "" || strings.TrimSpace(session.RecordingMetaJSON) == "" || strings.TrimSpace(session.DOMSnapshot) == "" {
		return false
	}
	if len(session.ActionsJSON)+len(session.DOMSnapshot)+len(session.RecordingMetaJSON) > maxRecordingNormalizedPayloadBytes {
		return false
	}
	if !isRecoverableSemanticDOMSnapshot(session.DOMSnapshot) {
		return false
	}
	_, err := NewRecordingNormalizer().NormalizeFinal(session, nil)
	return err == nil
}

// isRecoverableSemanticDOMSnapshot is intentionally stricter than the
// historical PageScript compatibility parser. Runtime-loss recovery may only
// promote a draft when its DOM was produced by the bounded Recorder schema, or
// when Recorder explicitly recorded that a semantic snapshot was unavailable.
// Arbitrary JSON objects (including {}) are not evidence that a runtime draft
// is complete enough to stop safely.
func isRecoverableSemanticDOMSnapshot(raw string) bool {
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil || snapshot == nil {
		return false
	}
	if unavailable, ok := snapshot["unavailable"].(bool); ok && unavailable {
		return true
	}
	if strings.TrimSpace(fmt.Sprint(snapshot["kind"])) != "semantic_dom_snapshot" {
		return false
	}
	if strings.TrimSpace(stringFromAny(snapshot["url"])) == "" {
		return false
	}
	if _, ok := snapshot["title"].(string); !ok {
		return false
	}
	if _, ok := snapshot["elements"].([]any); !ok {
		return false
	}
	switch version := snapshot["schema_version"].(type) {
	case float64:
		return version == 1
	case json.Number:
		return version.String() == "1"
	case int:
		return version == 1
	case int64:
		return version == 1
	default:
		return false
	}
}
