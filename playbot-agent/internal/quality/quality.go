package quality

import "strings"

type RecordingQualityInput struct {
	Actions            []RecordedAction
	Snapshot           DOMSnapshot
	Meta               RecordingMeta
	RequestedAuthScope string
}

type RecordedAction struct {
	Type   string
	Target RecordedTarget
	Value  string
	URL    string
}

type RecordedTarget struct {
	Role             string
	Text             string
	Placeholder      string
	Label            string
	Selector         string
	RecordedSelector string
	RefID            string
}

type DOMSnapshot struct {
	Elements   []DOMElement
	RawInvalid bool
}

type DOMElement struct {
	Role             string
	Text             string
	Placeholder      string
	RecordedSelector string
	RefID            string
}

type RecordingMeta struct {
	SchemaVersion int
	RecordingKind string
	AuthContext   string
	TargetURL     string
}

type Diagnostic struct {
	Code      string `json:"code"`
	Message   string `json:"message,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

type Diagnostics struct {
	Items []Diagnostic `json:"items"`
}

func ValidateRecordingQuality(input RecordingQualityInput) Diagnostics {
	var diagnostics Diagnostics
	if !validRecordingMeta(input.Meta) {
		diagnostics.add("recording_meta_invalid")
	}
	if requested := strings.TrimSpace(input.RequestedAuthScope); requested != "" && strings.TrimSpace(input.Meta.AuthContext) != "" && requested != strings.TrimSpace(input.Meta.AuthContext) {
		diagnostics.add("recording_auth_context_conflict")
	}
	if input.Snapshot.RawInvalid || len(input.Snapshot.Elements) == 0 {
		diagnostics.add("recording_snapshot_unusable")
	}

	for _, action := range input.Actions {
		actionType := strings.TrimSpace(action.Type)
		switch actionType {
		case "navigate":
			if strings.TrimSpace(action.URL) == "" && strings.TrimSpace(input.Meta.TargetURL) == "" {
				diagnostics.add("recording_navigation_missing_url")
			}
		case "fill", "input", "select", "expect_text":
			if !hasExecutableTarget(action.Target) {
				diagnostics.add("recording_action_missing_target")
				continue
			}
			if strings.TrimSpace(action.Value) == "" {
				diagnostics.add("recording_action_missing_value")
			}
		case "click", "expect_visible", "wait":
			if !hasExecutableTarget(action.Target) {
				diagnostics.add("recording_action_missing_target")
			}
		}
	}
	return diagnostics
}

func (d *Diagnostics) add(code string) {
	if d.HasCode(code) {
		return
	}
	d.Items = append(d.Items, Diagnostic{Code: code, Message: code})
}

func (d Diagnostics) HasCode(code string) bool {
	for _, item := range d.Items {
		if item.Code == code {
			return true
		}
	}
	return false
}

func (d Diagnostics) ContainsSecretMaterial() bool {
	for _, item := range d.Items {
		text := item.Code + item.Message
		if strings.Contains(text, "sk-") || strings.Contains(strings.ToLower(text), "cookie") || strings.Contains(strings.ToLower(text), "localstorage") || strings.Contains(text, `C:\Users\`) {
			return true
		}
	}
	return false
}

func (d Diagnostics) FirstCode() string {
	if len(d.Items) == 0 {
		return ""
	}
	return d.Items[0].Code
}

func hasExecutableTarget(target RecordedTarget) bool {
	return strings.TrimSpace(target.RefID) != "" ||
		strings.TrimSpace(target.RecordedSelector) != "" ||
		strings.TrimSpace(target.Selector) != "" ||
		(strings.TrimSpace(target.Role) != "" && strings.TrimSpace(target.Text) != "") ||
		strings.TrimSpace(target.Text) != "" ||
		strings.TrimSpace(target.Label) != "" ||
		strings.TrimSpace(target.Placeholder) != ""
}

func validRecordingMeta(meta RecordingMeta) bool {
	kind := strings.TrimSpace(meta.RecordingKind)
	authContext := strings.TrimSpace(meta.AuthContext)
	if meta.SchemaVersion != 1 || !validRecordingKind(kind) || !validAuthContext(authContext) {
		return false
	}
	if kind == "login_flow" && authContext != "clean" {
		return false
	}
	return true
}

func validRecordingKind(value string) bool {
	switch strings.TrimSpace(value) {
	case "login_flow", "business_flow":
		return true
	default:
		return false
	}
}

func validAuthContext(value string) bool {
	switch strings.TrimSpace(value) {
	case "clean", "project_saved":
		return true
	default:
		return false
	}
}
