package api

import (
	"encoding/json"
	"fmt"
	"strings"
)

func parseP475AgentGeneratedCases(result map[string]any, inheritedAuthContext string, defaultURL string) ([]generatedTestCaseBlueprint, error) {
	rawCases, ok := result["test_cases"].([]any)
	if !ok || len(rawCases) == 0 {
		return nil, fmt.Errorf("playbot_agent_missing_test_cases")
	}
	blueprints := make([]generatedTestCaseBlueprint, 0, len(rawCases))
	for _, item := range rawCases {
		blueprint, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("playbot_agent_test_case_invalid")
		}
		parsed, err := validateP475ActiveBlueprintForSave(blueprint, inheritedAuthContext, defaultURL)
		if err != nil {
			return nil, err
		}
		blueprints = append(blueprints, parsed)
	}
	return blueprints, nil
}

func parseP475AgentRefinedBlueprint(result map[string]any, inheritedAuthContext string, defaultURL string) (map[string]any, string, string, error) {
	raw, ok := result["refined_blueprint"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil, "", "", fmt.Errorf("playbot_agent_missing_refined_blueprint")
	}
	parsed, err := validateP475ActiveBlueprintForSave(raw, inheritedAuthContext, defaultURL)
	if err != nil {
		return nil, "", "", err
	}
	normalized, err := normalizeBlueprintObject(parsed.Raw)
	if err != nil {
		return nil, "", "", err
	}
	_ = normalized
	return parsed.Raw, strings.TrimSpace(stringFromAny(result["summary"])), stringFromAny(result["risk_notes"]), nil
}

func validateP475ActiveBlueprintForSave(input map[string]any, inheritedAuthContext string, defaultURL string) (generatedTestCaseBlueprint, error) {
	blueprint := cloneP475Map(input)
	title := strings.TrimSpace(stringFromAny(blueprint["title"]))
	if title == "" {
		return generatedTestCaseBlueprint{}, fmt.Errorf("blueprint_title_required")
	}
	description, _ := blueprint["description"].(string)
	steps, ok := blueprint["steps"].([]any)
	if !ok || len(steps) == 0 {
		return generatedTestCaseBlueprint{}, fmt.Errorf("blueprint_empty_steps")
	}
	for index, item := range steps {
		step, ok := item.(map[string]any)
		if !ok {
			return generatedTestCaseBlueprint{}, fmt.Errorf("blueprint_step_invalid")
		}
		if err := validateP475FinalStep(step); err != nil {
			return generatedTestCaseBlueprint{}, err
		}
		if _, err := normalizeExecutableStep(step, defaultURL); err != nil {
			return generatedTestCaseBlueprint{}, fmt.Errorf("blueprint_runner_normalization_failed: %w", err)
		}
		steps[index] = step
	}
	authContext := strings.TrimSpace(inheritedAuthContext)
	if authContext == "" {
		authContext = authContextClean
	}
	if rawContext := strings.TrimSpace(stringFromAny(blueprint["auth_context"])); rawContext != "" && !validAuthContext(rawContext) {
		return generatedTestCaseBlueprint{}, fmt.Errorf("blueprint_auth_context_invalid")
	}
	blueprint["title"] = title
	blueprint["description"] = description
	blueprint["steps"] = steps
	blueprint["auth_context"] = authContext
	normalized, err := normalizeBlueprintObject(blueprint)
	if err != nil {
		return generatedTestCaseBlueprint{}, err
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(normalized), &raw); err != nil {
		return generatedTestCaseBlueprint{}, err
	}
	return generatedTestCaseBlueprint{
		Title:       title,
		Description: description,
		Steps:       steps,
		AuthContext: authContext,
		Raw:         raw,
	}, nil
}

func validateP475FinalStep(step map[string]any) error {
	action := strings.TrimSpace(stringFromAny(step["action"]))
	if !executableActions[action] {
		return fmt.Errorf("blueprint_unsupported_action")
	}
	if targetHint, ok := step["target_hint"].(map[string]any); ok && len(targetHint) > 0 {
		return fmt.Errorf("blueprint_missing_final_target")
	}
	switch action {
	case "navigate":
		if strings.TrimSpace(stringFromAny(step["url"])) == "" {
			return fmt.Errorf("blueprint_navigation_missing_url")
		}
	case "click", "fill", "select", "expect_visible", "expect_text":
		target, ok := step["target"].(map[string]any)
		if !ok || len(target) == 0 {
			return fmt.Errorf("blueprint_missing_final_target")
		}
		if action == "fill" || action == "select" || action == "expect_text" {
			if strings.TrimSpace(stringFromAny(step["value"])) == "" {
				return fmt.Errorf("blueprint_step_missing_value")
			}
		}
	case "wait":
		return nil
	}
	return nil
}
