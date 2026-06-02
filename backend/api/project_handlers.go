package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/browserwing/browserwing/config"
	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/pkg/logger"
	"github.com/browserwing/browserwing/storage"
	"github.com/gin-gonic/gin"
)

// ProjectHandlers 包含了项目和版本相关的 API 处理器
type ProjectHandlers struct {
	boltDB *storage.BoltDB
	config *config.Config
}

// NewProjectHandlers 创建处理器实例
func NewProjectHandlers(boltDB *storage.BoltDB, cfg *config.Config) *ProjectHandlers {
	return &ProjectHandlers{
		boltDB: boltDB,
		config: cfg,
	}
}

func (h *ProjectHandlers) ListProjects(c *gin.Context) {
	var projects []models.Project
	if err := storage.DB.Preload("Versions").Order("created_at desc").Find(&projects).Error; err != nil {
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

	if err := storage.DB.Create(&project).Error; err != nil {
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
	storage.DB.Create(&defaultVersion)

	c.JSON(http.StatusOK, project)
}

func (h *ProjectHandlers) DeleteProject(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID"})
		return
	}

	// Cascade delete is configured in models
	if err := storage.DB.Delete(&models.Project{}, id).Error; err != nil {
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

	if err := storage.DB.Create(&req).Error; err != nil {
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
	if err := storage.DB.First(&version, versionID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Version not found"})
		return
	}

	if strings.TrimSpace(req.VersionName) != "" {
		version.VersionName = req.VersionName
	}
	version.Description = req.Description
	version.BaseURL = req.BaseURL

	if err := storage.DB.Save(&version).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, version)
}

func (h *ProjectHandlers) DeleteVersion(c *gin.Context) {
	vidStr := c.Param("vid")
	versionID, err := strconv.ParseUint(vidStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Version ID"})
		return
	}

	// Cascade delete is configured in models (it will delete Pages, TestCases, etc.)
	if err := storage.DB.Delete(&models.ProjectVersion{}, versionID).Error; err != nil {
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

	tx := storage.DB.Begin()
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
				PageID:      newPage.ID,
				Name:        s.Name,
				ActionTrace: s.ActionTrace,
				DOMSnapshot: s.DOMSnapshot,
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
	if err := storage.DB.Preload("Scripts").Preload("TestCases").Where("version_id = ?", versionID).Order("created_at desc").Find(&pages).Error; err != nil {
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

	if err := storage.DB.Create(&req).Error; err != nil {
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
	if err := storage.DB.Delete(&models.TestPage{}, pageID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Page deleted successfully"})
}

func (h *ProjectHandlers) SavePageRecording(c *gin.Context) {
	pidStr := c.Param("pid")
	pageID, err := strconv.ParseUint(pidStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Page ID"})
		return
	}

	var req struct {
		Name        string `json:"name"`
		ActionTrace string `json:"action_trace"`
		DOMSnapshot string `json:"dom_snapshot"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	// 删除此页面下已有的其他录制脚本，保证 1-to-1 的主流程（根据设计决策）
	storage.DB.Where("page_id = ?", pageID).Delete(&models.PageScript{})

	newScript := models.PageScript{
		PageID:      uint(pageID),
		Name:        req.Name,
		ActionTrace: req.ActionTrace,
		DOMSnapshot: req.DOMSnapshot,
	}

	if err := storage.DB.Create(&newScript).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "主流程录制保存成功", "script": newScript})
}
