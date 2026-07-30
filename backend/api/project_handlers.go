package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/browserwing/browserwing/config"
	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/pkg/logger"
	"github.com/browserwing/browserwing/storage"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ProjectHandlers 包含了项目和版本相关的 API 处理器
type ProjectHandlers struct {
	store          storage.Store
	config         *config.Config
	testCaseRunner *testCaseRunnerHolder
	projectAuth    *projectAuthRuntimeHolder
	lifecycle      *RecordingLifecycleService
	recovery       *RecordingRecoveryCoordinator
	playbotAgent   *playbotAgentHolder
	playbotRuns    *playbotRunHub
}

// NewProjectHandlers 创建处理器实例
func NewProjectHandlers(store storage.Store, cfg *config.Config, runnerHolder *testCaseRunnerHolder, authHolder *projectAuthRuntimeHolder, agentHolder *playbotAgentHolder, runHub *playbotRunHub, lifecycle *RecordingLifecycleService, recovery ...*RecordingRecoveryCoordinator) *ProjectHandlers {
	var holder *testCaseRunnerHolder
	holder = runnerHolder
	var coordinator *RecordingRecoveryCoordinator
	if len(recovery) > 0 {
		coordinator = recovery[0]
	}
	return &ProjectHandlers{
		store:          store,
		config:         cfg,
		testCaseRunner: holder,
		projectAuth:    authHolder,
		lifecycle:      lifecycle,
		recovery:       coordinator,
		playbotAgent:   agentHolder,
		playbotRuns:    runHub,
	}
}

func (h *ProjectHandlers) recordingRecoveryCoordinator() *RecordingRecoveryCoordinator {
	if h.recovery != nil {
		return h.recovery
	}
	return NewRecordingRecoveryCoordinator(h.recordingLifecycleService(), nil)
}

func (h *ProjectHandlers) gormDB() *gorm.DB {
	return h.store.GormDB()
}

func (h *ProjectHandlers) writeRecordingLifecycleResult(c *gin.Context, result recordingLifecycleResult, err error) {
	if err == nil {
		c.JSON(result.Status, result.Body)
		return
	}
	if lifecycle, ok := err.(*recordingLifecycleError); ok {
		if lifecycle.RetryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(lifecycle.RetryAfter))
		}
		c.JSON(lifecycle.Status, gin.H{"error": lifecycle.Detail, "code": lifecycle.Code})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "recording lifecycle failed", "code": "recording_lifecycle_store_failed"})
}

func (h *ProjectHandlers) ListProjects(c *gin.Context) {
	var projects []models.Project
	if err := h.gormDB().Preload("Versions").Order("created_at desc").Find(&projects).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, projects)
}

func (h *ProjectHandlers) CreateProject(c *gin.Context) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		BaseURL     string `json:"base_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Project name is required"})
		return
	}

	project := models.Project{
		Name:        req.Name,
		Description: req.Description,
	}

	if err := h.gormDB().Create(&project).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 自动创建一个默认版本 v1.0.0
	defaultVersion := models.ProjectVersion{
		ProjectID:   project.ID,
		VersionName: "v1.0.0",
		Description: "Default initial version",
		BaseURL:     req.BaseURL,
	}
	h.gormDB().Create(&defaultVersion)

	c.JSON(http.StatusOK, project)
}

func (h *ProjectHandlers) DeleteProject(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID"})
		return
	}

	err = h.gormDB().Transaction(func(tx *gorm.DB) error {
		projectID := uint(id)
		if err := tx.Where("project_id = ?", projectID).Delete(&models.ProjectAuthState{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Project{}, projectID).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Project deleted successfully"})
}

func (h *ProjectHandlers) CreateVersion(c *gin.Context) {
	idStr := c.Param("id")
	projectID, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Project ID"})
		return
	}

	var req models.ProjectVersion
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	req.ProjectID = uint(projectID)

	if strings.TrimSpace(req.VersionName) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Version name is required"})
		return
	}

	if err := h.gormDB().Create(&req).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, req)
}

func (h *ProjectHandlers) UpdateVersion(c *gin.Context) {
	vidStr := c.Param("vid")
	versionID, err := strconv.ParseUint(vidStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Version ID"})
		return
	}

	var req struct {
		VersionName string `json:"version_name"`
		Description string `json:"description"`
		BaseURL     string `json:"base_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	var version models.ProjectVersion
	if err := h.gormDB().First(&version, versionID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Version not found"})
		return
	}

	if strings.TrimSpace(req.VersionName) != "" {
		version.VersionName = req.VersionName
	}
	version.Description = req.Description
	version.BaseURL = req.BaseURL

	if err := h.gormDB().Save(&version).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, version)
}

func (h *ProjectHandlers) DeleteVersion(c *gin.Context) {
	idStr := c.Param("id")
	projectID, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Project ID"})
		return
	}
	vidStr := c.Param("vid")
	versionID, err := strconv.ParseUint(vidStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Version ID"})
		return
	}

	var version models.ProjectVersion
	if err := h.gormDB().Where("id = ? AND project_id = ?", uint(versionID), uint(projectID)).First(&version).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Version not found"})
		return
	}

	if err := h.gormDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("project_id = ? AND version_id = ?", uint(projectID), uint(versionID)).
			Delete(&models.ProjectAuthState{}).Error; err != nil {
			return err
		}
		return tx.Delete(&version).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Version deleted successfully"})
}

func (h *ProjectHandlers) CloneVersion(c *gin.Context) {
	vidStr := c.Param("vid")
	versionID, err := strconv.ParseUint(vidStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Version ID"})
		return
	}

	var req struct {
		NewVersionName string `json:"new_version_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	tx := h.gormDB().Begin()
	if tx.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start clone transaction"})
		return
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var sourceVersion models.ProjectVersion
	if err := tx.First(&sourceVersion, versionID).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusNotFound, gin.H{"error": "Source version not found"})
		return
	}

	rollbackStoreFailure := func() {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clone version"})
	}

	// 1. 创建新版本
	newVersion := models.ProjectVersion{
		ProjectID:   sourceVersion.ProjectID,
		VersionName: req.NewVersionName,
		Description: "Cloned from " + sourceVersion.VersionName,
	}
	if err := tx.Create(&newVersion).Error; err != nil {
		rollbackStoreFailure()
		return
	}

	// 2. 深度克隆 Pages
	var pages []models.TestPage
	if err := tx.Where("version_id = ?", sourceVersion.ID).Find(&pages).Error; err != nil {
		rollbackStoreFailure()
		return
	}
	normalizer := NewRecordingNormalizer()

	for _, p := range pages {
		newPage := models.TestPage{
			VersionID:   newVersion.ID,
			Name:        p.Name,
			Path:        p.Path,
			Description: p.Description,
		}
		if err := tx.Create(&newPage).Error; err != nil {
			rollbackStoreFailure()
			return
		}

		// 3. 深度克隆 PageScripts
		var scripts []models.PageScript
		if err := tx.Where("page_id = ?", p.ID).Find(&scripts).Error; err != nil {
			rollbackStoreFailure()
			return
		}
		for _, s := range scripts {
			normalized, err := normalizer.NormalizePageScript(s)
			if err != nil {
				tx.Rollback()
				c.JSON(http.StatusUnprocessableEntity, gin.H{
					"error":                 "recording source is invalid",
					"code":                  "recording_source_invalid",
					"source_page_id":        p.ID,
					"source_page_script_id": s.ID,
					"reason":                "page_script_normalization_failed",
				})
				return
			}
			newScript := models.PageScript{
				PageID:                newPage.ID,
				Name:                  s.Name,
				ActionTrace:           normalized.ActionsJSON,
				DOMSnapshot:           normalized.DOMSnapshot,
				RecordingMetaJSON:     normalized.RecordingMetaJSON,
				PageScriptContentHash: normalized.PageScriptContentHash,
				NormalizerVersion:     normalized.NormalizerVersion,
			}
			if err := tx.Create(&newScript).Error; err != nil {
				rollbackStoreFailure()
				return
			}
		}

		// 4. 深度克隆 TestCases
		var cases []models.TestCase
		if err := tx.Where("page_id = ?", p.ID).Find(&cases).Error; err != nil {
			rollbackStoreFailure()
			return
		}
		for _, c1 := range cases {
			newCase := models.TestCase{
				PageID:        newPage.ID,
				Title:         c1.Title,
				Description:   c1.Description,
				Blueprint:     c1.Blueprint,
				ScriptContent: c1.ScriptContent,
				Status:        c1.Status,
			}
			if err := tx.Create(&newCase).Error; err != nil {
				rollbackStoreFailure()
				return
			}
		}
	}

	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to commit transaction"})
		return
	}

	logger.Info(c.Request.Context(), "Successfully cloned version %d to new version %d (%s)", versionID, newVersion.ID, newVersion.VersionName)
	c.JSON(http.StatusOK, newVersion)
}

// ---- TestPage Handlers ----

func (h *ProjectHandlers) ListPages(c *gin.Context) {
	vidStr := c.Param("vid")
	versionID, err := strconv.ParseUint(vidStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Version ID"})
		return
	}

	var pages []models.TestPage
	// 预加载关联的 Scripts 和 TestCases 以便前端展示统计信息
	if err := h.gormDB().Preload("Scripts").Preload("TestCases", func(db *gorm.DB) *gorm.DB {
		return db.Select("id", "page_id", "title", "description", "status", "created_at", "updated_at").Order("updated_at desc, id desc")
	}).Where("version_id = ?", versionID).Order("created_at desc").Find(&pages).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, pages)
}

func (h *ProjectHandlers) CreatePage(c *gin.Context) {
	vidStr := c.Param("vid")
	versionID, err := strconv.ParseUint(vidStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Version ID"})
		return
	}

	var req models.TestPage
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	req.VersionID = uint(versionID)

	if strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Page name is required"})
		return
	}

	if err := h.gormDB().Create(&req).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, req)
}

func (h *ProjectHandlers) DeletePage(c *gin.Context) {
	pidStr := c.Param("pid")
	pageID, err := strconv.ParseUint(pidStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Page ID"})
		return
	}

	// 级联删除会清理对应的 PageScript 和 TestCase
	if err := h.gormDB().Delete(&models.TestPage{}, pageID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Page deleted successfully"})
}

func (h *ProjectHandlers) SavePageRecording(c *gin.Context) {
	projectID, versionID, pageID, ok := parseProjectVersionPageIDs(c)
	if !ok {
		return
	}
	if _, _, err := loadGenerationPageContext(h.gormDB(), projectID, versionID, pageID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, or page not found"})
		return
	}

	var req struct {
		OperationID        string          `json:"operation_id"`
		Name               string          `json:"name"`
		RecordingMeta      json.RawMessage `json:"recording_meta"`
		RecordingSessionID string          `json:"recording_session_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	if strings.TrimSpace(req.RecordingSessionID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recording_session_id is required", "code": "legacy_recording_route_removed"})
		return
	}
	session, err := h.loadRecordingSessionByID(req.RecordingSessionID)
	if err != nil || session.ProjectID != projectID || session.VersionID != versionID || session.PageID != pageID {
		c.JSON(http.StatusNotFound, gin.H{"error": "RecordingSession not found", "code": "recording_session_not_found"})
		return
	}
	result, lifecycleErr := h.recordingLifecycleService().Save(c.Request.Context(), saveRecordingLifecycleInput{OperationID: req.OperationID, Session: session, Name: req.Name, RecordingMeta: req.RecordingMeta})
	h.writeRecordingLifecycleResult(c, result, lifecycleErr)
}
