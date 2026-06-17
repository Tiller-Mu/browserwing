package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/browserwing/browserwing/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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
	if isTerminalRecordingStatus(session.Status) {
		c.JSON(http.StatusConflict, gin.H{"error": "RecordingSession is already terminated"})
		return
	}

	var payload struct {
		Actions     json.RawMessage `json:"actions"`
		DOMSnapshot json.RawMessage `json:"dom_snapshot"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if len(payload.Actions) > 0 && strings.TrimSpace(string(payload.Actions)) != "null" {
		actionCount, err := countJSONActions(payload.Actions)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "actions must be a JSON array"})
			return
		}
		updates["actions_json"] = string(payload.Actions)
		updates["action_count"] = actionCount
		updates["last_synced_at"] = time.Now().UTC()
	}
	if len(payload.DOMSnapshot) > 0 && strings.TrimSpace(string(payload.DOMSnapshot)) != "null" {
		updates["dom_snapshot"] = string(payload.DOMSnapshot)
	}
	if err := h.gormDB().Model(&models.RecordingSession{}).
		Where("id = ? AND project_id = ? AND version_id = ? AND page_id = ?", session.ID, session.ProjectID, session.VersionID, session.PageID).
		Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := h.gormDB().First(&session, session.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, recordingSessionSummary(session))
}

func (h *ProjectHandlers) StopRecordingSession(c *gin.Context) {
	_, session, ok := h.loadRecordingSessionFromRequest(c)
	if !ok {
		return
	}
	if isTerminalRecordingStatus(session.Status) {
		c.JSON(http.StatusConflict, gin.H{"error": "RecordingSession is already terminated"})
		return
	}

	var payload struct {
		DOMSnapshot json.RawMessage `json:"dom_snapshot"`
	}
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
			return
		}
	}

	runtime := h.projectAuthRuntime()
	var runtimeResult map[string]any
	if runtime != nil {
		result, err := runtime.StopPageRecording(c.Request.Context(), recordingSessionRuntimeScope(session))
		if err != nil {
			if stopPersistedRecordingDraft(c, h.gormDB(), session, err) {
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Stop page recording failed"})
			return
		}
		runtimeResult = result
	}

	updates := map[string]any{
		"status":     "stopped",
		"stopped_at": time.Now().UTC(),
		"updated_at": time.Now().UTC(),
	}
	if actions, ok := runtimeResult["actions"]; ok {
		if actionsJSON, actionCount, err := marshalActions(actions); err == nil {
			if actionCount > 0 {
				updates["actions_json"] = actionsJSON
				updates["action_count"] = actionCount
				updates["last_synced_at"] = time.Now().UTC()
			}
		}
	}
	if len(payload.DOMSnapshot) > 0 && strings.TrimSpace(string(payload.DOMSnapshot)) != "null" {
		updates["dom_snapshot"] = string(payload.DOMSnapshot)
	} else if snapshot, ok := runtimeResult["dom_snapshot"]; ok {
		if snapshotJSON, err := json.Marshal(snapshot); err == nil {
			updates["dom_snapshot"] = string(snapshotJSON)
		}
	}

	if err := h.gormDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.RecordingSession{}).
			Where("id = ? AND project_id = ? AND version_id = ? AND page_id = ?", session.ID, session.ProjectID, session.VersionID, session.PageID).
			Updates(updates).Error; err != nil {
			return err
		}
		for _, artifact := range buildRecordingArtifacts(session, runtimeResult["artifacts"]) {
			if err := tx.Create(&artifact).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := h.gormDB().First(&session, session.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, recordingSessionSummary(session))
}

func stopPersistedRecordingDraft(c *gin.Context, db *gorm.DB, session models.RecordingSession, stopErr error) bool {
	if !isRecoverableStoppedBrowserRecordingError(stopErr) {
		return false
	}
	if strings.TrimSpace(session.ActionsJSON) == "" || session.ActionCount <= 0 {
		return false
	}
	if _, err := countJSONActions(json.RawMessage(session.ActionsJSON)); err != nil {
		return false
	}
	now := time.Now().UTC()
	if err := db.Model(&models.RecordingSession{}).
		Where("id = ? AND status = ?", session.ID, "recording").
		Updates(map[string]any{
			"status":     "stopped",
			"stopped_at": now,
			"updated_at": now,
		}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return true
	}
	if err := db.First(&session, session.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return true
	}
	c.JSON(http.StatusOK, recordingSessionSummary(session))
	return true
}

func isRecoverableStoppedBrowserRecordingError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "recording is not in progress")
}

func (h *ProjectHandlers) CancelRecordingSession(c *gin.Context) {
	_, session, ok := h.loadRecordingSessionFromRequest(c)
	if !ok {
		return
	}
	if isTerminalRecordingStatus(session.Status) {
		c.JSON(http.StatusConflict, gin.H{"error": "RecordingSession is already terminated"})
		return
	}
	if session.Status != "recording" && session.Status != "stopped" {
		c.JSON(http.StatusConflict, gin.H{"error": "RecordingSession cannot be cancelled"})
		return
	}

	now := time.Now().UTC()
	updates := map[string]any{
		"status":     "cancelled",
		"updated_at": now,
	}
	if session.Status == "recording" {
		runtime := h.projectAuthRuntime()
		if runtime == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Project auth runtime is not configured"})
			return
		}
		if _, err := runtime.StopPageRecording(c.Request.Context(), recordingSessionRuntimeScope(session)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Cancel page recording failed"})
			return
		}
		updates["stopped_at"] = now
	}

	result := h.gormDB().Model(&models.RecordingSession{}).
		Where("id = ? AND project_id = ? AND version_id = ? AND page_id = ? AND status IN ?", session.ID, session.ProjectID, session.VersionID, session.PageID, []string{"recording", "stopped"}).
		Updates(updates)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "RecordingSession is already terminated"})
		return
	}
	if err := h.gormDB().First(&session, session.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, recordingSessionSummary(session))
}

func (h *ProjectHandlers) saveRecordingSessionAsPageScript(c *gin.Context, projectID, versionID, pageID uint, sessionID string, recordingMeta json.RawMessage, name string) bool {
	session, err := h.loadRecordingSessionByID(sessionID)
	if err != nil || session.ProjectID != projectID || session.VersionID != versionID || session.PageID != pageID {
		c.JSON(http.StatusNotFound, gin.H{"error": "RecordingSession not found"})
		return false
	}
	if session.Status != "stopped" {
		c.JSON(http.StatusConflict, gin.H{"error": "RecordingSession cannot be saved"})
		return false
	}
	meta, normalizedMeta, ok := normalizeRecordingMetaForSave(c, recordingMeta)
	if !ok {
		return false
	}
	if strings.TrimSpace(name) == "" {
		name = "Recorded main flow"
	}
	if meta.TargetURL == "" {
		meta.TargetURL = session.TargetURL
	}
	normalizedMetaData, err := json.Marshal(meta)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "marshal recording meta"})
		return false
	}
	normalizedMeta = string(normalizedMetaData)
	newScript := models.PageScript{
		PageID:            pageID,
		Name:              name,
		ActionTrace:       session.ActionsJSON,
		DOMSnapshot:       session.DOMSnapshot,
		RecordingMetaJSON: normalizedMeta,
	}
	now := time.Now().UTC()
	if err := h.gormDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("page_id = ?", pageID).Delete(&models.PageScript{}).Error; err != nil {
			return err
		}
		if err := tx.Create(&newScript).Error; err != nil {
			return err
		}
		return tx.Model(&models.RecordingSession{}).
			Where("id = ? AND page_id = ?", session.ID, pageID).
			Updates(map[string]any{
				"status":              "saved",
				"recording_meta_json": normalizedMeta,
				"stopped_at":          session.StoppedAt,
				"saved_at":            now,
				"updated_at":          now,
			}).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return false
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "主流程录制保存成功",
		"script": gin.H{
			"id":                  newScript.ID,
			"page_id":             newScript.PageID,
			"name":                newScript.Name,
			"recording_meta_json": newScript.RecordingMetaJSON,
		},
		"recording_session": gin.H{
			"id":     session.ID,
			"status": "saved",
		},
	})
	return true
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
		"target_url":           session.TargetURL,
		"status":               session.Status,
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
	}
}

func isTerminalRecordingStatus(status string) bool {
	return status == "saved" || status == "cancelled" || status == "failed"
}

func countJSONActions(raw json.RawMessage) (int, error) {
	var actions []any
	if err := json.Unmarshal(raw, &actions); err != nil {
		return 0, err
	}
	return len(actions), nil
}

func marshalActions(value any) (string, int, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", 0, err
	}
	count, err := countJSONActions(data)
	if err != nil {
		return "", 0, err
	}
	return string(data), count, nil
}

func normalizeRecordingMetaForSave(c *gin.Context, raw json.RawMessage) (p45RecordingMeta, string, bool) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recording_meta is required"})
		return p45RecordingMeta{}, "", false
	}
	var meta p45RecordingMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recording_meta JSON is invalid"})
		return p45RecordingMeta{}, "", false
	}
	if err := validateRecordingMeta(meta, false); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return p45RecordingMeta{}, "", false
	}
	data, err := json.Marshal(meta)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recording_meta JSON is invalid"})
		return p45RecordingMeta{}, "", false
	}
	return meta, string(data), true
}

func buildRecordingArtifacts(session models.RecordingSession, raw any) []models.RecordingArtifact {
	items := anySlice(raw)
	artifacts := make([]models.RecordingArtifact, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		storagePath := strings.TrimSpace(stringFromAny(obj["storage_path"]))
		if storagePath == "" {
			continue
		}
		backend := strings.TrimSpace(stringFromAny(obj["storage_backend"]))
		if backend == "" {
			backend = "local"
		}
		size := int64(0)
		switch v := obj["size_bytes"].(type) {
		case int:
			size = int64(v)
		case int64:
			size = v
		case float64:
			size = int64(v)
		}
		artifacts = append(artifacts, models.RecordingArtifact{
			ProjectID:          session.ProjectID,
			VersionID:          session.VersionID,
			PageID:             session.PageID,
			RecordingSessionID: session.ID,
			ArtifactType:       firstNonEmptyString(stringFromAny(obj["artifact_type"]), "other"),
			StorageBackend:     backend,
			StoragePath:        storagePath,
			FileName:           stringFromAny(obj["file_name"]),
			MimeType:           stringFromAny(obj["mime_type"]),
			SizeBytes:          size,
			Sensitive:          boolFromAny(obj["sensitive"]),
			CreatedAt:          time.Now().UTC(),
		})
	}
	return artifacts
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
