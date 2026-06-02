package testcase_executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	executor2 "github.com/browserwing/browserwing/executor"
)

const (
	StatusPassed = "passed"
	StatusFailed = "failed"
	StatusError  = "error"
)

type BrowserExecutor interface {
	Navigate(context.Context, string, *executor2.NavigateOptions) (*executor2.OperationResult, error)
	Click(context.Context, string, *executor2.ClickOptions) (*executor2.OperationResult, error)
	Type(context.Context, string, string, *executor2.TypeOptions) (*executor2.OperationResult, error)
	Select(context.Context, string, string, *executor2.SelectOptions) (*executor2.OperationResult, error)
	WaitFor(context.Context, string, *executor2.WaitForOptions) (*executor2.OperationResult, error)
	GetText(context.Context, string) (*executor2.OperationResult, error)
	GetPageText(context.Context) (*executor2.OperationResult, error)
	GetPageInfo(context.Context) (*executor2.OperationResult, error)
	Screenshot(context.Context, *executor2.ScreenshotOptions) (*executor2.OperationResult, error)
}

type Runner struct {
	executor BrowserExecutor
}

func New(executor BrowserExecutor) *Runner {
	return &Runner{executor: executor}
}

func (r *Runner) Run(ctx context.Context, input map[string]any) (map[string]any, error) {
	startedAt := time.Now().UTC()
	steps := mapSlice(input["steps"])
	stopOnFailure := boolValue(input["stop_on_failure"], true)
	captureScreenshot := boolValue(input["capture_screenshot"], true)
	executionURL := stringValue(input["execution_url"])
	initialNavigation := mapValue(input["initial_navigation"])

	reportSteps := make([]map[string]any, 0, len(steps))
	artifacts := map[string]any{"screenshots": []string{}}
	status := StatusPassed
	errorMessage := ""
	failedStepIndex := any(nil)

	if r.executor == nil {
		status = StatusError
		errorMessage = "testcase executor is not configured"
	} else if stringValue(initialNavigation["mode"]) == "default" {
		if _, err := r.executor.Navigate(ctx, executionURL, &executor2.NavigateOptions{WaitUntil: "load", Timeout: 60 * time.Second}); err != nil {
			status = StatusError
			errorMessage = fmt.Sprintf("initial navigation error: %s", err.Error())
		}
	}

	if errorMessage == "" {
		for i, step := range steps {
			stepReport := r.runStep(ctx, step, captureScreenshot, artifacts)
			stepReport["index"] = i
			reportSteps = append(reportSteps, stepReport)

			stepStatus := stringValue(stepReport["status"])
			if stepStatus != StatusPassed {
				status = stepStatus
				failedStepIndex = i
				errorMessage = fmt.Sprintf("step %d %s %s: %s", i, stringValue(step["action"]), stepStatus, stringValue(stepReport["error"]))
				if stopOnFailure {
					break
				}
			}
		}
	}

	endedAt := time.Now().UTC()
	durationMs := int(endedAt.Sub(startedAt).Milliseconds())
	if durationMs < 0 {
		durationMs = 0
	}
	if explicitDuration, ok := intValue(input["duration_ms"]); ok && explicitDuration > 0 {
		durationMs = explicitDuration
	}

	finalURL := executionURL
	if r.executor != nil {
		if pageInfo, err := r.executor.GetPageInfo(ctx); err == nil && pageInfo != nil && pageInfo.Data != nil {
			if url := stringValue(pageInfo.Data["url"]); url != "" {
				finalURL = url
			}
		}
	}

	passedSteps := 0
	for _, step := range reportSteps {
		if stringValue(step["status"]) == StatusPassed {
			passedSteps++
		}
	}
	report := map[string]any{
		"schema_version":      1,
		"source":              "blueprint",
		"execution_url":       executionURL,
		"initial_navigation":  initialNavigation,
		"browser_instance_id": stringValue(input["browser_instance_id"]),
		"started_at":          startedAt.Format(time.RFC3339Nano),
		"ended_at":            endedAt.Format(time.RFC3339Nano),
		"duration_ms":         durationMs,
		"steps":               reportSteps,
		"artifacts":           artifacts,
		"final_url":           finalURL,
		"summary": map[string]any{
			"total_steps":       len(steps),
			"passed_steps":      passedSteps,
			"failed_steps":      len(reportSteps) - passedSteps,
			"failed_step_index": failedStepIndex,
		},
	}

	return map[string]any{
		"status":        status,
		"error_message": errorMessage,
		"duration_ms":   durationMs,
		"report_data":   report,
	}, nil
}

func (r *Runner) runStep(ctx context.Context, step map[string]any, captureScreenshot bool, artifacts map[string]any) map[string]any {
	startedAt := time.Now().UTC()
	action := stringValue(step["action"])
	report := map[string]any{
		"action":         action,
		"description":    stringValue(step["description"]),
		"started_at":     startedAt.Format(time.RFC3339Nano),
		"target_summary": stringValue(step["target_summary"]),
	}

	var err error
	status := StatusPassed
	timeout := time.Duration(intValueOr(step["timeout_ms"], 10000)) * time.Millisecond
	identifier := stringValue(step["target_identifier"])
	value := stringValue(step["value"])

	switch action {
	case "navigate":
		_, err = r.executor.Navigate(ctx, stringValue(step["url"]), &executor2.NavigateOptions{WaitUntil: "load", Timeout: timeout})
	case "click":
		_, err = r.executor.Click(ctx, identifier, &executor2.ClickOptions{WaitVisible: true, WaitEnabled: true, Timeout: timeout, Button: "left", ClickCount: 1})
	case "fill":
		_, err = r.executor.Type(ctx, identifier, value, &executor2.TypeOptions{Clear: true, WaitVisible: true, Timeout: timeout})
	case "select":
		_, err = r.executor.Select(ctx, identifier, value, &executor2.SelectOptions{WaitVisible: true, Timeout: timeout})
	case "wait":
		if duration, ok := intValue(step["duration_ms"]); ok && duration > 0 {
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-time.After(time.Duration(duration) * time.Millisecond):
			}
		} else {
			_, err = r.executor.WaitFor(ctx, identifier, &executor2.WaitForOptions{Timeout: timeout, State: "visible"})
		}
	case "expect_visible":
		_, err = r.executor.WaitFor(ctx, identifier, &executor2.WaitForOptions{Timeout: timeout, State: "visible"})
		if err != nil {
			status = StatusFailed
		}
	case "expect_text":
		var textResult *executor2.OperationResult
		if identifier != "" {
			textResult, err = r.executor.GetText(ctx, identifier)
		} else {
			textResult, err = r.executor.GetPageText(ctx)
		}
		if err == nil && textResult != nil {
			actual := stringValue(textResult.Data["text"])
			if !strings.Contains(actual, value) {
				status = StatusFailed
				err = fmt.Errorf("expected text not found")
			}
		}
	default:
		status = StatusError
		err = fmt.Errorf("unsupported action: %s", action)
	}

	if err != nil && status == StatusPassed {
		status = StatusError
	}
	if err != nil {
		report["error"] = err.Error()
		if captureScreenshot {
			r.captureFailureScreenshot(ctx, artifacts)
		}
	}

	endedAt := time.Now().UTC()
	report["status"] = status
	report["ended_at"] = endedAt.Format(time.RFC3339Nano)
	report["duration_ms"] = int(endedAt.Sub(startedAt).Milliseconds())
	return report
}

func (r *Runner) captureFailureScreenshot(ctx context.Context, artifacts map[string]any) {
	if r.executor == nil {
		return
	}
	result, err := r.executor.Screenshot(ctx, &executor2.ScreenshotOptions{FullPage: false, Quality: 80, Format: "png"})
	if err != nil || result == nil || result.Data == nil {
		return
	}
	path := stringValue(result.Data["path"])
	if path == "" {
		return
	}
	screenshots, _ := artifacts["screenshots"].([]string)
	screenshots = append(screenshots, path)
	artifacts["screenshots"] = screenshots
}

func mapSlice(value any) []map[string]any {
	items, _ := value.([]map[string]any)
	return items
}

func mapValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func boolValue(value any, fallback bool) bool {
	if value == nil {
		return fallback
	}
	result, ok := value.(bool)
	if !ok {
		return fallback
	}
	return result
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func intValueOr(value any, fallback int) int {
	if result, ok := intValue(value); ok {
		return result
	}
	return fallback
}

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}
