package quality

import "testing"

/*
P4.7.5 contract basis:
- docs/P4_7_5_PLAYBOT_BLUEPRINT_QUALITY_DESIGN.md section 10 defines the
  recording quality error codes and requires machine-readable errors before LLM
  guessing or TestCase creation.
- docs/P4_7_5_PLAYBOT_GO_AGENT_IMPLEMENTATION_PLAN.md section 4.2 assigns these
  checks to playbot-agent/internal/quality.

Current expected red state:
- playbot-agent/internal/quality has no production validator yet, so these tests
  fail with missing symbols.

Targeted verification:
- cd ..\playbot-agent
- go test ./internal/quality -run TestP475 -count=1
*/

func TestP475RecordingQualityErrorsAreMachineReadable(t *testing.T) {
	cases := []struct {
		name  string
		input RecordingQualityInput
		code  string
	}{
		{
			name: "click missing executable target",
			input: RecordingQualityInput{
				Actions:  []RecordedAction{{Type: "click"}},
				Snapshot: DOMSnapshot{Elements: []DOMElement{{Role: "button", Text: "Save"}}},
				Meta:     RecordingMeta{SchemaVersion: 1, RecordingKind: "business_flow", AuthContext: "clean", TargetURL: "https://example.invalid/orders"},
			},
			code: "recording_action_missing_target",
		},
		{
			name: "fill missing value",
			input: RecordingQualityInput{
				Actions:  []RecordedAction{{Type: "fill", Target: RecordedTarget{Placeholder: "Email"}}},
				Snapshot: DOMSnapshot{Elements: []DOMElement{{Role: "textbox", Placeholder: "Email"}}},
				Meta:     RecordingMeta{SchemaVersion: 1, RecordingKind: "business_flow", AuthContext: "clean", TargetURL: "https://example.invalid/orders"},
			},
			code: "recording_action_missing_value",
		},
		{
			name: "navigate missing url",
			input: RecordingQualityInput{
				Actions:  []RecordedAction{{Type: "navigate"}},
				Snapshot: DOMSnapshot{Elements: []DOMElement{{Text: "Orders"}}},
				Meta:     RecordingMeta{SchemaVersion: 1, RecordingKind: "business_flow", AuthContext: "clean"},
			},
			code: "recording_navigation_missing_url",
		},
		{
			name: "snapshot unusable",
			input: RecordingQualityInput{
				Actions: []RecordedAction{{Type: "click", Target: RecordedTarget{RecordedSelector: "button.save"}}},
				Snapshot: DOMSnapshot{
					RawInvalid: true,
				},
				Meta: RecordingMeta{SchemaVersion: 1, RecordingKind: "business_flow", AuthContext: "clean", TargetURL: "https://example.invalid/orders"},
			},
			code: "recording_snapshot_unusable",
		},
		{
			name: "recording meta invalid",
			input: RecordingQualityInput{
				Actions:  []RecordedAction{{Type: "click", Target: RecordedTarget{Text: "Save"}}},
				Snapshot: DOMSnapshot{Elements: []DOMElement{{Role: "button", Text: "Save"}}},
				Meta:     RecordingMeta{SchemaVersion: 1, RecordingKind: "exploratory_flow", AuthContext: "clean"},
			},
			code: "recording_meta_invalid",
		},
		{
			name: "auth context conflict",
			input: RecordingQualityInput{
				Actions:            []RecordedAction{{Type: "click", Target: RecordedTarget{Text: "Save"}}},
				Snapshot:           DOMSnapshot{Elements: []DOMElement{{Role: "button", Text: "Save"}}},
				Meta:               RecordingMeta{SchemaVersion: 1, RecordingKind: "business_flow", AuthContext: "project_saved", TargetURL: "https://example.invalid/orders"},
				RequestedAuthScope: "clean",
			},
			code: "recording_auth_context_conflict",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diagnostics := ValidateRecordingQuality(tc.input)
			if !diagnostics.HasCode(tc.code) {
				t.Fatalf("diagnostics missing %s: %+v", tc.code, diagnostics)
			}
			if diagnostics.ContainsSecretMaterial() {
				t.Fatalf("diagnostics leaked secret material: %+v", diagnostics)
			}
		})
	}
}
