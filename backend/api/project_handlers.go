package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/pkg/logger"
	"github.com/browserwing/browserwing/storage"
	"github.com/gin-gonic/gin"
)

// ProjectHandlers 包含了项目和版本相关的 API 处理器
type ProjectHandlers struct{}

// NewProjectHandlers 创建处理器实例
func NewProjectHandlers() *ProjectHandlers {
	return &ProjectHandlers{}
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
	var req models.Project
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Project name is required"})
		return
	}

	if err := storage.DB.Create(&req).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 自动创建一个默认版本 v1.0.0
	defaultVersion := models.ProjectVersion{
		ProjectID:   req.ID,
		VersionName: "v1.0.0",
		Description: "Default initial version",
	}
	storage.DB.Create(&defaultVersion)

	c.JSON(http.StatusOK, req)
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
