package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/pkg/logger"
	"github.com/browserwing/browserwing/services/browser"
	"github.com/gin-gonic/gin"
	"github.com/go-rod/rod/lib/proto"
	"gorm.io/gorm"
)

const (
	authContextClean        = "clean"
	authContextProjectSaved = "project_saved"
	recordingKindLoginFlow  = "login_flow"
	recordingKindBusiness   = "business_flow"
)

type projectAuthRuntime interface {
	CaptureProjectAuthState(context.Context, map[string]any) (map[string]any, error)
	SaveProjectAuthState(context.Context, map[string]any) error
	StartPageRecording(context.Context, map[string]any) (map[string]any, error)
	PrepareTestExecution(context.Context, map[string]any) error
	RestoreProjectAuthState(context.Context, map[string]any) error
}

type projectAuthRuntimeHolder struct {
	mu      sync.RWMutex
	runtime projectAuthRuntime
}

func newProjectAuthRuntimeHolder(runtime projectAuthRuntime) *projectAuthRuntimeHolder {
	return &projectAuthRuntimeHolder{runtime: runtime}
}

func (h *projectAuthRuntimeHolder) set(runtime projectAuthRuntime) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runtime = runtime
}

func (h *projectAuthRuntimeHolder) get() projectAuthRuntime {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.runtime
}

func (h *ProjectHandlers) projectAuthRuntime() projectAuthRuntime {
	if h.projectAuth == nil {
		return nil
	}
	return h.projectAuth.get()
}

type injectedProjectAuthRuntime struct {
	target any
}

func adaptInjectedProjectAuthRuntime(target any) projectAuthRuntime {
	if runtime, ok := target.(projectAuthRuntime); ok {
		return runtime
	}
	return &injectedProjectAuthRuntime{target: target}
}

func (r *injectedProjectAuthRuntime) CaptureProjectAuthState(ctx context.Context, input map[string]any) (map[string]any, error) {
	return invokeProjectAuthMapMethod(ctx, r.target, "CaptureProjectAuthState", input)
}

func (r *injectedProjectAuthRuntime) SaveProjectAuthState(ctx context.Context, input map[string]any) error {
	_, err := invokeProjectAuthMapMethod(ctx, r.target, "SaveProjectAuthState", input)
	return err
}

func (r *injectedProjectAuthRuntime) StartPageRecording(ctx context.Context, input map[string]any) (map[string]any, error) {
	return invokeProjectAuthMapMethod(ctx, r.target, "StartPageRecording", input)
}

func (r *injectedProjectAuthRuntime) PrepareTestExecution(ctx context.Context, input map[string]any) error {
	_, err := invokeProjectAuthMapMethod(ctx, r.target, "PrepareTestExecution", input)
	return err
}

func (r *injectedProjectAuthRuntime) RestoreProjectAuthState(ctx context.Context, input map[string]any) error {
	_, err := invokeProjectAuthMapMethod(ctx, r.target, "RestoreProjectAuthState", input)
	return err
}

func invokeProjectAuthMapMethod(ctx context.Context, target any, name string, input map[string]any) (map[string]any, error) {
	method := reflect.ValueOf(target).MethodByName(name)
	if !method.IsValid() {
		return nil, fmt.Errorf("Project auth runtime missing %s", name)
	}
	output := method.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(input)})
	if len(output) == 1 {
		if !output[0].IsNil() {
			err, _ := output[0].Interface().(error)
			return nil, err
		}
		return nil, nil
	}
	if len(output) != 2 {
		return nil, fmt.Errorf("Project auth runtime %s has invalid signature", name)
	}
	if !output[1].IsNil() {
		err, _ := output[1].Interface().(error)
		return nil, err
	}
	data, err := json.Marshal(output[0].Interface())
	if err != nil {
		return nil, fmt.Errorf("marshal Project auth runtime %s result: %w", name, err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode Project auth runtime %s result: %w", name, err)
	}
	return result, nil
}

type unavailableProjectAuthRuntime struct{}

func (unavailableProjectAuthRuntime) CaptureProjectAuthState(context.Context, map[string]any) (map[string]any, error) {
	return nil, fmt.Errorf("Project auth runtime is not configured")
}

func (unavailableProjectAuthRuntime) SaveProjectAuthState(context.Context, map[string]any) error {
	return fmt.Errorf("Project auth runtime is not configured")
}

func (unavailableProjectAuthRuntime) StartPageRecording(context.Context, map[string]any) (map[string]any, error) {
	return nil, fmt.Errorf("Project auth runtime is not configured")
}

func (unavailableProjectAuthRuntime) PrepareTestExecution(context.Context, map[string]any) error {
	return nil
}

func (unavailableProjectAuthRuntime) RestoreProjectAuthState(context.Context, map[string]any) error {
	return fmt.Errorf("Project auth runtime is not configured")
}

type captureProjectAuthStateRequest struct {
	Name              string   `json:"name"`
	CapturedPageID    uint     `json:"captured_page_id"`
	CapturedURL       string   `json:"captured_url"`
	OriginAllowlist   []string `json:"origin_allowlist"`
	Replace           *bool    `json:"replace"`
	BrowserInstanceID string   `json:"browser_instance_id"`
}

type startPageRecordingSessionRequest struct {
	RecordingKind     string `json:"recording_kind"`
	AuthContext       string `json:"auth_context"`
	BrowserInstanceID string `json:"browser_instance_id"`
}

type p45RecordingMeta struct {
	SchemaVersion int    `json:"schema_version"`
	RecordingKind string `json:"recording_kind"`
	AuthContext   string `json:"auth_context"`
	AuthStateID   *uint  `json:"auth_state_id"`
	TargetURL     string `json:"target_url"`
	StartedAt     string `json:"started_at,omitempty"`
	EndedAt       string `json:"ended_at,omitempty"`
}

func (h *ProjectHandlers) GetProjectAuthState(c *gin.Context) {
	projectID, versionID, ok := parseProjectVersionIDs(c)
	if !ok {
		return
	}
	if _, err := h.loadProjectVersion(projectID, versionID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project or version not found"})
		return
	}
	auth, err := h.loadActiveProjectAuthState(projectID, versionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if auth == nil {
		c.JSON(http.StatusOK, gin.H{"auth_state": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"auth_state": projectAuthStateSummary(*auth)})
}

func (h *ProjectHandlers) CaptureProjectAuthState(c *gin.Context) {
	projectID, versionID, ok := parseProjectVersionIDs(c)
	if !ok {
		return
	}
	version, err := h.loadProjectVersion(projectID, versionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project or version not found"})
		return
	}

	var req captureProjectAuthStateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	if req.CapturedPageID != 0 {
		if _, err := h.loadPageInVersion(versionID, req.CapturedPageID); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, or page not found"})
			return
		}
	}
	if req.Replace != nil && !*req.Replace {
		c.JSON(http.StatusBadRequest, gin.H{"error": "replace=false is not supported for Project auth state"})
		return
	}

	runtime := h.projectAuthRuntime()
	if runtime == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Project auth runtime is not configured"})
		return
	}

	state, err := runtime.CaptureProjectAuthState(c.Request.Context(), map[string]any{
		"project_id":          projectID,
		"version_id":          versionID,
		"base_url":            version.BaseURL,
		"captured_page_id":    req.CapturedPageID,
		"captured_url":        req.CapturedURL,
		"origin_allowlist":    req.OriginAllowlist,
		"browser_instance_id": req.BrowserInstanceID,
	})
	if err != nil {
		logger.Warn(c.Request.Context(), "Capture project auth state failed: project_id=%d version_id=%d category=%s", projectID, versionID, projectAuthCaptureFailureCategory(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Capture project auth state failed"})
		return
	}
	state = filterProjectAuthStorageState(state, version.BaseURL, req.OriginAllowlist, req.CapturedURL)

	row, err := buildProjectAuthStateRow(projectID, versionID, req, state)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := runtime.SaveProjectAuthState(c.Request.Context(), map[string]any{
		"project_id":   projectID,
		"version_id":   versionID,
		"auth_state":   projectAuthStateSummary(row),
		"state_digest": row.StateDigest,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Save project auth state failed"})
		return
	}

	if err := h.gormDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("project_id = ? AND version_id = ? AND status = ?", projectID, versionID, "active").
			Delete(&models.ProjectAuthState{}).Error; err != nil {
			return err
		}
		return tx.Create(&row).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"auth_state": projectAuthStateSummary(row)})
}

func (h *ProjectHandlers) DeleteProjectAuthState(c *gin.Context) {
	projectID, versionID, ok := parseProjectVersionIDs(c)
	if !ok {
		return
	}
	if _, err := h.loadProjectVersion(projectID, versionID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project or version not found"})
		return
	}
	if err := h.gormDB().Where("project_id = ? AND version_id = ? AND status = ?", projectID, versionID, "active").
		Delete(&models.ProjectAuthState{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Project auth state deleted"})
}

func (h *ProjectHandlers) GetPageRecordingContext(c *gin.Context) {
	projectID, versionID, pageID, ok := parseProjectVersionPageIDs(c)
	if !ok {
		return
	}
	version, page, err := loadGenerationPageContext(h.gormDB(), projectID, versionID, pageID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, or page not found"})
		return
	}
	auth, err := h.loadActiveProjectAuthState(projectID, versionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var authSummary any
	if auth != nil {
		authSummary = projectAuthStateSummary(*auth)
	}
	c.JSON(http.StatusOK, gin.H{
		"page":       page,
		"target_url": buildPageURL(version.BaseURL, page.Path),
		"auth_state": authSummary,
	})
}

func (h *ProjectHandlers) StartPageRecordingSession(c *gin.Context) {
	projectID, versionID, pageID, ok := parseProjectVersionPageIDs(c)
	if !ok {
		return
	}
	version, page, err := loadGenerationPageContext(h.gormDB(), projectID, versionID, pageID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, or page not found"})
		return
	}

	var req startPageRecordingSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	meta := p45RecordingMeta{
		SchemaVersion: 1,
		RecordingKind: strings.TrimSpace(req.RecordingKind),
		AuthContext:   strings.TrimSpace(req.AuthContext),
		TargetURL:     buildPageURL(version.BaseURL, page.Path),
	}
	if err := validateRecordingMeta(meta, false); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var auth *models.ProjectAuthState
	if meta.AuthContext == authContextProjectSaved {
		auth, err = h.loadActiveProjectAuthState(projectID, versionID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if auth == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Project auth state is required"})
			return
		}
	}

	runtime := h.projectAuthRuntime()
	if runtime == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Project auth runtime is not configured"})
		return
	}
	input := map[string]any{
		"project_id":              projectID,
		"version_id":              versionID,
		"page_id":                 pageID,
		"recording_kind":          meta.RecordingKind,
		"auth_context":            meta.AuthContext,
		"target_url":              meta.TargetURL,
		"browser_instance_id":     req.BrowserInstanceID,
		"use_global_cookie_store": false,
	}
	if auth != nil {
		input["auth_state"] = projectAuthStateSummary(*auth)
		input["auth_state_json"] = auth.StateJSON
	}
	result, err := runtime.StartPageRecording(c.Request.Context(), input)
	if err != nil {
		safeDetail := safeProjectRecordingStartError(err)
		logger.Error(
			c.Request.Context(),
			"Failed to start project page recording: project_id=%d version_id=%d page_id=%d recording_kind=%s auth_context=%s detail=%s",
			projectID,
			versionID,
			pageID,
			meta.RecordingKind,
			meta.AuthContext,
			safeDetail,
		)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  "Start page recording failed",
			"detail": safeDetail,
		})
		return
	}
	if result == nil {
		result = map[string]any{}
	}
	result["recording_meta"] = map[string]any{
		"schema_version": meta.SchemaVersion,
		"recording_kind": meta.RecordingKind,
		"auth_context":   meta.AuthContext,
		"auth_state_id":  nil,
		"target_url":     meta.TargetURL,
	}
	if auth != nil {
		result["auth_state"] = projectAuthStateSummary(*auth)
		result["recording_meta"].(map[string]any)["auth_state_id"] = auth.ID
	}
	c.JSON(http.StatusOK, result)
}

func safeProjectRecordingStartError(err error) string {
	if err == nil {
		return "Project page recording could not be started"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "project auth state") || strings.Contains(msg, "storage"):
		return "Saved project auth state could not be restored"
	case strings.Contains(msg, "browser") || strings.Contains(msg, "chrome"):
		return "Browser is not ready for project recording"
	case strings.Contains(msg, "navigate") || strings.Contains(msg, "load"):
		return "Target page could not be opened for recording"
	case strings.Contains(msg, "recorder") || strings.Contains(msg, "recording"):
		return "Recorder could not be initialized"
	default:
		return "Project page recording could not be started"
	}
}

func parseProjectVersionIDs(c *gin.Context) (uint, uint, bool) {
	projectID, err := parseUintParam(c, "id", "Invalid Project ID")
	if err != nil {
		return 0, 0, false
	}
	versionID, err := parseUintParam(c, "vid", "Invalid Version ID")
	if err != nil {
		return 0, 0, false
	}
	return projectID, versionID, true
}

func (h *ProjectHandlers) loadProjectVersion(projectID, versionID uint) (models.ProjectVersion, error) {
	var project models.Project
	if err := h.gormDB().First(&project, projectID).Error; err != nil {
		return models.ProjectVersion{}, err
	}
	var version models.ProjectVersion
	if err := h.gormDB().Where("id = ? AND project_id = ?", versionID, projectID).First(&version).Error; err != nil {
		return models.ProjectVersion{}, err
	}
	return version, nil
}

func (h *ProjectHandlers) loadPageInVersion(versionID, pageID uint) (models.TestPage, error) {
	var page models.TestPage
	err := h.gormDB().Where("id = ? AND version_id = ?", pageID, versionID).First(&page).Error
	return page, err
}

func (h *ProjectHandlers) loadActiveProjectAuthState(projectID, versionID uint) (*models.ProjectAuthState, error) {
	var auth models.ProjectAuthState
	err := h.gormDB().Where("project_id = ? AND version_id = ? AND status = ?", projectID, versionID, "active").
		Order("captured_at desc, id desc").First(&auth).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &auth, nil
}

func projectAuthCaptureFailureCategory(err error) string {
	if err == nil {
		return "none"
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "open a page") || strings.Contains(text, "page") && strings.Contains(text, "not available") || strings.Contains(text, "not capturable"):
		return "page_unavailable"
	case strings.Contains(text, "web storage") || strings.Contains(text, "storage"):
		return "storage_unavailable"
	default:
		return "runtime_failed"
	}
}

func buildProjectAuthStateRow(projectID, versionID uint, req captureProjectAuthStateRequest, state map[string]any) (models.ProjectAuthState, error) {
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return models.ProjectAuthState{}, fmt.Errorf("Project auth state is not JSON serializable")
	}
	cookieCount, originCount, origins := summarizeStorageState(state)
	if cookieCount == 0 && originCount == 0 {
		return models.ProjectAuthState{}, fmt.Errorf("Project auth state is empty")
	}
	allowlist := req.OriginAllowlist
	if len(allowlist) == 0 {
		allowlist = origins
	}
	allowlistJSON, _ := json.Marshal(allowlist)
	digest := sha256.Sum256(stateJSON)
	now := time.Now().UTC()
	capturedURL := strings.TrimSpace(req.CapturedURL)
	if capturedURL == "" {
		capturedURL = stringFromAny(state["captured_url"])
	}
	capturedAt := now
	if raw := strings.TrimSpace(stringFromAny(state["captured_at"])); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			capturedAt = parsed.UTC()
		}
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Default project auth state"
	}
	return models.ProjectAuthState{
		ProjectID:           projectID,
		VersionID:           versionID,
		Name:                name,
		Status:              "active",
		SchemaVersion:       1,
		StateJSON:           string(stateJSON),
		StateDigest:         hex.EncodeToString(digest[:]),
		OriginAllowlistJSON: string(allowlistJSON),
		CookieCount:         cookieCount,
		OriginCount:         originCount,
		CapturedURL:         capturedURL,
		CapturedPageID:      req.CapturedPageID,
		CapturedAt:          capturedAt,
		CreatedAt:           now,
		UpdatedAt:           now,
	}, nil
}

func summarizeStorageState(state map[string]any) (int, int, []string) {
	cookieCount := len(anySlice(state["cookies"]))
	originsRaw := anySlice(state["origins"])
	origins := make([]string, 0, len(originsRaw))
	for _, item := range originsRaw {
		originObj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hasStorage := len(anySlice(originObj["local_storage"])) > 0 || len(anySlice(originObj["session_storage"])) > 0
		origin := strings.TrimSpace(stringFromAny(originObj["origin"]))
		if hasStorage && origin != "" {
			origins = append(origins, origin)
		}
	}
	return cookieCount, len(origins), origins
}

func filterProjectAuthStorageState(state map[string]any, baseURL string, originAllowlist []string, capturedURL string) map[string]any {
	currentOrigin := pageOrigin(firstNonEmptyString(capturedURL, stringFromAny(state["captured_url"]), baseURL))
	allowed := allowedProjectOrigins(baseURL, currentOrigin, originAllowlist)
	filtered := make(map[string]any, len(state))
	for key, value := range state {
		filtered[key] = value
	}

	filtered["cookies"] = filterStorageCookies(normalizeCookieObjects(state["cookies"]), allowed)
	filteredOrigins := make([]map[string]any, 0)
	for _, raw := range anySlice(state["origins"]) {
		originObj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if originAllowed(strings.TrimSpace(stringFromAny(originObj["origin"])), allowed) {
			filteredOrigins = append(filteredOrigins, originObj)
		}
	}
	filtered["origins"] = filteredOrigins
	return filtered
}

func projectAuthStateSummary(auth models.ProjectAuthState) map[string]any {
	origins := []string{}
	if strings.TrimSpace(auth.OriginAllowlistJSON) != "" {
		_ = json.Unmarshal([]byte(auth.OriginAllowlistJSON), &origins)
	}
	return map[string]any{
		"id":                auth.ID,
		"project_id":        auth.ProjectID,
		"version_id":        auth.VersionID,
		"name":              auth.Name,
		"status":            auth.Status,
		"schema_version":    auth.SchemaVersion,
		"state_digest":      auth.StateDigest,
		"origins":           origins,
		"cookie_count":      auth.CookieCount,
		"origin_count":      auth.OriginCount,
		"captured_url":      auth.CapturedURL,
		"captured_page_id":  auth.CapturedPageID,
		"captured_at":       auth.CapturedAt,
		"last_validated_at": auth.LastValidatedAt,
		"invalid_reason":    auth.InvalidReason,
		"created_at":        auth.CreatedAt,
		"updated_at":        auth.UpdatedAt,
	}
}

func validateRecordingMeta(meta p45RecordingMeta, allowEmpty bool) error {
	if meta.SchemaVersion == 0 && meta.RecordingKind == "" && meta.AuthContext == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("recording_meta is required")
	}
	if meta.SchemaVersion != 1 {
		return fmt.Errorf("recording_meta schema_version is invalid")
	}
	if meta.RecordingKind != recordingKindLoginFlow && meta.RecordingKind != recordingKindBusiness {
		return fmt.Errorf("recording_kind is invalid")
	}
	if !validAuthContext(meta.AuthContext) {
		return fmt.Errorf("auth_context is invalid")
	}
	if meta.RecordingKind == recordingKindLoginFlow && meta.AuthContext != authContextClean {
		return fmt.Errorf("login_flow must use clean auth_context")
	}
	return nil
}

func parseRecordingMetaJSON(raw string) (p45RecordingMeta, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return p45RecordingMeta{AuthContext: authContextClean}, false, nil
	}
	var meta p45RecordingMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return p45RecordingMeta{}, true, fmt.Errorf("recording_meta JSON is invalid")
	}
	if err := validateRecordingMeta(meta, false); err != nil {
		return p45RecordingMeta{}, true, err
	}
	return meta, true, nil
}

func validAuthContext(value string) bool {
	return value == authContextClean || value == authContextProjectSaved
}

func authContextFromBlueprint(blueprint map[string]any) (string, string, error) {
	raw, exists := blueprint["auth_context"]
	if !exists || strings.TrimSpace(stringFromAny(raw)) == "" {
		return authContextClean, "legacy_default", nil
	}
	contextValue := strings.TrimSpace(stringFromAny(raw))
	if !validAuthContext(contextValue) {
		return "", "", testCaseValidationError("Blueprint auth_context is invalid")
	}
	return contextValue, "blueprint", nil
}

func anySlice(value any) []any {
	switch v := value.(type) {
	case []any:
		return v
	case []map[string]any:
		items := make([]any, 0, len(v))
		for _, item := range v {
			items = append(items, item)
		}
		return items
	default:
		return nil
	}
}

func stringSliceFromAny(value any) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		items := make([]string, 0, len(v))
		for _, item := range v {
			if text := strings.TrimSpace(stringFromAny(item)); text != "" {
				items = append(items, text)
			}
		}
		return items
	default:
		return nil
	}
}

func firstExistingValue(obj map[string]any, names ...string) any {
	for _, name := range names {
		if value, ok := obj[name]; ok {
			return value
		}
	}
	return nil
}

func allowedProjectOrigins(baseURL string, currentOrigin string, originAllowlist []string) map[string]struct{} {
	allowed := map[string]struct{}{}
	if origin := pageOrigin(baseURL); strings.HasPrefix(origin, "http://") || strings.HasPrefix(origin, "https://") {
		allowed[origin] = struct{}{}
	}
	for _, raw := range originAllowlist {
		if origin := pageOrigin(raw); strings.HasPrefix(origin, "http://") || strings.HasPrefix(origin, "https://") {
			allowed[origin] = struct{}{}
		}
	}
	if len(allowed) == 0 && currentOrigin != "" {
		allowed[currentOrigin] = struct{}{}
	}
	return allowed
}

func originAllowed(origin string, allowed map[string]struct{}) bool {
	_, ok := allowed[origin]
	return ok
}

func filterStorageCookies(cookies []map[string]any, allowed map[string]struct{}) []map[string]any {
	filtered := make([]map[string]any, 0, len(cookies))
	for _, cookie := range cookies {
		if cookieAllowed(cookie, allowed) {
			filtered = append(filtered, cookie)
		}
	}
	return filtered
}

func normalizeCookieObjects(value any) []map[string]any {
	cookies := make([]map[string]any, 0)
	for _, raw := range anySlice(value) {
		if obj, ok := raw.(map[string]any); ok {
			cookies = append(cookies, obj)
		}
	}
	return cookies
}

func cookieAllowed(cookie map[string]any, allowed map[string]struct{}) bool {
	cookieDomain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(stringFromAny(firstExistingValue(cookie, "domain", "Domain")))), ".")
	if cookieDomain == "" {
		if cookieURL := strings.TrimSpace(stringFromAny(firstExistingValue(cookie, "url", "URL"))); cookieURL != "" {
			return originAllowed(pageOrigin(cookieURL), allowed)
		}
		return false
	}
	for origin := range allowed {
		parsed, err := url.Parse(origin)
		if err != nil {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if host == cookieDomain || strings.HasSuffix(host, "."+cookieDomain) {
			return true
		}
	}
	return false
}

type browserProjectAuthRuntime struct {
	manager *browser.Manager
}

func newBrowserProjectAuthRuntime(manager *browser.Manager) projectAuthRuntime {
	if manager == nil {
		return nil
	}
	return &browserProjectAuthRuntime{manager: manager}
}

func (r *browserProjectAuthRuntime) CaptureProjectAuthState(ctx context.Context, input map[string]any) (map[string]any, error) {
	state, err := r.captureActiveProjectAuthState(ctx, input)
	if err == nil {
		return state, nil
	}
	if snapshot := r.manager.ConsumeLastRecordingStorageState(browser.RecordingStorageScope{
		ProjectID: uintFromAny(input["project_id"]),
		VersionID: uintFromAny(input["version_id"]),
		PageID:    uintFromAny(input["captured_page_id"]),
	}); snapshot != nil {
		return snapshot, nil
	}
	return nil, err
}

func (r *browserProjectAuthRuntime) captureActiveProjectAuthState(ctx context.Context, input map[string]any) (map[string]any, error) {
	page := r.manager.GetActivePage()
	if page == nil {
		return nil, fmt.Errorf("please open a page first")
	}
	info, err := page.Info()
	if err != nil {
		return nil, err
	}
	origin := pageOrigin(info.URL)
	if !strings.HasPrefix(origin, "http://") && !strings.HasPrefix(origin, "https://") {
		return nil, fmt.Errorf("active page origin is not capturable")
	}
	cookiesValue, err := page.Browser().GetCookies()
	if err != nil {
		return nil, err
	}
	allowedOrigins := allowedProjectOrigins(stringFromAny(input["base_url"]), origin, stringSliceFromAny(input["origin_allowlist"]))
	cookies := filterStorageCookies(normalizeRodCookies(cookiesValue), allowedOrigins)
	storage, err := page.Eval(`() => {
		const readStorage = (storage) => {
			const items = [];
			for (let i = 0; i < storage.length; i++) {
				const name = storage.key(i);
				items.push({ name, value: storage.getItem(name) });
			}
			return items;
		};
		return {
			local_storage: readStorage(window.localStorage),
			session_storage: readStorage(window.sessionStorage),
		};
	}`)
	if err != nil {
		return nil, fmt.Errorf("capture project auth web storage failed: %w", err)
	}
	originState := map[string]any{"origin": origin}
	if storage != nil && originAllowed(origin, allowedOrigins) {
		var storageObj map[string]any
		if data, err := json.Marshal(storage.Value); err == nil {
			_ = json.Unmarshal(data, &storageObj)
		}
		for key, value := range storageObj {
			originState[key] = value
		}
	}
	return map[string]any{
		"schema_version": 1,
		"kind":           "browser_storage_state",
		"captured_url":   firstNonEmptyString(stringFromAny(input["captured_url"]), info.URL),
		"captured_at":    time.Now().UTC().Format(time.RFC3339),
		"origins":        []map[string]any{originState},
		"cookies":        cookies,
		"extensions":     map[string]any{},
	}, nil
}

func (r *browserProjectAuthRuntime) SaveProjectAuthState(context.Context, map[string]any) error {
	return nil
}

func (r *browserProjectAuthRuntime) PrepareTestExecution(ctx context.Context, input map[string]any) error {
	instanceID := stringFromAny(input["browser_instance_id"])
	if !r.manager.IsRunning() {
		if err := r.manager.StartWithoutGlobalCookieStore(ctx); err != nil {
			return err
		}
	}
	return r.manager.OpenIsolatedPage(ctx, "about:blank", "", instanceID)
}

func (r *browserProjectAuthRuntime) StartPageRecording(ctx context.Context, input map[string]any) (map[string]any, error) {
	targetURL := strings.TrimSpace(stringFromAny(input["target_url"]))
	if targetURL == "" {
		return nil, fmt.Errorf("target_url is required")
	}
	instanceID := stringFromAny(input["browser_instance_id"])
	if !r.manager.IsRunning() {
		if err := r.manager.StartWithoutGlobalCookieStore(ctx); err != nil {
			return nil, err
		}
	}
	if err := r.manager.OpenIsolatedPage(ctx, "about:blank", "", instanceID); err != nil {
		return nil, err
	}
	if stringFromAny(input["auth_context"]) == authContextProjectSaved {
		if err := r.RestoreProjectAuthState(ctx, input); err != nil {
			return nil, err
		}
	}
	page := r.manager.GetActivePage()
	if page == nil {
		return nil, fmt.Errorf("isolated recording page is not available")
	}
	if err := page.Timeout(60 * time.Second).Navigate(targetURL); err != nil {
		return nil, fmt.Errorf("failed to navigate isolated recording page: %w", err)
	}
	if err := page.Timeout(60 * time.Second).WaitLoad(); err != nil {
		return nil, fmt.Errorf("failed to wait isolated recording page load: %w", err)
	}
	if err := r.manager.StartRecordingWithStorageScope(ctx, instanceID, browser.RecordingStorageScope{
		ProjectID: uintFromAny(input["project_id"]),
		VersionID: uintFromAny(input["version_id"]),
		PageID:    uintFromAny(input["page_id"]),
	}); err != nil {
		return nil, err
	}
	return map[string]any{"recording_session_id": fmt.Sprintf("project-recording-%d", time.Now().UnixNano())}, nil
}

func (r *browserProjectAuthRuntime) RestoreProjectAuthState(ctx context.Context, input map[string]any) error {
	rawState := strings.TrimSpace(stringFromAny(input["auth_state_json"]))
	if rawState == "" {
		return nil
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(rawState), &state); err != nil {
		return fmt.Errorf("project auth state is corrupted")
	}
	page := r.manager.GetActivePage()
	if page == nil {
		if err := r.manager.OpenIsolatedPage(ctx, "about:blank", "", stringFromAny(input["browser_instance_id"])); err != nil {
			return err
		}
		page = r.manager.GetActivePage()
	}
	if page == nil {
		return fmt.Errorf("browser page is not available")
	}
	cookies := cookiesForRestore(state["cookies"])
	if len(cookies) > 0 {
		if err := page.SetCookies(cookies); err != nil {
			return err
		}
	}
	for _, originValue := range anySlice(state["origins"]) {
		originObj, ok := originValue.(map[string]any)
		if !ok {
			continue
		}
		origin := strings.TrimSpace(stringFromAny(originObj["origin"]))
		if origin == "" {
			continue
		}
		if err := page.Timeout(60 * time.Second).Navigate(origin); err != nil {
			return fmt.Errorf("navigate to auth origin %s failed: %w", origin, err)
		}
		if err := page.Timeout(60 * time.Second).WaitLoad(); err != nil {
			return fmt.Errorf("wait auth origin %s load failed: %w", origin, err)
		}
		script := fmt.Sprintf(`(items) => {
			for (const item of items.local_storage || []) localStorage.setItem(item.name, item.value || "");
			for (const item of items.session_storage || []) sessionStorage.setItem(item.name, item.value || "");
		}`)
		if _, err := page.Eval(script, map[string]any{
			"local_storage":   originObj["local_storage"],
			"session_storage": originObj["session_storage"],
		}); err != nil {
			return fmt.Errorf("restore project auth web storage for %s failed: %w", origin, err)
		}
	}
	return ctx.Err()
}

func normalizeRodCookies(cookiesValue any) []map[string]any {
	value := reflect.ValueOf(cookiesValue)
	if !value.IsValid() || value.Kind() != reflect.Slice {
		return nil
	}
	cookies := make([]map[string]any, 0, value.Len())
	for i := 0; i < value.Len(); i++ {
		item := value.Index(i).Interface()
		data, err := json.Marshal(item)
		if err != nil {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err == nil {
			cookies = append(cookies, obj)
		}
	}
	return cookies
}

func cookiesForRestore(value any) []*proto.NetworkCookieParam {
	cookies := make([]*proto.NetworkCookieParam, 0)
	for _, raw := range anySlice(value) {
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(stringFromAny(obj["name"]))
		if name == "" {
			continue
		}
		cookie := &proto.NetworkCookieParam{
			Name:     name,
			Value:    stringFromAny(obj["value"]),
			Domain:   stringFromAny(obj["domain"]),
			Path:     firstNonEmptyString(stringFromAny(obj["path"]), "/"),
			HTTPOnly: boolFromAny(firstExistingValue(obj, "http_only", "httpOnly")),
			Secure:   boolFromAny(obj["secure"]),
		}
		cookies = append(cookies, cookie)
	}
	return cookies
}

func pageOrigin(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return rawURL
	}
	return parsed.Scheme + "://" + parsed.Host
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func boolFromAny(value any) bool {
	result, _ := value.(bool)
	return result
}

func uintFromAny(value any) uint {
	switch v := value.(type) {
	case uint:
		return v
	case int:
		if v < 0 {
			return 0
		}
		return uint(v)
	case int64:
		if v < 0 {
			return 0
		}
		return uint(v)
	case float64:
		if v < 0 {
			return 0
		}
		return uint(v)
	default:
		return 0
	}
}
