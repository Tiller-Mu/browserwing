package models

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

const maxSanitizedRecordingDownloadURLLength = 2048

// SanitizeRecordingDownloadURL keeps only an analysis-safe HTTP(S) download
// URL. It deliberately runs server-side because recording-page JavaScript is
// untrusted input and can be replaced by the page being recorded.
func SanitizeRecordingDownloadURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || parsed.Host == "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	parsed.Scheme = scheme
	parsed.User = nil
	parsed.Fragment = ""
	parsed.RawFragment = ""
	if query, err := url.ParseQuery(parsed.RawQuery); err == nil {
		for key, values := range query {
			if !isSensitiveRecordingDownloadQueryKey(key) {
				continue
			}
			for index := range values {
				values[index] = "REDACTED"
			}
			query[key] = values
		}
		parsed.RawQuery = query.Encode()
	} else {
		parsed.RawQuery = ""
	}
	parsed.ForceQuery = false
	sanitized := parsed.String()
	if len(sanitized) <= maxSanitizedRecordingDownloadURLLength {
		return sanitized
	}
	parsed.RawQuery = ""
	return parsed.String()
}

// NormalizeRecordingActions sanitizes download metadata from recorder output
// before it can be persisted, displayed, or sent to Playbot. Invalid download
// analysis actions are omitted; ordinary actions keep their behavior while
// losing invalid download-link attributes.
func NormalizeRecordingActions(actions []ScriptAction) []ScriptAction {
	normalized := make([]ScriptAction, 0, len(actions))
	for _, action := range actions {
		if isDownloadRecordingAction(action.Type) {
			action.URL = SanitizeRecordingDownloadURL(action.URL)
			if action.URL == "" {
				continue
			}
		}
		action.Attrs = normalizeRecordingDownloadAttrs(action.Attrs)
		normalized = append(normalized, action)
	}
	return normalized
}

// NormalizeRecordingActionsJSON applies the same boundary to externally
// supplied session-sync JSON without discarding unrelated action fields.
func NormalizeRecordingActionsJSON(raw []byte) (string, int, error) {
	var actions []map[string]any
	if err := json.Unmarshal(raw, &actions); err != nil {
		return "", 0, err
	}
	normalized := make([]map[string]any, 0, len(actions))
	for _, action := range actions {
		if strings.TrimSpace(stringFromRecordingActionMap(action["type"])) == "" {
			return "", 0, fmt.Errorf("recording action type is required")
		}
		if action, keep := NormalizeRecordingActionMap(action); keep {
			normalized = append(normalized, action)
		}
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", 0, err
	}
	return string(data), len(normalized), nil
}

// NormalizeRecordingActionMap is the map equivalent used by read-side action
// sanitizers. It returns false only for an invalid download analysis action.
func NormalizeRecordingActionMap(action map[string]any) (map[string]any, bool) {
	if action == nil {
		return nil, false
	}
	normalized := make(map[string]any, len(action))
	for key, value := range action {
		normalized[key] = value
	}
	if isDownloadRecordingAction(stringFromRecordingActionMap(normalized["type"])) {
		sanitizedURL := SanitizeRecordingDownloadURL(stringFromRecordingActionMap(normalized["url"]))
		if sanitizedURL == "" {
			return nil, false
		}
		normalized["url"] = sanitizedURL
	}
	if attrs, ok := normalized["attrs"].(map[string]any); ok {
		attrs = normalizeRecordingDownloadAttrsMap(attrs)
		if len(attrs) == 0 {
			delete(normalized, "attrs")
		} else {
			normalized["attrs"] = attrs
		}
	}
	canonicalizeRecordingActionZeroValues(normalized)
	return normalized, true
}

// canonicalizeRecordingActionZeroValues makes sparse Sync JSON and the JSON
// produced by marshaling ScriptAction compare as the same action. ScriptAction
// predates omitempty tags on several scalar fields, so Recorder emits their Go
// zero values while browser JSON commonly omits them. Only declared action
// fields are collapsed; unknown non-zero extension fields remain part of the
// semantic recording identity.
func canonicalizeRecordingActionZeroValues(action map[string]any) {
	for _, key := range []string{
		"selector", "xpath", "value", "url", "text", "tag_name",
		"key", "extract_type", "attribute_name", "js_code", "variable_name", "extracted_data",
		"description", "accept", "remark", "method", "xhr_id",
		"screenshot_mode", "ai_control_prompt", "ai_control_xpath", "ai_control_llm_config_id",
	} {
		if value, ok := action[key]; ok && recordingActionEmptyString(value) {
			delete(action, key)
		}
	}
	for _, key := range []string{
		"timestamp", "sensitive_input_policy_version", "duration", "x", "y",
		"scroll_x", "scroll_y", "status", "screenshot_width", "screenshot_height",
	} {
		if value, ok := action[key]; ok && recordingActionZeroNumber(value) {
			delete(action, key)
		}
	}
	for _, key := range []string{"sensitive_input", "multiple"} {
		if value, ok := action[key]; ok && recordingActionFalse(value) {
			delete(action, key)
		}
	}
	for _, key := range []string{"attrs", "file_paths", "file_names"} {
		if value, ok := action[key]; ok && recordingActionEmptyCollection(value) {
			delete(action, key)
		}
	}
	for _, key := range []string{"condition", "intent", "accessibility", "context", "evidence"} {
		if value, ok := action[key]; ok && value == nil {
			delete(action, key)
		}
	}
}

func recordingActionEmptyString(value any) bool {
	text, ok := value.(string)
	return ok && text == ""
}

func recordingActionZeroNumber(value any) bool {
	number, ok := value.(float64)
	return ok && number == 0
}

func recordingActionFalse(value any) bool {
	flag, ok := value.(bool)
	return ok && !flag
}

func recordingActionEmptyCollection(value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func normalizeRecordingDownloadAttrs(attrs map[string]string) map[string]string {
	if len(attrs) == 0 {
		return attrs
	}
	rawURL, hasDownloadURL := attrs["download_url"]
	if !hasDownloadURL {
		return attrs
	}
	normalized := make(map[string]string, len(attrs))
	for key, value := range attrs {
		normalized[key] = value
	}
	if sanitizedURL := SanitizeRecordingDownloadURL(rawURL); sanitizedURL != "" {
		normalized["download_url"] = sanitizedURL
		return normalized
	}
	delete(normalized, "download_url")
	delete(normalized, "download_filename_hint")
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeRecordingDownloadAttrsMap(attrs map[string]any) map[string]any {
	rawURL, hasDownloadURL := attrs["download_url"]
	if !hasDownloadURL {
		return attrs
	}
	normalized := make(map[string]any, len(attrs))
	for key, value := range attrs {
		normalized[key] = value
	}
	if sanitizedURL := SanitizeRecordingDownloadURL(stringFromRecordingActionMap(rawURL)); sanitizedURL != "" {
		normalized["download_url"] = sanitizedURL
		return normalized
	}
	delete(normalized, "download_url")
	delete(normalized, "download_filename_hint")
	return normalized
}

func isDownloadRecordingAction(actionType string) bool {
	return strings.EqualFold(strings.TrimSpace(actionType), "download")
}

func stringFromRecordingActionMap(value any) string {
	text, _ := value.(string)
	return text
}

func isSensitiveRecordingDownloadQueryKey(raw string) bool {
	words := recordingDownloadQueryKeyWords(raw)
	if len(words) >= 2 && words[0] == "x" && (words[1] == "amz" || words[1] == "goog") {
		return true
	}
	// All-lowercase query keys have no casing boundary for the tokenizer. Keep
	// the compact aliases explicit so credential-shaped names are redacted
	// without treating unrelated words such as "keynote" as secrets.
	switch strings.Join(words, "") {
	case "apikey", "apitoken", "accesskey", "accesskeyid", "secretkey", "sessiontoken", "sessionkey", "authtoken", "authorizationtoken", "awsaccesskeyid", "awssecretaccesskey", "awssecuritytoken":
		return true
	}
	for _, word := range words {
		switch word {
		case "token", "auth", "authorization", "secret", "signature", "sig", "credential", "credentials", "key", "password", "passwd", "session":
			return true
		}
	}
	return false
}

// recordingDownloadQueryKeyWords normalizes separator-delimited, camelCase,
// and acronym-prefixed query keys into words before applying the credential
// boundary. This keeps accessKey and AWSAccessKeyId equivalent to access_key.
func recordingDownloadQueryKeyWords(raw string) []string {
	runes := []rune(strings.TrimSpace(raw))
	words := make([]string, 0, 4)
	current := make([]rune, 0, len(runes))
	flush := func() {
		if len(current) == 0 {
			return
		}
		words = append(words, string(current))
		current = current[:0]
	}
	for index, character := range runes {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			flush()
			continue
		}
		if unicode.IsUpper(character) && len(current) > 0 {
			previous := runes[index-1]
			nextIsLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
			if unicode.IsLower(previous) || unicode.IsDigit(previous) || (unicode.IsUpper(previous) && nextIsLower) {
				flush()
			}
		}
		current = append(current, unicode.ToLower(character))
	}
	flush()
	return words
}
