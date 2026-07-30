package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/browserwing/browserwing/models"
	"github.com/gin-gonic/gin"
)

func (h *ProjectHandlers) GetRecordingSession(c *gin.Context) {
	_, session, ok := h.loadRecordingSessionFromRequest(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, recordingSessionSummary(session))
}

func (h *ProjectHandlers) SyncRecordingSession(c *gin.Context) {
	_, session, ok := h.loadRecordingSessionFromRequest(c)
	if !ok {
		return
	}
	var payload struct {
		OperationID  string          `json:"operation_id"`
		SyncRevision uint64          `json:"sync_revision"`
		Actions      json.RawMessage `json:"actions"`
		DOMSnapshot  json.RawMessage `json:"dom_snapshot"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	result, err := h.recordingLifecycleService().Sync(c.Request.Context(), syncRecordingLifecycleInput{OperationID: payload.OperationID, Session: session, SyncRevision: payload.SyncRevision, Actions: payload.Actions, DOMSnapshot: payload.DOMSnapshot})
	h.writeRecordingLifecycleResult(c, result, err)
}

func (h *ProjectHandlers) StopRecordingSession(c *gin.Context) {
	_, session, ok := h.loadRecordingSessionFromRequest(c)
	if !ok {
		return
	}
	var payload struct {
		OperationID string          `json:"operation_id"`
		DOMSnapshot json.RawMessage `json:"dom_snapshot"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	// HTTP only asks Coordinator to drive the scoped runtime. Coordinator then
	// consumes the resulting recording_stopped event and is the sole stopped
	// session writer, shared with page-driven and restart recovery Stop flows.
	result, err := h.recordingRecoveryCoordinator().StopRecordingSession(c.Request.Context(), stopRecordingLifecycleInput{OperationID: payload.OperationID, Session: session, DOMSnapshot: payload.DOMSnapshot})
	h.writeRecordingLifecycleResult(c, result, err)
}

func (h *ProjectHandlers) CancelRecordingSession(c *gin.Context) {
	_, session, ok := h.loadRecordingSessionFromRequest(c)
	if !ok {
		return
	}
	var payload struct {
		OperationID string `json:"operation_id"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	result, err := h.recordingLifecycleService().Cancel(c.Request.Context(), cancelRecordingLifecycleInput{OperationID: payload.OperationID, Session: session})
	h.writeRecordingLifecycleResult(c, result, err)
}

func (h *ProjectHandlers) DownloadRecordingArtifact(c *gin.Context) {
	artifactID, err := strconv.ParseUint(c.Param("artifact_id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid artifact ID"})
		return
	}
	projectID, err := parseUintQuery(c, "project_id")
	if err != nil {
		return
	}
	versionID, err := parseUintQuery(c, "version_id")
	if err != nil {
		return
	}
	pageID, err := parseUintQuery(c, "page_id")
	if err != nil {
		return
	}
	var artifact models.RecordingArtifact
	if err := h.gormDB().
		Where("id = ? AND project_id = ? AND version_id = ? AND page_id = ?", uint(artifactID), projectID, versionID, pageID).
		First(&artifact).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "RecordingArtifact not found"})
		return
	}
	if artifact.StorageBackend != "local" {
		c.JSON(http.StatusNotFound, gin.H{"error": "RecordingArtifact not found"})
		return
	}
	path, ok := h.controlledArtifactPath(c, artifact.StoragePath)
	if !ok {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "RecordingArtifact not found"})
		return
	}
	mimeType := strings.TrimSpace(artifact.MimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	c.Data(http.StatusOK, mimeType, data)
}

func (h *ProjectHandlers) loadRecordingSessionFromRequest(c *gin.Context) (models.ProjectVersion, models.RecordingSession, bool) {
	projectID, versionID, pageID, ok := parseProjectVersionPageIDs(c)
	if !ok {
		return models.ProjectVersion{}, models.RecordingSession{}, false
	}
	version, _, err := loadGenerationPageContext(h.gormDB(), projectID, versionID, pageID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, or recording session not found"})
		return models.ProjectVersion{}, models.RecordingSession{}, false
	}
	session, err := h.loadRecordingSessionByID(c.Param("sid"))
	if err != nil || session.ProjectID != projectID || session.VersionID != versionID || session.PageID != pageID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, page, or recording session not found"})
		return models.ProjectVersion{}, models.RecordingSession{}, false
	}
	return version, session, true
}

func (h *ProjectHandlers) loadRecordingSessionByID(raw string) (models.RecordingSession, error) {
	id, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 32)
	if err != nil {
		return models.RecordingSession{}, err
	}
	var session models.RecordingSession
	err = h.gormDB().First(&session, uint(id)).Error
	return session, err
}

func recordingSessionSummary(session models.RecordingSession) gin.H {
	return gin.H{
		"id":                   session.ID,
		"recording_session_id": fmt.Sprint(session.ID),
		"project_id":           session.ProjectID,
		"version_id":           session.VersionID,
		"page_id":              session.PageID,
		"recording_kind":       session.RecordingKind,
		"auth_context":         session.AuthContext,
		"source_auth_state_id": session.SourceAuthStateID,
		"target_url":           session.TargetURL,
		"status":               session.Status,
		"lifecycle_revision":   session.LifecycleRevision,
		"sync_revision":        session.SyncRevision,
		"browser_instance_id":  session.BrowserInstanceID,
		"runtime_page_id":      session.RuntimePageID,
		"action_count":         session.ActionCount,
		"started_at":           session.StartedAt,
		"last_synced_at":       session.LastSyncedAt,
		"stopped_at":           session.StoppedAt,
		"saved_at":             session.SavedAt,
		"error_message":        session.ErrorMessage,
		"created_at":           session.CreatedAt,
		"updated_at":           session.UpdatedAt,
	}
}

func recordingSessionRuntimeScope(session models.RecordingSession) map[string]any {
	return map[string]any{
		"project_id":           session.ProjectID,
		"version_id":           session.VersionID,
		"page_id":              session.PageID,
		"recording_session_id": fmt.Sprint(session.ID),
		"browser_instance_id":  session.BrowserInstanceID,
		"runtime_page_id":      session.RuntimePageID,
		"runtime_generation":   session.RuntimeGeneration,
		"lease_generation":     session.LeaseGeneration,
	}
}

func isTerminalRecordingStatus(status string) bool {
	return status == "saved" || status == "cancelled" || status == "failed"
}

func parseUintQuery(c *gin.Context, name string) (uint, error) {
	raw := strings.TrimSpace(c.Query(name))
	value, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid " + name})
		return 0, err
	}
	return uint(value), nil
}

func (h *ProjectHandlers) controlledArtifactPath(c *gin.Context, storagePath string) (string, bool) {
	if filepath.IsAbs(storagePath) {
		c.JSON(http.StatusNotFound, gin.H{"error": "RecordingArtifact not found"})
		return "", false
	}
	root := "."
	if h.config != nil && strings.TrimSpace(h.config.AssetsDir) != "" {
		root = h.config.AssetsDir
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Artifact storage is unavailable"})
		return "", false
	}
	cleanRel := filepath.Clean(strings.TrimLeft(storagePath, `/\`))
	if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(os.PathSeparator)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "RecordingArtifact not found"})
		return "", false
	}
	abs := filepath.Join(rootAbs, cleanRel)
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "RecordingArtifact not found"})
		return "", false
	}
	return abs, true
}
