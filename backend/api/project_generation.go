package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/services/playbot"
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

	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "append"
	}
	if mode != "append" && mode != "replace" && mode != "preview" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid generation mode"})
		return
	}

	version, page, err := loadGenerationPageContext(h.gormDB(), projectID, versionID, pageID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, or page not found"})
		return
	}

	var script models.PageScript
	if err := h.gormDB().Where("page_id = ?", page.ID).Order("created_at desc, id desc").First(&script).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请先录制主流程后再生成测试用例"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	snapshot, err := parseRequiredJSON(script.DOMSnapshot, "页面快照 JSON 非法")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	intentPlan, err := parseRequiredJSON(script.ActionTrace, "主流程录制 JSON 非法")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	meta, _, err := parseRecordingMetaJSON(script.RecordingMetaJSON)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	authContext := meta.AuthContext
	if authContext == "" {
		authContext = authContextClean
	}

	llmConfig, err := h.loadGenerationLLMConfig(req.LLMConfigID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	stdout, err := playbot.GenerateTestPlan(c.Request.Context(), playbot.GenerateOptions{
		PageURL:         buildPageURL(version.BaseURL, page.Path),
		Snapshot:        snapshot,
		IntentPlan:      intentPlan,
		PageDescription: page.Description,
		Instruction:     req.Instruction,
		AuthContext:     authContext,
		LLMEndpoint:     llmConfig.BaseURL,
		LLMAPIKey:       llmConfig.APIKey,
		LLMModel:        llmConfig.Model,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	blueprints, err := parsePlaybotGeneratedCases(stdout, authContext)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if mode == "preview" {
		previewCases := make([]gin.H, 0, len(blueprints))
		for _, blueprint := range blueprints {
			previewCases = append(previewCases, gin.H{
				"title":       blueprint.Title,
				"description": blueprint.Description,
				"blueprint":   blueprint.Raw,
			})
		}
		c.JSON(http.StatusOK, gin.H{
			"mode":            mode,
			"saved":           false,
			"generated_count": len(previewCases),
			"test_cases":      previewCases,
		})
		return
	}

	savedCases, err := saveGeneratedTestCases(h.gormDB(), page.ID, mode, blueprints)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"mode":            mode,
		"saved":           true,
		"generated_count": len(savedCases),
		"test_cases":      savedCases,
	})
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
	if h.store == nil {
		return nil, fmt.Errorf("LLM 配置存储未初始化")
	}

	var (
		cfg *models.LLMConfigModel
		err error
	)
	if strings.TrimSpace(id) != "" {
		cfg, err = h.store.GetLLMConfig(strings.TrimSpace(id))
	} else {
		cfg, err = h.store.GetDefaultLLMConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("LLM 配置不存在或未启用")
	}
	if !cfg.IsActive {
		return nil, fmt.Errorf("LLM 配置未启用")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("LLM 配置缺少 API Key")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("LLM 配置缺少模型")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, fmt.Errorf("LLM 配置缺少 endpoint/base URL")
	}
	return cfg, nil
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
