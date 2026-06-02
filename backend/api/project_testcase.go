package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/storage"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const defaultTestCaseStatus = "active"

var allowedTestCaseStatuses = map[string]bool{
	"active":   true,
	"draft":    true,
	"archived": true,
}

type testCaseMutationRequest struct {
	Title         *string         `json:"title"`
	Description   *string         `json:"description"`
	Status        *string         `json:"status"`
	Blueprint     json.RawMessage `json:"blueprint"`
	ScriptContent *string         `json:"script_content"`
}

type testCaseSummaryResponse struct {
	ID          uint      `json:"id"`
	PageID      uint      `json:"page_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type testCaseDetailResponse struct {
	ID            uint           `json:"id"`
	PageID        uint           `json:"page_id"`
	Title         string         `json:"title"`
	Description   string         `json:"description"`
	Status        string         `json:"status"`
	Blueprint     map[string]any `json:"blueprint"`
	ScriptContent string         `json:"script_content"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

func (h *ProjectHandlers) ListTestCases(c *gin.Context) {
	_, _, pageID, ok := parseProjectVersionPageIDs(c)
	if !ok {
		return
	}
	if _, _, err := loadGenerationPageContextFromContext(c); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, or page not found"})
		return
	}

	var testCases []models.TestCase
	if err := storage.DB.Where("page_id = ?", pageID).Order("updated_at desc, id desc").Find(&testCases).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	summaries := make([]testCaseSummaryResponse, 0, len(testCases))
	for _, testCase := range testCases {
		summaries = append(summaries, toTestCaseSummary(testCase))
	}

	c.JSON(http.StatusOK, gin.H{
		"test_cases": summaries,
		"count":      len(summaries),
	})
}

func (h *ProjectHandlers) CreateTestCase(c *gin.Context) {
	_, _, pageID, ok := parseProjectVersionPageIDs(c)
	if !ok {
		return
	}
	if _, _, err := loadGenerationPageContextFromContext(c); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, or page not found"})
		return
	}

	var req testCaseMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	finalCase, err := buildNewTestCase(pageID, req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := storage.DB.Create(&finalCase).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	detail, err := toTestCaseDetail(finalCase)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"test_case": detail})
}

func (h *ProjectHandlers) GetTestCase(c *gin.Context) {
	_, _, pageID, testCaseID, ok := parseProjectVersionPageTestCaseIDs(c)
	if !ok {
		return
	}
	if _, _, err := loadGenerationPageContextFromContext(c); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, or testcase not found"})
		return
	}

	testCase, err := loadTestCaseForPage(pageID, testCaseID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, or testcase not found"})
		return
	}
	detail, err := toTestCaseDetail(testCase)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"test_case": detail})
}

func (h *ProjectHandlers) UpdateTestCase(c *gin.Context) {
	_, _, pageID, testCaseID, ok := parseProjectVersionPageTestCaseIDs(c)
	if !ok {
		return
	}
	if _, _, err := loadGenerationPageContextFromContext(c); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, or testcase not found"})
		return
	}

	var req testCaseMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	var saved models.TestCase
	err := storage.DB.Transaction(func(tx *gorm.DB) error {
		testCase, err := loadTestCaseForPageTx(tx, pageID, testCaseID)
		if err != nil {
			return err
		}
		if err := applyTestCaseUpdate(&testCase, req); err != nil {
			return err
		}
		if err := tx.Save(&testCase).Error; err != nil {
			return err
		}
		saved = testCase
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, or testcase not found"})
			return
		}
		if isTestCaseValidationError(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	detail, err := toTestCaseDetail(saved)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"test_case": detail})
}

func (h *ProjectHandlers) DeleteTestCase(c *gin.Context) {
	_, _, pageID, testCaseID, ok := parseProjectVersionPageTestCaseIDs(c)
	if !ok {
		return
	}
	if _, _, err := loadGenerationPageContextFromContext(c); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, or testcase not found"})
		return
	}

	err := storage.DB.Transaction(func(tx *gorm.DB) error {
		testCase, err := loadTestCaseForPageTx(tx, pageID, testCaseID)
		if err != nil {
			return err
		}
		return tx.Delete(&testCase).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, or testcase not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "TestCase deleted successfully"})
}

func loadGenerationPageContextFromContext(c *gin.Context) (models.ProjectVersion, models.TestPage, error) {
	projectID, versionID, pageID, ok := parseProjectVersionPageIDs(c)
	if !ok {
		return models.ProjectVersion{}, models.TestPage{}, fmt.Errorf("invalid path parameters")
	}
	return loadGenerationPageContext(projectID, versionID, pageID)
}

func parseProjectVersionPageTestCaseIDs(c *gin.Context) (uint, uint, uint, uint, bool) {
	projectID, versionID, pageID, ok := parseProjectVersionPageIDs(c)
	if !ok {
		return 0, 0, 0, 0, false
	}
	testCaseID, err := parseUintParam(c, "tcid", "Invalid TestCase ID")
	if err != nil {
		return 0, 0, 0, 0, false
	}
	return projectID, versionID, pageID, testCaseID, true
}

func loadTestCaseForPage(pageID, testCaseID uint) (models.TestCase, error) {
	return loadTestCaseForPageTx(storage.DB, pageID, testCaseID)
}

func loadTestCaseForPageTx(db *gorm.DB, pageID, testCaseID uint) (models.TestCase, error) {
	var testCase models.TestCase
	err := db.Where("id = ? AND page_id = ?", testCaseID, pageID).First(&testCase).Error
	return testCase, err
}

func buildNewTestCase(pageID uint, req testCaseMutationRequest) (models.TestCase, error) {
	title, err := requireRequestTitle(req.Title)
	if err != nil {
		return models.TestCase{}, err
	}
	description := optionalString(req.Description)
	status, err := normalizeTestCaseStatus(req.Status)
	if err != nil {
		return models.TestCase{}, err
	}
	blueprint, err := normalizeRequestBlueprint(req.Blueprint, title, description, status, true)
	if err != nil {
		return models.TestCase{}, err
	}

	return models.TestCase{
		PageID:        pageID,
		Title:         title,
		Description:   description,
		Status:        status,
		Blueprint:     blueprint,
		ScriptContent: optionalString(req.ScriptContent),
	}, nil
}

func applyTestCaseUpdate(testCase *models.TestCase, req testCaseMutationRequest) error {
	title := testCase.Title
	if req.Title != nil {
		nextTitle, err := requireRequestTitle(req.Title)
		if err != nil {
			return err
		}
		title = nextTitle
	}

	description := testCase.Description
	if req.Description != nil {
		description = *req.Description
	}

	status := testCase.Status
	if req.Status != nil {
		nextStatus, err := normalizeTestCaseStatus(req.Status)
		if err != nil {
			return err
		}
		status = nextStatus
	}

	rawBlueprint := req.Blueprint
	if len(rawBlueprint) == 0 {
		rawBlueprint = []byte(testCase.Blueprint)
	}
	blueprint, err := normalizeRequestBlueprint(rawBlueprint, title, description, status, true)
	if err != nil {
		return err
	}

	testCase.Title = title
	testCase.Description = description
	testCase.Status = status
	testCase.Blueprint = blueprint
	if req.ScriptContent != nil {
		testCase.ScriptContent = *req.ScriptContent
	}
	return nil
}

func requireRequestTitle(title *string) (string, error) {
	if title == nil {
		return "", testCaseValidationError("Title is required")
	}
	trimmed := strings.TrimSpace(*title)
	if trimmed == "" {
		return "", testCaseValidationError("Title is required")
	}
	return trimmed, nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func normalizeTestCaseStatus(status *string) (string, error) {
	if status == nil || strings.TrimSpace(*status) == "" {
		return defaultTestCaseStatus, nil
	}
	trimmed := strings.TrimSpace(*status)
	if !allowedTestCaseStatuses[trimmed] {
		return "", testCaseValidationError("Invalid TestCase status")
	}
	return trimmed, nil
}

func normalizeRequestBlueprint(raw json.RawMessage, title, description, status string, required bool) (string, error) {
	if len(raw) == 0 {
		if required {
			return "", testCaseValidationError("Blueprint is required")
		}
		return "", nil
	}

	var blueprint map[string]any
	if err := json.Unmarshal(raw, &blueprint); err != nil {
		return "", testCaseValidationError("Blueprint JSON invalid")
	}
	if len(blueprint) == 0 {
		return "", testCaseValidationError("Blueprint must be a JSON object")
	}
	if status == "active" && !hasNonEmptySteps(blueprint) {
		return "", testCaseValidationError("Active TestCase blueprint must contain non-empty steps")
	}

	blueprint["title"] = title
	blueprint["description"] = description
	data, err := json.Marshal(blueprint)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func hasNonEmptySteps(blueprint map[string]any) bool {
	steps, ok := blueprint["steps"].([]any)
	return ok && len(steps) > 0
}

func toTestCaseSummary(testCase models.TestCase) testCaseSummaryResponse {
	return testCaseSummaryResponse{
		ID:          testCase.ID,
		PageID:      testCase.PageID,
		Title:       testCase.Title,
		Description: testCase.Description,
		Status:      testCase.Status,
		CreatedAt:   testCase.CreatedAt,
		UpdatedAt:   testCase.UpdatedAt,
	}
}

func toTestCaseDetail(testCase models.TestCase) (testCaseDetailResponse, error) {
	var blueprint map[string]any
	if err := json.Unmarshal([]byte(testCase.Blueprint), &blueprint); err != nil {
		return testCaseDetailResponse{}, fmt.Errorf("TestCase blueprint data is corrupted: %w", err)
	}
	if len(blueprint) == 0 {
		return testCaseDetailResponse{}, fmt.Errorf("TestCase blueprint data is corrupted")
	}

	return testCaseDetailResponse{
		ID:            testCase.ID,
		PageID:        testCase.PageID,
		Title:         testCase.Title,
		Description:   testCase.Description,
		Status:        testCase.Status,
		Blueprint:     blueprint,
		ScriptContent: testCase.ScriptContent,
		CreatedAt:     testCase.CreatedAt,
		UpdatedAt:     testCase.UpdatedAt,
	}, nil
}

type testCaseValidationError string

func (e testCaseValidationError) Error() string {
	return string(e)
}

func isTestCaseValidationError(err error) bool {
	var validationErr testCaseValidationError
	return errors.As(err, &validationErr)
}
