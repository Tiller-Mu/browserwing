package compiler

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

/*
P4.7.5 contract basis:
- docs/P4_7_5_PLAYBOT_BLUEPRINT_QUALITY_DESIGN.md sections 11 and 13 require the
  compiler to output Go runner executable Blueprint fields: navigate.url,
  fill/select/expect_text.value, final target, and description.
- docs/P4_7_5_PLAYBOT_GO_AGENT_IMPLEMENTATION_PLAN.md section 4.2 assigns the
  compiler fixture tests to playbot-agent/internal/compiler.

Current expected red state:
- playbot-agent/internal/compiler has no production compiler yet, so these tests
  fail with missing symbols.

Targeted verification:
- cd ..\playbot-agent
- go test ./internal/compiler -run TestP475 -count=1
*/

func TestP475CompilerConvertsRecordedClickFillNavigateToExecutableBlueprint(t *testing.T) {
	blueprint, err := CompileBlueprint(SemanticPlan{
		Title:       "orders happy path",
		Description: "generated from recording",
		Steps: []SemanticStep{
			{Action: "navigate", Value: "/orders", IntentReason: "Open recorded orders page"},
			{Action: "click", TargetHint: TargetHint{Role: "button", Text: "New order", RecordedSelector: "button.new-order"}, IntentReason: "Start a new order"},
			{Action: "fill", TargetHint: TargetHint{Placeholder: "Customer email", RecordedSelector: "input[name=email]"}, Value: "alice@example.invalid", IntentReason: "Enter customer email"},
		},
	}, CompileContext{BaseURL: "https://example.invalid/app"})
	if err != nil {
		t.Fatalf("CompileBlueprint returned error: %v", err)
	}
	requireP475Step(t, blueprint, 0, "navigate", "url", "https://example.invalid/orders")
	requireP475Step(t, blueprint, 1, "click", "target", map[string]any{"role": "button", "text": "New order", "recorded_selector": "button.new-order"})
	requireP475Step(t, blueprint, 2, "fill", "value", "alice@example.invalid")
	requireP475Step(t, blueprint, 2, "fill", "target", map[string]any{"placeholder": "Customer email", "recorded_selector": "input[name=email]"})

	raw, err := json.Marshal(blueprint)
	if err != nil {
		t.Fatalf("marshal compiled blueprint: %v", err)
	}
	if strings.Contains(string(raw), "target_hint") || strings.Contains(string(raw), "intent_reason") {
		t.Fatalf("compiled active blueprint retained internal fields: %s", raw)
	}
}

func TestP475CompilerPreservesRecordedSelectorRoleTextAndPlaceholder(t *testing.T) {
	blueprint, err := CompileBlueprint(SemanticPlan{
		Title:       "locator preservation",
		Description: "locator contract",
		Steps: []SemanticStep{{
			Action: "click",
			TargetHint: TargetHint{
				Role:             "button",
				Text:             "Save",
				Placeholder:      "ignored for button",
				RecordedSelector: "button.save",
				RefID:            "ref-save",
			},
			IntentReason: "Save the form",
		}},
	}, CompileContext{BaseURL: "https://example.invalid/app"})
	if err != nil {
		t.Fatalf("CompileBlueprint returned error: %v", err)
	}
	step := blueprint.Steps[0]
	if step.Description != "Save the form" {
		t.Fatalf("description = %q, want intent_reason converted to description", step.Description)
	}
	if step.Target.RecordedSelector != "button.save" || step.Target.Role != "button" || step.Target.Text != "Save" || step.Target.RefID != "ref-save" {
		t.Fatalf("target did not preserve recorded locator fields: %+v", step.Target)
	}
}

func TestP475CompilerRejectsUnsupportedAction(t *testing.T) {
	_, err := CompileBlueprint(SemanticPlan{
		Title:       "unsupported action",
		Description: "must fail",
		Steps:       []SemanticStep{{Action: "hover", TargetHint: TargetHint{Text: "Menu"}}},
	}, CompileContext{BaseURL: "https://example.invalid/app"})
	if err == nil || !strings.Contains(err.Error(), "blueprint_unsupported_action") {
		t.Fatalf("unsupported action error = %v, want blueprint_unsupported_action", err)
	}
}

func TestP475CompilerRejectsTargetHintOnlyInFinalBlueprint(t *testing.T) {
	_, err := ValidateExecutableBlueprint(Blueprint{
		Title:       "target hint only",
		Description: "invalid active blueprint",
		Steps: []BlueprintStep{{
			Action:     "click",
			TargetHint: map[string]any{"text": "Save"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "blueprint_missing_final_target") {
		t.Fatalf("target_hint-only validation error = %v, want blueprint_missing_final_target", err)
	}
}

func TestP475CompilerRejectsNavigateValueOnlyInFinalBlueprint(t *testing.T) {
	_, err := ValidateExecutableBlueprint(Blueprint{
		Title:       "navigate value only",
		Description: "invalid active blueprint",
		Steps: []BlueprintStep{{
			Action: "navigate",
			Value:  "/orders",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "blueprint_navigation_missing_url") {
		t.Fatalf("navigate value-only validation error = %v, want blueprint_navigation_missing_url", err)
	}
}

func requireP475Step(t *testing.T, blueprint Blueprint, index int, action string, field string, want any) {
	t.Helper()
	if len(blueprint.Steps) <= index {
		t.Fatalf("compiled blueprint has %d steps, want index %d: %+v", len(blueprint.Steps), index, blueprint)
	}
	step := blueprint.Steps[index]
	if step.Action != action {
		t.Fatalf("step %d action = %q, want %q: %+v", index, step.Action, action, step)
	}
	data, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("marshal step %d: %v", index, err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("decode step %d: %v; raw: %s", index, err, data)
	}
	if _, exists := obj[field]; !exists {
		t.Fatalf("step %d missing field %s; step: %s", index, field, data)
	}
	if !reflect.DeepEqual(obj[field], want) {
		t.Fatalf("step %d field %s = %#v, want %#v; step: %s", index, field, obj[field], want, data)
	}
}
