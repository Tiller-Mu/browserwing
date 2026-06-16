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
	playbotAgent   *playbotAgentHolder
}

// NewProjectHandlers 创建处理器实例
func NewProjectHandlers(store storage.Store, cfg *config.Config, runnerHolder *testCaseRunnerHolder, authHolder *projectAuthRuntimeHolder, agentHolder *playbotAgentHolder) *ProjectHandlers {
	var holder *testCaseRunnerHolder
	holder = runnerHolder
	return &ProjectHandlers{
		store:          store,
		config:         cfg,
		testCaseRunner: holder,
		projectAuth:    authHolder,
		playbotAgent:   agentHolder,
	}
}

func (h *ProjectHandlers) gormDB() *gorm.DB {
	return h.store.GormDB()
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

	// 1. 创建新版本
	newVersion := models.ProjectVersion{
		ProjectID:   sourceVersion.ProjectID,
		VersionName: req.NewVersionName,
		Description: "Cloned from " + sourceVersion.VersionName,
	}
	if err := tx.Create(&newVersion).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 2. 深度克隆 Pages
	var pages []models.TestPage
	tx.Where("version_id = ?", sourceVersion.ID).Find(&pages)

	for _, p := range pages {
		newPage := models.TestPage{
			VersionID:   newVersion.ID,
			Name:        p.Name,
			Path:        p.Path,
			Description: p.Description,
		}
		tx.Create(&newPage)

		// 3. 深度克隆 PageScripts
		var scripts []models.PageScript
		tx.Where("page_id = ?", p.ID).Find(&scripts)
		for _, s := range scripts {
			newScript := models.PageScript{
				PageID:            newPage.ID,
				Name:              s.Name,
				ActionTrace:       s.ActionTrace,
				DOMSnapshot:       s.DOMSnapshot,
				RecordingMetaJSON: s.RecordingMetaJSON,
			}
			tx.Create(&newScript)
		}

		// 4. 深度克隆 TestCases
		var cases []models.TestCase
		tx.Where("page_id = ?", p.ID).Find(&cases)
		for _, c1 := range cases {
			newCase := models.TestCase{
				PageID:        newPage.ID,
				Title:         c1.Title,
				Description:   c1.Description,
				Blueprint:     c1.Blueprint,
				ScriptContent: c1.ScriptContent,
				Status:        c1.Status,
			}
			tx.Create(&newCase)
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
		Name               string          `json:"name"`
		ActionTrace        string          `json:"action_trace"`
		DOMSnapshot        string          `json:"dom_snapshot"`
		RecordingMeta      json.RawMessage `json:"recording_meta"`
		RecordingSessionID string          `json:"recording_session_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	if strings.TrimSpace(req.RecordingSessionID) != "" {
		h.saveRecordingSessionAsPageScript(c, projectID, versionID, pageID, req.RecordingSessionID, req.RecordingMeta, req.Name)
		return
	}

	recordingMetaJSON := ""
	if len(req.RecordingMeta) > 0 && strings.TrimSpace(string(req.RecordingMeta)) != "null" {
		var meta p45RecordingMeta
		if err := json.Unmarshal(req.RecordingMeta, &meta); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "recording_meta JSON is invalid"})
			return
		}
		if err := validateRecordingMeta(meta, false); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		normalizedMeta, err := json.Marshal(meta)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "recording_meta JSON is invalid"})
			return
		}
		recordingMetaJSON = string(normalizedMeta)
	}

	newScript := models.PageScript{
		PageID:            pageID,
		Name:              req.Name,
		ActionTrace:       req.ActionTrace,
		DOMSnapshot:       req.DOMSnapshot,
		RecordingMetaJSON: recordingMetaJSON,
	}

	if err := h.gormDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("page_id = ?", pageID).Delete(&models.PageScript{}).Error; err != nil {
			return err
		}
		return tx.Create(&newScript).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "主流程录制保存成功", "script": newScript})
}
