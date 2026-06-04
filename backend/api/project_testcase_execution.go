package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/services/testcase_executor"
	"github.com/browserwing/browserwing/storage"
	"github.com/gin-gonic/gin"
)

const (
	defaultExecutionLimit = 20
	maxExecutionLimit     = 50
	maxStepTimeoutMs      = 60000
	maxWaitDurationMs     = 30000
)

var executableActions = map[string]bool{
	"navigate":       true,
	"click":          true,
	"fill":           true,
	"select":         true,
	"wait":           true,
	"expect_visible": true,
	"expect_text":    true,
}

type runTestCaseRequest struct {
	BrowserInstanceID string `json:"browser_instance_id"`
	Headless          *bool  `json:"headless"`
	StopOnFailure     *bool  `json:"stop_on_failure"`
	CaptureScreenshot *bool  `json:"capture_screenshot"`
	AuthContext       string `json:"auth_context"`
}

type testExecutionSummaryResponse struct {
	ID           uint      `json:"id"`
	TestCaseID   uint      `json:"test_case_id"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message"`
	DurationMs   int       `json:"duration_ms"`
	CreatedAt    time.Time `json:"created_at"`
}

type testExecutionDetailResponse struct {
	ID           uint           `json:"id"`
	TestCaseID   uint           `json:"test_case_id"`
	Status       string         `json:"status"`
	ErrorMessage string         `json:"error_message"`
	DurationMs   int            `json:"duration_ms"`
	ReportData   map[string]any `json:"report_data"`
	CreatedAt    time.Time      `json:"created_at"`
}

func (h *ProjectHandlers) RunTestCase(c *gin.Context) {
	_, _, pageID, testCaseID, ok := parseProjectVersionPageTestCaseIDs(c)
	if !ok {
		return
	}
	version, page, err := loadGenerationPageContextFromContext(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, or testcase not found"})
		return
	}
	testCase, err := loadTestCaseForPage(pageID, testCaseID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, or testcase not found"})
		return
	}

	var req runTestCaseRequest
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
			return
		}
	}

	input, err := buildRunTestCaseInput(testCase, version, page, req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	runtime := h.projectAuthRuntime()
	var authSummary map[string]any
	var authStateJSON string
	if input["auth_context"] == authContextProjectSaved {
		auth, err := loadActiveProjectAuthState(version.ProjectID, version.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if auth == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Project auth state is required"})
			return
		}
		authSummary = projectAuthStateSummary(*auth)
		input["auth_state"] = authSummary
		authStateJSON = auth.StateJSON
	}
	if runtime == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Project auth runtime is not configured"})
		return
	}
	if err := runtime.PrepareTestExecution(c.Request.Context(), input); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Prepare isolated test execution failed"})
		return
	}
	if input["auth_context"] == authContextProjectSaved {
		input["auth_state_json"] = authStateJSON
		if err := runtime.RestoreProjectAuthState(c.Request.Context(), input); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Restore project auth state failed"})
			return
		}
		delete(input, "auth_state_json")
	}

	result, err := h.runTestCaseWithRunner(c.Request.Context(), input)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	attachAuthContextToRunnerResult(result, input, authSummary)

	execution, err := saveTestCaseExecution(testCase.ID, result)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	detail, err := toTestExecutionDetail(execution)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"execution": detail})
}

func (h *ProjectHandlers) ListTestCaseExecutions(c *gin.Context) {
	_, _, pageID, testCaseID, ok := parseProjectVersionPageTestCaseIDs(c)
	if !ok {
		return
	}
	if _, _, err := loadGenerationPageContextFromContext(c); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, or testcase not found"})
		return
	}
	if _, err := loadTestCaseForPage(pageID, testCaseID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, or testcase not found"})
		return
	}

	limit := defaultExecutionLimit
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > maxExecutionLimit {
		limit = maxExecutionLimit
	}

	var executions []models.TestExecution
	if err := storage.DB.Where("test_case_id = ?", testCaseID).
		Order("created_at desc, id desc").
		Limit(limit).
		Find(&executions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	summaries := make([]testExecutionSummaryResponse, 0, len(executions))
	for _, execution := range executions {
		summaries = append(summaries, toTestExecutionSummary(execution))
	}
	c.JSON(http.StatusOK, gin.H{
		"executions": summaries,
		"count":      len(summaries),
	})
}

func (h *ProjectHandlers) GetTestCaseExecution(c *gin.Context) {
	_, _, pageID, testCaseID, ok := parseProjectVersionPageTestCaseIDs(c)
	if !ok {
		return
	}
	executionID, err := parseUintParam(c, "eid", "Invalid TestExecution ID")
	if err != nil {
		return
	}
	if _, _, err := loadGenerationPageContextFromContext(c); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, testcase, or execution not found"})
		return
	}
	if _, err := loadTestCaseForPage(pageID, testCaseID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, testcase, or execution not found"})
		return
	}

	var execution models.TestExecution
	if err := storage.DB.Where("id = ? AND test_case_id = ?", executionID, testCaseID).First(&execution).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, testcase, or execution not found"})
		return
	}
	detail, err := toTestExecutionDetail(execution)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"execution": detail})
}

func (h *ProjectHandlers) runTestCaseWithRunner(ctx context.Context, input map[string]any) (map[string]any, error) {
	if h.testCaseRunner == nil {
		return nil, fmt.Errorf("TestCase runner is not configured")
	}
	runner := h.testCaseRunner.get()
	if runner == nil {
		return nil, fmt.Errorf("TestCase runner is not configured")
	}
	return invokeTestCaseRunner(ctx, runner, input)
}

func invokeTestCaseRunner(ctx context.Context, runner any, input map[string]any) (map[string]any, error) {
	if typed, ok := runner.(interface {
		Run(context.Context, map[string]any) (map[string]any, error)
	}); ok {
		return typed.Run(ctx, input)
	}

	method := reflect.ValueOf(runner).MethodByName("Run")
	if !method.IsValid() || method.Type().NumIn() != 2 || method.Type().NumOut() != 2 {
		return nil, fmt.Errorf("TestCase runner has invalid Run signature")
	}
	ctxValue := reflect.ValueOf(ctx)
	inputType := method.Type().In(1)
	inputValue := reflect.ValueOf(input)
	if !inputValue.Type().AssignableTo(inputType) {
		if inputValue.Type().ConvertibleTo(inputType) {
			inputValue = inputValue.Convert(inputType)
		} else {
			return nil, fmt.Errorf("TestCase runner input type %s is not compatible with %s", inputValue.Type(), inputType)
		}
	}
	output := method.Call([]reflect.Value{ctxValue, inputValue})
	if !output[1].IsNil() {
		err, _ := output[1].Interface().(error)
		return nil, err
	}
	data, err := json.Marshal(output[0].Interface())
	if err != nil {
		return nil, fmt.Errorf("marshal TestCase runner result: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("decode TestCase runner result: %w", err)
	}
	return normalizeRunnerResultObject(obj), nil
}

func buildRunTestCaseInput(testCase models.TestCase, version models.ProjectVersion, page models.TestPage, req runTestCaseRequest) (map[string]any, error) {
	if testCase.Status != "active" {
		return nil, testCaseValidationError("Only active TestCases can be executed")
	}
	blueprint, err := parseExecutableBlueprint(testCase.Blueprint)
	if err != nil {
		return nil, err
	}
	authContext, authContextSource, err := authContextFromBlueprint(blueprint)
	if err != nil {
		return nil, err
	}
	if requestAuthContext := strings.TrimSpace(req.AuthContext); requestAuthContext != "" {
		if !validAuthContext(requestAuthContext) {
			return nil, testCaseValidationError("Request auth_context is invalid")
		}
		authContext = requestAuthContext
		authContextSource = "request"
	}
	defaultURL, err := buildExecutionURL(version.BaseURL, page.Path)
	if err != nil {
		return nil, err
	}

	rawSteps, _ := blueprint["steps"].([]any)
	steps := make([]map[string]any, 0, len(rawSteps))
	initialNavigation := map[string]any{
		"mode":       "default",
		"url":        defaultURL,
		"step_index": nil,
	}
	if len(rawSteps) > 0 {
		if first, ok := rawSteps[0].(map[string]any); ok && strings.TrimSpace(stringField(first, "action")) == "navigate" {
			initialNavigation["mode"] = "explicit_step"
			initialNavigation["step_index"] = 0
		}
	}
	for index, rawStep := range rawSteps {
		step, ok := rawStep.(map[string]any)
		if !ok {
			return nil, testCaseValidationError(fmt.Sprintf("Blueprint step %d must be a JSON object", index))
		}
		normalized, err := normalizeExecutableStep(step, defaultURL)
		if err != nil {
			return nil, err
		}
		steps = append(steps, normalized)
		if index == 0 && normalized["action"] == "navigate" {
			initialNavigation["url"] = normalized["url"]
		}
	}

	stopOnFailure := true
	if req.StopOnFailure != nil {
		stopOnFailure = *req.StopOnFailure
	}
	captureScreenshot := true
	if req.CaptureScreenshot != nil {
		captureScreenshot = *req.CaptureScreenshot
	}

	return map[string]any{
		"test_case_id":        testCase.ID,
		"page_id":             page.ID,
		"execution_url":       initialNavigation["url"],
		"initial_navigation":  initialNavigation,
		"steps":               steps,
		"browser_instance_id": req.BrowserInstanceID,
		"stop_on_failure":     stopOnFailure,
		"capture_screenshot":  captureScreenshot,
		"auth_context":        authContext,
		"auth_context_source": authContextSource,
	}, nil
}

func parseExecutableBlueprint(raw string) (map[string]any, error) {
	var blueprint map[string]any
	if err := json.Unmarshal([]byte(raw), &blueprint); err != nil {
		return nil, testCaseValidationError("TestCase blueprint must be a JSON object")
	}
	if len(blueprint) == 0 {
		return nil, testCaseValidationError("TestCase blueprint must be a JSON object")
	}
	steps, ok := blueprint["steps"].([]any)
	if !ok || len(steps) == 0 {
		return nil, testCaseValidationError("Active TestCase blueprint must contain executable steps")
	}
	return blueprint, nil
}

func normalizeExecutableStep(step map[string]any, defaultURL string) (map[string]any, error) {
	action := strings.TrimSpace(stringField(step, "action"))
	if !executableActions[action] {
		return nil, testCaseValidationError("Blueprint contains unsupported action")
	}
	if timeout, ok := intField(step, "timeout_ms"); ok && timeout > maxStepTimeoutMs {
		return nil, testCaseValidationError("Step timeout_ms exceeds allowed maximum")
	}

	normalized := map[string]any{
		"action":      action,
		"description": stringField(step, "description"),
	}
	if timeout, ok := intField(step, "timeout_ms"); ok {
		normalized["timeout_ms"] = timeout
	}

	switch action {
	case "navigate":
		targetURL := defaultURL
		if rawURL := strings.TrimSpace(stringField(step, "url")); rawURL != "" {
			nextURL, err := resolveExecutionURL(defaultURL, rawURL)
			if err != nil {
				return nil, err
			}
			targetURL = nextURL
		}
		normalized["url"] = targetURL
	case "click", "expect_visible":
		identifier, summary, err := normalizeStepTarget(step)
		if err != nil {
			return nil, err
		}
		normalized["target_identifier"] = identifier
		normalized["target_summary"] = summary
	case "fill":
		identifier, summary, err := normalizeStepTarget(step)
		if err != nil {
			return nil, err
		}
		value, ok := stringCompatibleField(step, "value", "text")
		if !ok {
			return nil, testCaseValidationError("fill step requires value")
		}
		normalized["target_identifier"] = identifier
		normalized["target_summary"] = summary
		normalized["value"] = value
	case "select":
		identifier, summary, err := normalizeStepTarget(step)
		if err != nil {
			return nil, err
		}
		value, ok := stringCompatibleField(step, "value")
		if !ok {
			return nil, testCaseValidationError("select step requires value")
		}
		normalized["target_identifier"] = identifier
		normalized["target_summary"] = summary
		normalized["value"] = value
	case "wait":
		if duration, ok := intField(step, "duration_ms"); ok {
			if duration > maxWaitDurationMs {
				return nil, testCaseValidationError("wait duration_ms exceeds allowed maximum")
			}
			normalized["duration_ms"] = duration
			normalized["target_summary"] = fmt.Sprintf("duration:%dms", duration)
			break
		}
		identifier, summary, err := normalizeStepTarget(step)
		if err != nil {
			return nil, testCaseValidationError("wait step requires target or duration_ms")
		}
		normalized["target_identifier"] = identifier
		normalized["target_summary"] = summary
	case "expect_text":
		value, ok := stringCompatibleField(step, "value", "text")
		if !ok || strings.TrimSpace(value) == "" {
			return nil, testCaseValidationError("expect_text step requires expected text")
		}
		normalized["value"] = value
		if hasTarget(step) {
			identifier, summary, err := normalizeStepTarget(step)
			if err != nil {
				return nil, err
			}
			normalized["target_identifier"] = identifier
			normalized["target_summary"] = summary
		} else {
			normalized["target_summary"] = "page:text"
		}
	}
	return normalized, nil
}

func normalizeStepTarget(step map[string]any) (string, string, error) {
	target, ok := targetObject(step)
	if !ok {
		return "", "", testCaseValidationError("step requires target")
	}
	if value := strings.TrimSpace(stringField(target, "ref_id")); value != "" {
		return value, "ref_id:" + value, nil
	}
	role := strings.TrimSpace(stringField(target, "role"))
	text := strings.TrimSpace(stringField(target, "text"))
	if role != "" && text != "" {
		return roleTextXPath(role, text), "role+text:" + role + ":" + text, nil
	}
	if value := strings.TrimSpace(stringField(target, "recorded_selector")); value != "" {
		return value, "recorded_selector:" + value, nil
	}
	if value := strings.TrimSpace(stringField(target, "selector")); value != "" {
		return value, "selector:" + value, nil
	}
	if value := strings.TrimSpace(stringField(target, "css")); value != "" {
		return "css:" + value, "css:" + value, nil
	}
	if value := strings.TrimSpace(stringField(target, "xpath")); value != "" {
		return ensureXPathPrefix(value), "xpath:" + strings.TrimPrefix(value, "xpath:"), nil
	}
	if value := strings.TrimSpace(stringField(target, "label")); value != "" {
		return value, "label:" + value, nil
	}
	if value := strings.TrimSpace(stringField(target, "placeholder")); value != "" {
		return value, "placeholder:" + value, nil
	}
	if text != "" {
		return text, "text:" + text, nil
	}
	return "", "", testCaseValidationError("step target has no supported locator")
}

func targetObject(step map[string]any) (map[string]any, bool) {
	for _, name := range []string{"target", "target_hint"} {
		if obj, ok := step[name].(map[string]any); ok && len(obj) > 0 {
			return obj, true
		}
	}
	return nil, false
}

func hasTarget(step map[string]any) bool {
	_, ok := targetObject(step)
	return ok
}

func buildExecutionURL(baseURL, pagePath string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", testCaseValidationError("Project version base_url is required for execution")
	}
	return resolveExecutionURL(baseURL, pagePath)
}

func resolveExecutionURL(baseURL, rawPath string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", testCaseValidationError("Project version base_url is invalid")
	}
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return base.String(), nil
	}
	next, err := url.Parse(rawPath)
	if err != nil {
		return "", testCaseValidationError("Execution URL is invalid")
	}
	return base.ResolveReference(next).String(), nil
}

func saveTestCaseExecution(testCaseID uint, result map[string]any) (models.TestExecution, error) {
	status := strings.TrimSpace(stringFromAny(firstRunnerValue(result, "status", "Status")))
	if status != testcase_executor.StatusPassed && status != testcase_executor.StatusFailed && status != testcase_executor.StatusError {
		return models.TestExecution{}, fmt.Errorf("TestCase runner returned invalid status")
	}
	reportData, ok := firstRunnerValue(result, "report_data", "ReportData").(map[string]any)
	if !ok || len(reportData) == 0 {
		return models.TestExecution{}, fmt.Errorf("TestCase runner returned empty report_data")
	}
	reportData = normalizeExecutionReportForStorage(reportData)
	reportJSON, err := json.Marshal(reportData)
	if err != nil {
		return models.TestExecution{}, fmt.Errorf("marshal execution report_data: %w", err)
	}
	execution := models.TestExecution{
		TestCaseID:   testCaseID,
		Status:       status,
		ErrorMessage: stringFromAny(firstRunnerValue(result, "error_message", "ErrorMessage")),
		DurationMs:   intFromAny(firstRunnerValue(result, "duration_ms", "DurationMs")),
		ReportData:   string(reportJSON),
	}
	if err := storage.DB.Create(&execution).Error; err != nil {
		return models.TestExecution{}, err
	}
	return execution, nil
}

func normalizeExecutionReportForStorage(report map[string]any) map[string]any {
	steps, ok := report["steps"].([]any)
	if !ok {
		return report
	}
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if _, exists := step["error"]; !exists {
			step["error"] = ""
		}
	}
	return report
}

func attachAuthContextToRunnerResult(result map[string]any, input map[string]any, authSummary map[string]any) {
	reportData, ok := firstRunnerValue(result, "report_data", "ReportData").(map[string]any)
	if !ok || reportData == nil {
		return
	}
	if _, exists := reportData["auth_context"]; !exists {
		reportData["auth_context"] = input["auth_context"]
	}
	if _, exists := reportData["auth_context_source"]; !exists {
		reportData["auth_context_source"] = input["auth_context_source"]
	}
	if authSummary != nil {
		reportData["auth_state"] = authSummary
	}
	result["report_data"] = reportData
}

func toTestExecutionSummary(execution models.TestExecution) testExecutionSummaryResponse {
	return testExecutionSummaryResponse{
		ID:           execution.ID,
		TestCaseID:   execution.TestCaseID,
		Status:       execution.Status,
		ErrorMessage: execution.ErrorMessage,
		DurationMs:   execution.DurationMs,
		CreatedAt:    execution.CreatedAt,
	}
}

func toTestExecutionDetail(execution models.TestExecution) (testExecutionDetailResponse, error) {
	var reportData map[string]any
	if err := json.Unmarshal([]byte(execution.ReportData), &reportData); err != nil {
		return testExecutionDetailResponse{}, fmt.Errorf("TestExecution report_data is corrupted: %w", err)
	}
	if len(reportData) == 0 {
		return testExecutionDetailResponse{}, fmt.Errorf("TestExecution report_data is corrupted")
	}
	return testExecutionDetailResponse{
		ID:           execution.ID,
		TestCaseID:   execution.TestCaseID,
		Status:       execution.Status,
		ErrorMessage: execution.ErrorMessage,
		DurationMs:   execution.DurationMs,
		ReportData:   reportData,
		CreatedAt:    execution.CreatedAt,
	}, nil
}

func normalizeRunnerResultObject(obj map[string]any) map[string]any {
	result := make(map[string]any)
	for key, value := range obj {
		switch key {
		case "Status":
			result["status"] = value
		case "ErrorMessage":
			result["error_message"] = value
		case "DurationMs":
			result["duration_ms"] = value
		case "ReportData":
			result["report_data"] = value
		default:
			result[key] = value
		}
	}
	if report, ok := result["report_data"].(map[string]any); ok {
		result["report_data"] = normalizeReportObject(report)
	}
	return result
}

func normalizeReportObject(report map[string]any) map[string]any {
	normalized := make(map[string]any)
	for key, value := range report {
		normalized[toSnakeCase(key)] = normalizeReportValue(value)
	}
	return normalized
}

func normalizeReportValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return normalizeReportObject(v)
	case []any:
		items := make([]any, 0, len(v))
		for _, item := range v {
			items = append(items, normalizeReportValue(item))
		}
		return items
	default:
		return v
	}
}

func toSnakeCase(value string) string {
	switch value {
	case "schemaVersion":
		return "schema_version"
	case "executionUrl":
		return "execution_url"
	case "initialNavigation":
		return "initial_navigation"
	case "browserInstanceID", "browserInstanceId":
		return "browser_instance_id"
	case "startedAt":
		return "started_at"
	case "endedAt":
		return "ended_at"
	case "durationMs":
		return "duration_ms"
	case "failedStepIndex":
		return "failed_step_index"
	case "totalSteps":
		return "total_steps"
	case "passedSteps":
		return "passed_steps"
	case "failedSteps":
		return "failed_steps"
	case "finalUrl":
		return "final_url"
	case "targetSummary":
		return "target_summary"
	case "stepIndex":
		return "step_index"
	case "authContext":
		return "auth_context"
	case "authContextSource":
		return "auth_context_source"
	case "authState":
		return "auth_state"
	default:
		return value
	}
}

func roleTextXPath(role, text string) string {
	return fmt.Sprintf(`//*[@role=%q and contains(normalize-space(.), %q)] | //%s[contains(normalize-space(.), %q)]`, role, text, roleTag(role), text)
}

func roleTag(role string) string {
	switch strings.ToLower(role) {
	case "button":
		return "button"
	case "link":
		return "a"
	case "textbox":
		return "input"
	default:
		return "*"
	}
}

func ensureXPathPrefix(value string) string {
	if strings.HasPrefix(strings.ToLower(value), "xpath:") {
		return value
	}
	return "xpath:" + value
}

func stringCompatibleField(obj map[string]any, names ...string) (string, bool) {
	for _, name := range names {
		value, ok := obj[name]
		if ok {
			return stringFromAny(value), true
		}
	}
	return "", false
}

func stringField(obj map[string]any, name string) string {
	return stringFromAny(obj[name])
}

func intField(obj map[string]any, name string) (int, bool) {
	value, ok := obj[name]
	if !ok {
		return 0, false
	}
	return intFromAny(value), true
}

func firstRunnerValue(obj map[string]any, names ...string) any {
	for _, name := range names {
		if value, ok := obj[name]; ok {
			return value
		}
	}
	return nil
}

func stringFromAny(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}
