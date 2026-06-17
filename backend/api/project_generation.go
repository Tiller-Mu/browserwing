package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/browserwing/browserwing/llm"
	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/services/playbotagent"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type generateTestCasesRequest struct {
	Mode        string `json:"mode"`
	LLMConfigID string `json:"llm_config_id"`
	Instruction string `json:"instruction"`
}

type playbotGenerateOutput struct {
	TestCases      []json.RawMessage `json:"test_cases"`
	Analysis       json.RawMessage   `json:"analysis"`
	GeneratedCount int               `json:"generated_count"`
	Error          any               `json:"error"`
}

type generatedTestCaseBlueprint struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Steps       []any          `json:"steps"`
	AuthContext string         `json:"auth_context"`
	Raw         map[string]any `json:"-"`
}

type generateTestCasesExecutionOptions struct {
	EnableAgentLLM bool
	EventSink      func(playbotagent.Event)
}

type generateTestCasesExecutionResult struct {
	Status int
	Body   gin.H
}

// GenerateTestCases 读取页面主流程，调用 Playbot 生成并按模式保存 TestCase。
func (h *ProjectHandlers) GenerateTestCases(c *gin.Context) {
	projectID, versionID, pageID, ok := parseProjectVersionPageIDs(c)
	if !ok {
		return
	}

	var req generateTestCasesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	response := h.executeGenerateTestCases(c.Request.Context(), projectID, versionID, pageID, req, generateTestCasesExecutionOptions{})
	c.JSON(response.Status, response.Body)
}

func (h *ProjectHandlers) executeGenerateTestCases(ctx context.Context, projectID, versionID, pageID uint, req generateTestCasesRequest, opts generateTestCasesExecutionOptions) generateTestCasesExecutionResult {
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "append"
	}
	if mode != "append" && mode != "replace" && mode != "preview" {
		return generateTestCasesExecutionResult{Status: http.StatusBadRequest, Body: gin.H{"error": "Invalid generation mode"}}
	}

	version, page, err := loadGenerationPageContext(h.gormDB(), projectID, versionID, pageID)
	if err != nil {
		return generateTestCasesExecutionResult{Status: http.StatusNotFound, Body: gin.H{"error": "Project, version, or page not found"}}
	}

	var script models.PageScript
	if err := h.gormDB().Where("page_id = ?", page.ID).Order("created_at desc, id desc").First(&script).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return generateTestCasesExecutionResult{Status: http.StatusBadRequest, Body: gin.H{"error": "请先录制主流程后再生成测试用例"}}
		}
		return generateTestCasesExecutionResult{Status: http.StatusInternalServerError, Body: gin.H{"error": err.Error()}}
	}

	llmConfig, err := h.loadGenerationLLMConfig(req.LLMConfigID)
	if err != nil {
		status, body := llmConfigErrorResponse(err)
		if status != 0 {
			return generateTestCasesExecutionResult{Status: status, Body: body}
		}
		return generateTestCasesExecutionResult{Status: http.StatusInternalServerError, Body: gin.H{"error": err.Error()}}
	}

	source, err := buildP475RecordingSource(script)
	if err != nil {
		return generateTestCasesExecutionResult{Status: http.StatusBadRequest, Body: gin.H{"error": err.Error()}}
	}

	job, secret := buildP475GenerateJob(projectID, versionID, pageID, version, page, source, req.Instruction, llmConfig, opts.EnableAgentLLM)
	eventSink := opts.EventSink
	if eventSink != nil {
		redactions := playbotRunDisplayRedactions(source, secret)
		eventSink = func(event playbotagent.Event) {
			opts.EventSink(sanitizePlaybotAgentEventForDisplay(event, redactions))
		}
	}
	result, err := h.runP475AgentWithContextRetryAndEvents(ctx, job, secret, source, eventSink)
	if err != nil {
		return generateTestCasesExecutionResult{Status: http.StatusInternalServerError, Body: gin.H{"error": err.Error()}}
	}
	if status := strings.TrimSpace(stringFromAny(result["status"])); status != "success" {
		code := strings.TrimSpace(stringFromAny(result["code"]))
		if code == "" {
			code = "playbot_agent_failed"
		}
		return generateTestCasesExecutionResult{Status: http.StatusBadRequest, Body: gin.H{"error": code, "code": code}}
	}
	defaultURL, err := buildExecutionURL(version.BaseURL, page.Path)
	if err != nil {
		return generateTestCasesExecutionResult{Status: http.StatusBadRequest, Body: gin.H{"error": err.Error()}}
	}
	blueprints, err := parseP475AgentGeneratedCases(result, source.AuthContext, defaultURL)
	if err != nil {
		return generateTestCasesExecutionResult{Status: http.StatusBadRequest, Body: gin.H{"error": err.Error()}}
	}
	visibleSummary, modelOutput := sanitizeP475AgentDisplayPayload(result, source, secret)

	if mode == "preview" {
		previewCases := make([]gin.H, 0, len(blueprints))
		for _, blueprint := range blueprints {
			previewCases = append(previewCases, gin.H{
				"title":       blueprint.Title,
				"description": blueprint.Description,
				"blueprint":   blueprint.Raw,
			})
		}
		return generateTestCasesExecutionResult{Status: http.StatusOK, Body: gin.H{
			"mode":            mode,
			"saved":           false,
			"generated_count": len(previewCases),
			"test_cases":      previewCases,
			"visible_summary": visibleSummary,
			"model_output":    modelOutput,
		}}
	}

	savedCases, err := saveGeneratedTestCases(h.gormDB(), page.ID, mode, blueprints)
	if err != nil {
		return generateTestCasesExecutionResult{Status: http.StatusInternalServerError, Body: gin.H{"error": err.Error()}}
	}

	return generateTestCasesExecutionResult{Status: http.StatusOK, Body: gin.H{
		"mode":            mode,
		"saved":           true,
		"generated_count": len(savedCases),
		"test_cases":      savedCases,
		"visible_summary": visibleSummary,
		"model_output":    modelOutput,
	}}
}

func parseProjectVersionPageIDs(c *gin.Context) (uint, uint, uint, bool) {
	projectID, err := parseUintParam(c, "id", "Invalid Project ID")
	if err != nil {
		return 0, 0, 0, false
	}
	versionID, err := parseUintParam(c, "vid", "Invalid Version ID")
	if err != nil {
		return 0, 0, 0, false
	}
	pageID, err := parseUintParam(c, "pid", "Invalid Page ID")
	if err != nil {
		return 0, 0, 0, false
	}
	return projectID, versionID, pageID, true
}

func parseUintParam(c *gin.Context, name, message string) (uint, error) {
	value, err := strconv.ParseUint(c.Param(name), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": message})
		return 0, err
	}
	return uint(value), nil
}

func loadGenerationPageContext(db *gorm.DB, projectID, versionID, pageID uint) (models.ProjectVersion, models.TestPage, error) {
	var project models.Project
	if err := db.First(&project, projectID).Error; err != nil {
		return models.ProjectVersion{}, models.TestPage{}, err
	}

	var version models.ProjectVersion
	if err := db.Where("id = ? AND project_id = ?", versionID, projectID).First(&version).Error; err != nil {
		return models.ProjectVersion{}, models.TestPage{}, err
	}

	var page models.TestPage
	if err := db.Where("id = ? AND version_id = ?", pageID, versionID).First(&page).Error; err != nil {
		return models.ProjectVersion{}, models.TestPage{}, err
	}

	return version, page, nil
}

func parseRequiredJSON(raw string, message string) (any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%s", message)
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("%s", message)
	}
	if parsed == nil {
		return nil, fmt.Errorf("%s", message)
	}
	return parsed, nil
}

func (h *ProjectHandlers) loadGenerationLLMConfig(id string) (*models.LLMConfigModel, error) {
	return llm.ResolveRuntimeConfig(h.store, id)
}

func buildPageURL(baseURL, pagePath string) string {
	path := strings.TrimSpace(pagePath)
	if parsed, err := url.Parse(path); err == nil && parsed.IsAbs() {
		return path
	}

	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	rel := strings.TrimLeft(path, "/")
	if base == "" {
		return rel
	}
	if rel == "" {
		return base
	}
	return base + "/" + rel
}

func parsePlaybotGeneratedCases(stdout string, inheritedAuthContext string) ([]generatedTestCaseBlueprint, error) {
	var output playbotGenerateOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		return nil, fmt.Errorf("Playbot 输出不是合法 JSON")
	}
	if output.Error != nil {
		return nil, fmt.Errorf("Playbot 返回错误: %v", output.Error)
	}
	if len(output.TestCases) == 0 {
		return nil, fmt.Errorf("Playbot 未返回测试用例")
	}

	blueprints := make([]generatedTestCaseBlueprint, 0, len(output.TestCases))
	for _, raw := range output.TestCases {
		var blueprint map[string]any
		if err := json.Unmarshal(raw, &blueprint); err != nil {
			return nil, fmt.Errorf("Playbot 测试用例结构非法")
		}

		title, _ := blueprint["title"].(string)
		description, _ := blueprint["description"].(string)
		steps, ok := blueprint["steps"].([]any)
		if strings.TrimSpace(title) == "" || strings.TrimSpace(description) == "" || !ok || len(steps) == 0 {
			return nil, fmt.Errorf("Playbot 测试用例缺少必要字段")
		}
		if rawContext := strings.TrimSpace(stringFromAny(blueprint["auth_context"])); rawContext != "" && !validAuthContext(rawContext) {
			return nil, fmt.Errorf("Playbot 测试用例 auth_context 非法")
		}
		authContext := strings.TrimSpace(inheritedAuthContext)
		if authContext == "" {
			authContext = authContextClean
		}
		blueprint["auth_context"] = authContext

		blueprints = append(blueprints, generatedTestCaseBlueprint{
			Title:       title,
			Description: description,
			Steps:       steps,
			AuthContext: authContext,
			Raw:         blueprint,
		})
	}

	return blueprints, nil
}

func saveGeneratedTestCases(db *gorm.DB, pageID uint, mode string, blueprints []generatedTestCaseBlueprint) ([]models.TestCase, error) {
	saved := make([]models.TestCase, 0, len(blueprints))
	err := db.Transaction(func(tx *gorm.DB) error {
		if mode == "replace" {
			if err := tx.Where("page_id = ?", pageID).Delete(&models.TestCase{}).Error; err != nil {
				return err
			}
		}

		for _, blueprint := range blueprints {
			blueprintJSON, err := json.Marshal(blueprint.Raw)
			if err != nil {
				return err
			}
			testCase := models.TestCase{
				PageID:        pageID,
				Title:         blueprint.Title,
				Description:   blueprint.Description,
				Blueprint:     string(blueprintJSON),
				ScriptContent: "",
				Status:        "active",
			}
			if err := tx.Create(&testCase).Error; err != nil {
				return err
			}
			saved = append(saved, testCase)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return saved, nil
}
