package compiler

import (
	"fmt"
	"net/url"
	"strings"
)

type SemanticPlan struct {
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	Steps       []SemanticStep `json:"steps,omitempty"`
}

type SemanticStep struct {
	Action       string     `json:"action,omitempty"`
	Value        string     `json:"value,omitempty"`
	URL          string     `json:"url,omitempty"`
	TargetHint   TargetHint `json:"target_hint,omitempty"`
	IntentReason string     `json:"intent_reason,omitempty"`
}

type TargetHint struct {
	Role             string `json:"role,omitempty"`
	Text             string `json:"text,omitempty"`
	Placeholder      string `json:"placeholder,omitempty"`
	Label            string `json:"label,omitempty"`
	Selector         string `json:"selector,omitempty"`
	RecordedSelector string `json:"recorded_selector,omitempty"`
	RefID            string `json:"ref_id,omitempty"`
}

type CompileContext struct {
	BaseURL string
}

type Blueprint struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Steps       []BlueprintStep `json:"steps"`
	AuthContext string          `json:"auth_context,omitempty"`
}

type BlueprintStep struct {
	Action      string         `json:"action"`
	Description string         `json:"description,omitempty"`
	URL         string         `json:"url,omitempty"`
	Value       string         `json:"value,omitempty"`
	Target      Target         `json:"target,omitempty"`
	TargetHint  map[string]any `json:"target_hint,omitempty"`
	TimeoutMs   int            `json:"timeout_ms,omitempty"`
}

type Target struct {
	Role             string `json:"role,omitempty"`
	Text             string `json:"text,omitempty"`
	Placeholder      string `json:"placeholder,omitempty"`
	Label            string `json:"label,omitempty"`
	Selector         string `json:"selector,omitempty"`
	RecordedSelector string `json:"recorded_selector,omitempty"`
	RefID            string `json:"ref_id,omitempty"`
}

func CompileBlueprint(plan SemanticPlan, ctx CompileContext) (Blueprint, error) {
	blueprint := Blueprint{
		Title:       strings.TrimSpace(plan.Title),
		Description: strings.TrimSpace(plan.Description),
		Steps:       make([]BlueprintStep, 0, len(plan.Steps)),
	}
	for _, semantic := range plan.Steps {
		step := BlueprintStep{
			Action:      strings.TrimSpace(semantic.Action),
			Description: strings.TrimSpace(semantic.IntentReason),
		}
		if step.Description == "" {
			step.Description = strings.TrimSpace(plan.Description)
		}
		switch step.Action {
		case "navigate":
			rawURL := firstNonEmpty(semantic.URL, semantic.Value)
			if strings.TrimSpace(rawURL) == "" {
				return Blueprint{}, fmt.Errorf("blueprint_navigation_missing_url")
			}
			resolved, err := resolveURL(ctx.BaseURL, rawURL)
			if err != nil {
				return Blueprint{}, err
			}
			step.URL = resolved
		case "click", "expect_visible":
			target := targetFromHint(semantic.TargetHint)
			if !target.hasLocator() {
				return Blueprint{}, fmt.Errorf("blueprint_missing_final_target")
			}
			step.Target = target
		case "fill", "select", "expect_text":
			target := targetFromHint(semantic.TargetHint)
			if !target.hasLocator() {
				return Blueprint{}, fmt.Errorf("blueprint_missing_final_target")
			}
			if strings.TrimSpace(semantic.Value) == "" {
				return Blueprint{}, fmt.Errorf("blueprint_step_missing_value")
			}
			step.Target = target
			step.Value = semantic.Value
		case "wait":
			target := targetFromHint(semantic.TargetHint)
			if target.hasLocator() {
				step.Target = target
			}
		default:
			return Blueprint{}, fmt.Errorf("blueprint_unsupported_action")
		}
		blueprint.Steps = append(blueprint.Steps, step)
	}
	if _, err := ValidateExecutableBlueprint(blueprint); err != nil {
		return Blueprint{}, err
	}
	return blueprint, nil
}

func ValidateExecutableBlueprint(blueprint Blueprint) (Blueprint, error) {
	if len(blueprint.Steps) == 0 {
		return Blueprint{}, fmt.Errorf("blueprint_empty_steps")
	}
	for _, step := range blueprint.Steps {
		switch strings.TrimSpace(step.Action) {
		case "navigate":
			if strings.TrimSpace(step.URL) == "" {
				return Blueprint{}, fmt.Errorf("blueprint_navigation_missing_url")
			}
		case "click", "expect_visible":
			if !step.Target.hasLocator() {
				return Blueprint{}, fmt.Errorf("blueprint_missing_final_target")
			}
		case "fill", "select", "expect_text":
			if !step.Target.hasLocator() {
				return Blueprint{}, fmt.Errorf("blueprint_missing_final_target")
			}
			if strings.TrimSpace(step.Value) == "" {
				return Blueprint{}, fmt.Errorf("blueprint_step_missing_value")
			}
		case "wait":
			continue
		default:
			return Blueprint{}, fmt.Errorf("blueprint_unsupported_action")
		}
		if len(step.TargetHint) > 0 && !step.Target.hasLocator() {
			return Blueprint{}, fmt.Errorf("blueprint_missing_final_target")
		}
	}
	return blueprint, nil
}

func targetFromHint(hint TargetHint) Target {
	return Target{
		Role:             strings.TrimSpace(hint.Role),
		Text:             strings.TrimSpace(hint.Text),
		Placeholder:      strings.TrimSpace(hint.Placeholder),
		Label:            strings.TrimSpace(hint.Label),
		Selector:         strings.TrimSpace(hint.Selector),
		RecordedSelector: strings.TrimSpace(hint.RecordedSelector),
		RefID:            strings.TrimSpace(hint.RefID),
	}
}

func (t Target) hasLocator() bool {
	return strings.TrimSpace(t.RefID) != "" ||
		strings.TrimSpace(t.RecordedSelector) != "" ||
		strings.TrimSpace(t.Selector) != "" ||
		(strings.TrimSpace(t.Role) != "" && strings.TrimSpace(t.Text) != "") ||
		strings.TrimSpace(t.Text) != "" ||
		strings.TrimSpace(t.Label) != "" ||
		strings.TrimSpace(t.Placeholder) != ""
}

func resolveURL(baseURL, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("blueprint_navigation_invalid_url")
	}
	if parsed.IsAbs() {
		return parsed.String(), nil
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("blueprint_navigation_invalid_base_url")
	}
	return base.ResolveReference(parsed).String(), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
