package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/services/playbot"
	"github.com/browserwing/browserwing/storage"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	refinementStatusProposed  = "proposed"
	refinementStatusApplied   = "applied"
	refinementStatusDiscarded = "discarded"
)

type refineTestCaseRequest struct {
	Prompt      string `json:"prompt"`
	LLMConfigID string `json:"llm_config_id"`
	ExecutionID *uint  `json:"execution_id"`
}

type playbotRefineOutput struct {
	RefinedBlueprint json.RawMessage `json:"refined_blueprint"`
	Summary          string          `json:"summary"`
	RiskNotes        string          `json:"risk_notes"`
	Error            any             `json:"error"`
}

type refinementSummaryResponse struct {
	ID         uint       `json:"id"`
	TestCaseID uint       `json:"test_case_id"`
	UserPrompt string     `json:"user_prompt"`
	Summary    string     `json:"summary"`
	RiskNotes  string     `json:"risk_notes"`
	Status     string     `json:"status"`
	AppliedAt  *time.Time `json:"applied_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type refinementDetailResponse struct {
	ID                uint           `json:"id"`
	TestCaseID        uint           `json:"test_case_id"`
	UserPrompt        string         `json:"user_prompt"`
	Summary           string         `json:"summary"`
	RiskNotes         string         `json:"risk_notes"`
	Status            string         `json:"status"`
	OriginalBlueprint map[string]any `json:"original_blueprint"`
	RefinedBlueprint  map[string]any `json:"refined_blueprint"`
	AppliedAt         *time.Time     `json:"applied_at"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type refinementStatusResponse struct {
	ID         uint       `json:"id"`
	TestCaseID uint       `json:"test_case_id"`
	Status     string     `json:"status"`
	AppliedAt  *time.Time `json:"applied_at"`
}

func (h *ProjectHandlers) RefineTestCase(c *gin.Context) {
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

	var req refineTestCaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Prompt is required"})
		return
	}

	currentBlueprint, originalBlueprintJSON, err := normalizeStoredBlueprint(testCase.Blueprint)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "TestCase blueprint data is corrupted"})
		return
	}

	snapshot, intentPlan, warnings, err := loadRefinementPageContext(page.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	executionReport, err := h.loadRefinementExecutionReport(testCase.ID, req.ExecutionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Execution context not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	llmConfig, err := h.loadGenerationLLMConfig(req.LLMConfigID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	stdout, err := playbot.RefineTestCase(c.Request.Context(), playbot.RefineOptions{
		PageURL:          buildPageURL(version.BaseURL, page.Path),
		PageDescription:  page.Description,
		CurrentBlueprint: currentBlueprint,
		UserPrompt:       prompt,
		Snapshot:         snapshot,
		IntentPlan:       intentPlan,
		ExecutionReport:  executionReport,
		ContextWarnings:  warnings,
		LLMEndpoint:      llmConfig.BaseURL,
		LLMAPIKey:        llmConfig.APIKey,
		LLMModel:         llmConfig.Model,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	refinedBlueprint, refinedBlueprintJSON, summary, riskNotes, err := parsePlaybotRefineOutput(stdout, testCase.Status)
	if err != nil {
		if isPlaybotRefineFailure(err) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_ = refinedBlueprint

	refinement := models.LLMRefinement{
		TestCaseID:        testCase.ID,
		UserPrompt:        prompt,
		OriginalBlueprint: originalBlueprintJSON,
		RefinedBlueprint:  refinedBlueprintJSON,
		Summary:           summary,
		RiskNotes:         riskNotes,
		Status:            refinementStatusProposed,
	}
	if err := storage.DB.Create(&refinement).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	detail, err := toRefinementDetail(refinement)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"refinement": detail})
}

func (h *ProjectHandlers) ListTestCaseRefinements(c *gin.Context) {
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

	var refinements []models.LLMRefinement
	if err := storage.DB.Where("test_case_id = ?", testCaseID).Order("created_at desc, id desc").Find(&refinements).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	items := make([]refinementSummaryResponse, 0, len(refinements))
	for _, refinement := range refinements {
		items = append(items, toRefinementSummary(refinement))
	}
	c.JSON(http.StatusOK, gin.H{"refinements": items, "count": len(items)})
}

func (h *ProjectHandlers) GetTestCaseRefinement(c *gin.Context) {
	_, _, pageID, testCaseID, refinementID, ok := parseProjectVersionPageTestCaseRefinementIDs(c)
	if !ok {
		return
	}
	if _, _, err := loadGenerationPageContextFromContext(c); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, testcase, or refinement not found"})
		return
	}
	if _, err := loadTestCaseForPage(pageID, testCaseID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, testcase, or refinement not found"})
		return
	}
	refinement, err := loadRefinementForTestCase(testCaseID, refinementID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, testcase, or refinement not found"})
		return
	}
	detail, err := toRefinementDetail(refinement)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"refinement": detail})
}

func (h *ProjectHandlers) ApplyTestCaseRefinement(c *gin.Context) {
	_, _, pageID, testCaseID, refinementID, ok := parseProjectVersionPageTestCaseRefinementIDs(c)
	if !ok {
		return
	}
	if _, _, err := loadGenerationPageContextFromContext(c); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, testcase, or refinement not found"})
		return
	}

	var savedCase models.TestCase
	var savedRefinement models.LLMRefinement
	err := storage.DB.Transaction(func(tx *gorm.DB) error {
		testCase, err := loadTestCaseForPageTx(tx, pageID, testCaseID)
		if err != nil {
			return err
		}
		refinement, err := loadRefinementForTestCaseTx(tx, testCase.ID, refinementID)
		if err != nil {
			return err
		}
		if refinement.Status != refinementStatusProposed {
			return testCaseValidationError("Only proposed refinement can be applied")
		}
		if !blueprintEquivalent(testCase.Blueprint, refinement.OriginalBlueprint) {
			return staleRefinementError("Refinement is stale; reload the TestCase and generate a new suggestion")
		}

		refined, err := parseBlueprintObjectString(refinement.RefinedBlueprint, "Refined blueprint data is corrupted")
		if err != nil {
			return err
		}
		title, description, err := validateRefinedBlueprintForStatus(refined, testCase.Status)
		if err != nil {
			return err
		}
		refinedJSON, err := normalizeBlueprintObject(refined)
		if err != nil {
			return err
		}

		testCase.Title = title
		testCase.Description = description
		testCase.Blueprint = refinedJSON
		if err := tx.Save(&testCase).Error; err != nil {
			return err
		}
		now := time.Now()
		refinement.Status = refinementStatusApplied
		refinement.AppliedAt = &now
		if err := tx.Save(&refinement).Error; err != nil {
			return err
		}
		savedCase = testCase
		savedRefinement = refinement
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, testcase, or refinement not found"})
			return
		}
		if isStaleRefinementError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		if isTestCaseValidationError(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	detail, err := toTestCaseDetail(savedCase)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"test_case":  detail,
		"refinement": toRefinementStatus(savedRefinement),
	})
}

func (h *ProjectHandlers) DiscardTestCaseRefinement(c *gin.Context) {
	_, _, pageID, testCaseID, refinementID, ok := parseProjectVersionPageTestCaseRefinementIDs(c)
	if !ok {
		return
	}
	if _, _, err := loadGenerationPageContextFromContext(c); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, testcase, or refinement not found"})
		return
	}

	var savedRefinement models.LLMRefinement
	err := storage.DB.Transaction(func(tx *gorm.DB) error {
		testCase, err := loadTestCaseForPageTx(tx, pageID, testCaseID)
		if err != nil {
			return err
		}
		refinement, err := loadRefinementForTestCaseTx(tx, testCase.ID, refinementID)
		if err != nil {
			return err
		}
		if refinement.Status != refinementStatusProposed {
			return testCaseValidationError("Only proposed refinement can be discarded")
		}
		refinement.Status = refinementStatusDiscarded
		if err := tx.Save(&refinement).Error; err != nil {
			return err
		}
		savedRefinement = refinement
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, testcase, or refinement not found"})
			return
		}
		if isTestCaseValidationError(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"refinement": toRefinementStatus(savedRefinement)})
}

func parseProjectVersionPageTestCaseRefinementIDs(c *gin.Context) (uint, uint, uint, uint, uint, bool) {
	projectID, versionID, pageID, testCaseID, ok := parseProjectVersionPageTestCaseIDs(c)
	if !ok {
		return 0, 0, 0, 0, 0, false
	}
	refinementID, err := parseUintParam(c, "rid", "Invalid Refinement ID")
	if err != nil {
		return 0, 0, 0, 0, 0, false
	}
	return projectID, versionID, pageID, testCaseID, refinementID, true
}

func loadRefinementForTestCase(testCaseID, refinementID uint) (models.LLMRefinement, error) {
	return loadRefinementForTestCaseTx(storage.DB, testCaseID, refinementID)
}

func loadRefinementForTestCaseTx(db *gorm.DB, testCaseID, refinementID uint) (models.LLMRefinement, error) {
	var refinement models.LLMRefinement
	err := db.Where("id = ? AND test_case_id = ?", refinementID, testCaseID).First(&refinement).Error
	return refinement, err
}

func (h *ProjectHandlers) loadRefinementExecutionReport(testCaseID uint, executionID *uint) (any, error) {
	if executionID == nil {
		return nil, nil
	}
	var execution models.TestExecution
	if err := storage.DB.Where("id = ? AND test_case_id = ?", *executionID, testCaseID).First(&execution).Error; err != nil {
		return nil, err
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(execution.ReportData), &report); err != nil {
		return nil, fmt.Errorf("Execution report data is corrupted: %w", err)
	}
	return report, nil
}

func loadRefinementPageContext(pageID uint) (snapshot any, intentPlan any, warnings []map[string]string, err error) {
	var script models.PageScript
	if err := storage.DB.Where("page_id = ?", pageID).Order("created_at desc, id desc").First(&script).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, []map[string]string{{"code": "missing_page_script", "message": "No main flow recording is available"}}, nil
		}
		return nil, nil, nil, fmt.Errorf("load page script context failed: %w", err)
	}

	snapshot, err = parseOptionalContextJSON(script.DOMSnapshot)
	if err != nil {
		warnings = append(warnings, map[string]string{"code": "invalid_snapshot", "message": "Latest page snapshot is not valid JSON"})
	}
	intentPlan, err = parseOptionalContextJSON(script.ActionTrace)
	if err != nil {
		warnings = append(warnings, map[string]string{"code": "invalid_intent_plan", "message": "Latest action trace is not valid JSON"})
	}
	return snapshot, intentPlan, warnings, nil
}

func parseOptionalContextJSON(raw string) (any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("context is empty")
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func parsePlaybotRefineOutput(stdout string, status string) (map[string]any, string, string, string, error) {
	var output playbotRefineOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		return nil, "", "", "", fmt.Errorf("Playbot 输出不是合法 JSON")
	}
	if output.Error != nil {
		return nil, "", "", "", playbotRefineFailureError(fmt.Sprintf("Playbot 返回错误: %v", output.Error))
	}
	if len(output.RefinedBlueprint) == 0 {
		return nil, "", "", "", fmt.Errorf("Playbot 未返回 refined_blueprint")
	}
	if strings.TrimSpace(output.Summary) == "" {
		return nil, "", "", "", fmt.Errorf("Playbot summary 不能为空")
	}
	var refined map[string]any
	if err := json.Unmarshal(output.RefinedBlueprint, &refined); err != nil {
		return nil, "", "", "", fmt.Errorf("Playbot refined_blueprint 不是 JSON object")
	}
	if len(refined) == 0 {
		return nil, "", "", "", fmt.Errorf("Playbot refined_blueprint 不能为空")
	}
	if _, _, err := validateRefinedBlueprintForStatus(refined, status); err != nil {
		return nil, "", "", "", err
	}
	refinedJSON, err := normalizeBlueprintObject(refined)
	if err != nil {
		return nil, "", "", "", err
	}
	return refined, refinedJSON, strings.TrimSpace(output.Summary), output.RiskNotes, nil
}

func validateRefinedBlueprintForStatus(blueprint map[string]any, status string) (string, string, error) {
	title, ok := blueprint["title"].(string)
	if !ok || strings.TrimSpace(title) == "" {
		return "", "", testCaseValidationError("Refined blueprint title is required")
	}
	description, ok := blueprint["description"].(string)
	if !ok {
		return "", "", testCaseValidationError("Refined blueprint description is required")
	}
	if status == "active" && !hasNonEmptySteps(blueprint) {
		return "", "", testCaseValidationError("Active TestCase blueprint must contain non-empty steps")
	}
	blueprint["title"] = strings.TrimSpace(title)
	blueprint["description"] = description
	return strings.TrimSpace(title), description, nil
}

func normalizeStoredBlueprint(raw string) (map[string]any, string, error) {
	blueprint, err := parseBlueprintObjectString(raw, "TestCase blueprint data is corrupted")
	if err != nil {
		return nil, "", err
	}
	normalized, err := normalizeBlueprintObject(blueprint)
	if err != nil {
		return nil, "", err
	}
	return blueprint, normalized, nil
}

func parseBlueprintObjectString(raw string, message string) (map[string]any, error) {
	var blueprint map[string]any
	if err := json.Unmarshal([]byte(raw), &blueprint); err != nil {
		return nil, fmt.Errorf("%s: %w", message, err)
	}
	if len(blueprint) == 0 {
		return nil, fmt.Errorf("%s", message)
	}
	return blueprint, nil
}

func normalizeBlueprintObject(blueprint map[string]any) (string, error) {
	data, err := json.Marshal(blueprint)
	if err != nil {
		return "", err
	}
	var normalized map[string]any
	if err := json.Unmarshal(data, &normalized); err != nil {
		return "", err
	}
	normalizedData, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(normalizedData), nil
}

func blueprintEquivalent(left string, right string) bool {
	leftObj, leftJSON, err := normalizeStoredBlueprint(left)
	if err != nil {
		return false
	}
	_ = leftObj
	rightObj, rightJSON, err := normalizeStoredBlueprint(right)
	if err != nil {
		return false
	}
	_ = rightObj
	return leftJSON == rightJSON
}

func toRefinementSummary(refinement models.LLMRefinement) refinementSummaryResponse {
	return refinementSummaryResponse{
		ID:         refinement.ID,
		TestCaseID: refinement.TestCaseID,
		UserPrompt: refinement.UserPrompt,
		Summary:    refinement.Summary,
		RiskNotes:  refinement.RiskNotes,
		Status:     refinement.Status,
		AppliedAt:  refinement.AppliedAt,
		CreatedAt:  refinement.CreatedAt,
		UpdatedAt:  refinement.UpdatedAt,
	}
}

func toRefinementDetail(refinement models.LLMRefinement) (refinementDetailResponse, error) {
	original, err := parseBlueprintObjectString(refinement.OriginalBlueprint, "LLMRefinement original_blueprint data is corrupted")
	if err != nil {
		return refinementDetailResponse{}, err
	}
	refined, err := parseBlueprintObjectString(refinement.RefinedBlueprint, "LLMRefinement refined_blueprint data is corrupted")
	if err != nil {
		return refinementDetailResponse{}, err
	}
	return refinementDetailResponse{
		ID:                refinement.ID,
		TestCaseID:        refinement.TestCaseID,
		UserPrompt:        refinement.UserPrompt,
		Summary:           refinement.Summary,
		RiskNotes:         refinement.RiskNotes,
		Status:            refinement.Status,
		OriginalBlueprint: original,
		RefinedBlueprint:  refined,
		AppliedAt:         refinement.AppliedAt,
		CreatedAt:         refinement.CreatedAt,
		UpdatedAt:         refinement.UpdatedAt,
	}, nil
}

func toRefinementStatus(refinement models.LLMRefinement) refinementStatusResponse {
	return refinementStatusResponse{
		ID:         refinement.ID,
		TestCaseID: refinement.TestCaseID,
		Status:     refinement.Status,
		AppliedAt:  refinement.AppliedAt,
	}
}

type staleRefinementError string

func (e staleRefinementError) Error() string {
	return string(e)
}

func isStaleRefinementError(err error) bool {
	var stale staleRefinementError
	return errors.As(err, &stale)
}

type playbotRefineFailureError string

func (e playbotRefineFailureError) Error() string {
	return string(e)
}

func isPlaybotRefineFailure(err error) bool {
	var failure playbotRefineFailureError
	return errors.As(err, &failure)
}
