package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"gorm.io/gorm"
)

const (
	authContextClean        = "clean"
	authContextProjectSaved = "project_saved"
	recordingKindLoginFlow  = "login_flow"
	recordingKindBusiness   = "business_flow"
)

var errRecordingSessionStorageSnapshotUnavailable = errors.New("recording session storage snapshot is unavailable")
var errProjectRecordingStartInProgress = errors.New("project recording start is already in progress")
var errProjectRecordingStopInProgress = errors.New("project recording stop is already in progress")

type projectAuthRuntime interface {
	CaptureProjectAuthState(context.Context, map[string]any) (map[string]any, error)
	SaveProjectAuthState(context.Context, map[string]any) error
	StartPageRecording(context.Context, map[string]any) (map[string]any, error)
	StopPageRecording(context.Context, map[string]any) (map[string]any, error)
	ActivePageRecording() (recordingSessionID string, active bool)
	PendingStoppedPageRecording() (recordingSessionID string, pending bool)
	AcknowledgeStoppedPageRecording(context.Context, map[string]any)
	DiscardStoppedPageRecording(context.Context, map[string]any)
	ReleaseRecordingSessionResources(context.Context, map[string]any)
	AcknowledgeProjectAuthStateCapture(context.Context, map[string]any)
	DiscardProjectAuthStateCapture(context.Context, map[string]any)
	HasProjectAuthStateCapture(context.Context, map[string]any) bool
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

// The lifecycle service is long-lived.  Forward every call through the holder
// instead of retaining the runtime present when the router was built, so test
// replacement and Manager recreation are observed without creating a second
// business orchestrator.
func (h *projectAuthRuntimeHolder) CaptureProjectAuthState(ctx context.Context, input map[string]any) (map[string]any, error) {
	return h.runtimeOrUnavailable().CaptureProjectAuthState(ctx, input)
}
func (h *projectAuthRuntimeHolder) SaveProjectAuthState(ctx context.Context, input map[string]any) error {
	return h.runtimeOrUnavailable().SaveProjectAuthState(ctx, input)
}
func (h *projectAuthRuntimeHolder) StartPageRecording(ctx context.Context, input map[string]any) (map[string]any, error) {
	return h.runtimeOrUnavailable().StartPageRecording(ctx, input)
}
func (h *projectAuthRuntimeHolder) StopPageRecording(ctx context.Context, input map[string]any) (map[string]any, error) {
	return h.runtimeOrUnavailable().StopPageRecording(ctx, input)
}
func (h *projectAuthRuntimeHolder) ActivePageRecording() (string, bool) {
	return h.runtimeOrUnavailable().ActivePageRecording()
}
func (h *projectAuthRuntimeHolder) PendingStoppedPageRecording() (string, bool) {
	return h.runtimeOrUnavailable().PendingStoppedPageRecording()
}
func (h *projectAuthRuntimeHolder) AcknowledgeStoppedPageRecording(ctx context.Context, input map[string]any) {
	h.runtimeOrUnavailable().AcknowledgeStoppedPageRecording(ctx, input)
}
func (h *projectAuthRuntimeHolder) DiscardStoppedPageRecording(ctx context.Context, input map[string]any) {
	h.runtimeOrUnavailable().DiscardStoppedPageRecording(ctx, input)
}
func (h *projectAuthRuntimeHolder) ReleaseRecordingSessionResources(ctx context.Context, input map[string]any) {
	h.runtimeOrUnavailable().ReleaseRecordingSessionResources(ctx, input)
}
func (h *projectAuthRuntimeHolder) AcknowledgeProjectAuthStateCapture(ctx context.Context, input map[string]any) {
	h.runtimeOrUnavailable().AcknowledgeProjectAuthStateCapture(ctx, input)
}
func (h *projectAuthRuntimeHolder) DiscardProjectAuthStateCapture(ctx context.Context, input map[string]any) {
	h.runtimeOrUnavailable().DiscardProjectAuthStateCapture(ctx, input)
}
func (h *projectAuthRuntimeHolder) HasProjectAuthStateCapture(ctx context.Context, input map[string]any) bool {
	return h.runtimeOrUnavailable().HasProjectAuthStateCapture(ctx, input)
}
func (h *projectAuthRuntimeHolder) PrepareTestExecution(ctx context.Context, input map[string]any) error {
	return h.runtimeOrUnavailable().PrepareTestExecution(ctx, input)
}
func (h *projectAuthRuntimeHolder) RestoreProjectAuthState(ctx context.Context, input map[string]any) error {
	return h.runtimeOrUnavailable().RestoreProjectAuthState(ctx, input)
}

// These optional lookups are intentionally forwarded by the holder rather
// than captured when LifecycleService is constructed. Recovery must observe a
// replacement Manager/runtime in tests and after runtime recreation, while
// remaining scoped to the session-bound identity.
func (h *projectAuthRuntimeHolder) HasActivePageRecordingScope(ctx context.Context, input map[string]any) bool {
	locator, ok := h.get().(recordingRuntimeScopeLocator)
	return ok && locator.HasActivePageRecordingScope(ctx, input)
}

func (h *projectAuthRuntimeHolder) HasPendingStoppedPageRecordingScope(ctx context.Context, input map[string]any) bool {
	locator, ok := h.get().(recordingRuntimeScopeLocator)
	return ok && locator.HasPendingStoppedPageRecordingScope(ctx, input)
}

func (h *projectAuthRuntimeHolder) runtimeOrUnavailable() projectAuthRuntime {
	if runtime := h.get(); runtime != nil {
		return runtime
	}
	return unavailableProjectAuthRuntime{}
}

func (h *ProjectHandlers) projectAuthRuntime() projectAuthRuntime {
	if h.projectAuth == nil {
		return nil
	}
	return h.projectAuth
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

func (r *injectedProjectAuthRuntime) StopPageRecording(ctx context.Context, input map[string]any) (map[string]any, error) {
	return invokeProjectAuthMapMethod(ctx, r.target, "StopPageRecording", input)
}

func (r *injectedProjectAuthRuntime) ActivePageRecording() (string, bool) {
	method := reflect.ValueOf(r.target).MethodByName("ActivePageRecording")
	if !method.IsValid() || method.Type().NumIn() != 0 || method.Type().NumOut() != 2 {
		return "", false
	}
	output := method.Call(nil)
	recordingSessionID, _ := output[0].Interface().(string)
	active, _ := output[1].Interface().(bool)
	return recordingSessionID, active
}

func (r *injectedProjectAuthRuntime) PendingStoppedPageRecording() (string, bool) {
	method := reflect.ValueOf(r.target).MethodByName("PendingStoppedPageRecording")
	if !method.IsValid() || method.Type().NumIn() != 0 || method.Type().NumOut() != 2 {
		return "", false
	}
	output := method.Call(nil)
	recordingSessionID, _ := output[0].Interface().(string)
	pending, _ := output[1].Interface().(bool)
	return recordingSessionID, pending
}

func (r *injectedProjectAuthRuntime) HasActivePageRecordingScope(ctx context.Context, input map[string]any) bool {
	return r.invokeScopedRuntimeLookup("HasActivePageRecordingScope", ctx, input)
}

func (r *injectedProjectAuthRuntime) HasPendingStoppedPageRecordingScope(ctx context.Context, input map[string]any) bool {
	return r.invokeScopedRuntimeLookup("HasPendingStoppedPageRecordingScope", ctx, input)
}

func (r *injectedProjectAuthRuntime) invokeScopedRuntimeLookup(name string, ctx context.Context, input map[string]any) bool {
	if locator, ok := r.target.(recordingRuntimeScopeLocator); ok {
		switch name {
		case "HasActivePageRecordingScope":
			return locator.HasActivePageRecordingScope(ctx, input)
		case "HasPendingStoppedPageRecordingScope":
			return locator.HasPendingStoppedPageRecordingScope(ctx, input)
		}
	}
	method := reflect.ValueOf(r.target).MethodByName(name)
	if !method.IsValid() || method.Type().NumIn() != 2 || method.Type().NumOut() != 1 || method.Type().Out(0).Kind() != reflect.Bool {
		return false
	}
	if !reflect.TypeOf(ctx).AssignableTo(method.Type().In(0)) || !reflect.TypeOf(input).AssignableTo(method.Type().In(1)) {
		return false
	}
	return method.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(input)})[0].Bool()
}

func (r *injectedProjectAuthRuntime) AcknowledgeStoppedPageRecording(ctx context.Context, input map[string]any) {
	method := reflect.ValueOf(r.target).MethodByName("AcknowledgeStoppedPageRecording")
	if !method.IsValid() || method.Type().NumIn() != 2 || method.Type().NumOut() != 0 {
		return
	}
	if !reflect.TypeOf(ctx).AssignableTo(method.Type().In(0)) || !reflect.TypeOf(input).AssignableTo(method.Type().In(1)) {
		return
	}
	method.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(input)})
}

func (r *injectedProjectAuthRuntime) DiscardStoppedPageRecording(ctx context.Context, input map[string]any) {
	method := reflect.ValueOf(r.target).MethodByName("DiscardStoppedPageRecording")
	if !method.IsValid() || method.Type().NumIn() != 2 || method.Type().NumOut() != 0 {
		return
	}
	if !reflect.TypeOf(ctx).AssignableTo(method.Type().In(0)) || !reflect.TypeOf(input).AssignableTo(method.Type().In(1)) {
		return
	}
	method.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(input)})
}

func (r *injectedProjectAuthRuntime) ReleaseRecordingSessionResources(ctx context.Context, input map[string]any) {
	method := reflect.ValueOf(r.target).MethodByName("ReleaseRecordingSessionResources")
	if !method.IsValid() || method.Type().NumIn() != 2 || method.Type().NumOut() != 0 {
		return
	}
	if !reflect.TypeOf(ctx).AssignableTo(method.Type().In(0)) || !reflect.TypeOf(input).AssignableTo(method.Type().In(1)) {
		return
	}
	method.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(input)})
}

func (r *injectedProjectAuthRuntime) AcknowledgeProjectAuthStateCapture(ctx context.Context, input map[string]any) {
	method := reflect.ValueOf(r.target).MethodByName("AcknowledgeProjectAuthStateCapture")
	if !method.IsValid() || method.Type().NumIn() != 2 || method.Type().NumOut() != 0 {
		return
	}
	if !reflect.TypeOf(ctx).AssignableTo(method.Type().In(0)) || !reflect.TypeOf(input).AssignableTo(method.Type().In(1)) {
		return
	}
	method.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(input)})
}

func (r *injectedProjectAuthRuntime) DiscardProjectAuthStateCapture(ctx context.Context, input map[string]any) {
	method := reflect.ValueOf(r.target).MethodByName("DiscardProjectAuthStateCapture")
	if !method.IsValid() || method.Type().NumIn() != 2 || method.Type().NumOut() != 0 {
		return
	}
	if !reflect.TypeOf(ctx).AssignableTo(method.Type().In(0)) || !reflect.TypeOf(input).AssignableTo(method.Type().In(1)) {
		return
	}
	method.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(input)})
}

func (r *injectedProjectAuthRuntime) HasProjectAuthStateCapture(ctx context.Context, input map[string]any) bool {
	method := reflect.ValueOf(r.target).MethodByName("HasProjectAuthStateCapture")
	if !method.IsValid() || method.Type().NumIn() != 2 || method.Type().NumOut() != 1 || method.Type().Out(0).Kind() != reflect.Bool {
		return false
	}
	if !reflect.TypeOf(ctx).AssignableTo(method.Type().In(0)) || !reflect.TypeOf(input).AssignableTo(method.Type().In(1)) {
		return false
	}
	return method.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(input)})[0].Bool()
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

func (unavailableProjectAuthRuntime) StopPageRecording(context.Context, map[string]any) (map[string]any, error) {
	return nil, fmt.Errorf("Project auth runtime is not configured")
}

func (unavailableProjectAuthRuntime) ActivePageRecording() (string, bool) {
	return "", false
}

func (unavailableProjectAuthRuntime) PendingStoppedPageRecording() (string, bool) {
	return "", false
}

func (unavailableProjectAuthRuntime) AcknowledgeStoppedPageRecording(context.Context, map[string]any) {
}

func (unavailableProjectAuthRuntime) DiscardStoppedPageRecording(context.Context, map[string]any) {
}

func (unavailableProjectAuthRuntime) ReleaseRecordingSessionResources(context.Context, map[string]any) {
}

func (unavailableProjectAuthRuntime) AcknowledgeProjectAuthStateCapture(context.Context, map[string]any) {
}

func (unavailableProjectAuthRuntime) DiscardProjectAuthStateCapture(context.Context, map[string]any) {
}

func (unavailableProjectAuthRuntime) HasProjectAuthStateCapture(context.Context, map[string]any) bool {
	return false
}

func (unavailableProjectAuthRuntime) PrepareTestExecution(context.Context, map[string]any) error {
	return nil
}

func (unavailableProjectAuthRuntime) RestoreProjectAuthState(context.Context, map[string]any) error {
	return fmt.Errorf("Project auth runtime is not configured")
}

type captureProjectAuthStateRequest struct {
	OperationID        string   `json:"operation_id"`
	Name               string   `json:"name"`
	CapturedPageID     uint     `json:"captured_page_id"`
	CapturedURL        string   `json:"captured_url"`
	OriginAllowlist    []string `json:"origin_allowlist"`
	Replace            *bool    `json:"replace"`
	BrowserInstanceID  string   `json:"browser_instance_id"`
	RecordingSessionID string   `json:"recording_session_id"`
}

type startPageRecordingSessionRequest struct {
	OperationID       string `json:"operation_id"`
	RecordingKind     string `json:"recording_kind"`
	AuthContext       string `json:"auth_context"`
	TargetURL         string `json:"target_url"`
	BrowserInstanceID string `json:"browser_instance_id"`
	RuntimePageID     string `json:"runtime_page_id"`
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
	if _, err := h.loadProjectVersion(projectID, versionID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project or version not found"})
		return
	}

	var req captureProjectAuthStateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	recordingSessionID := strings.TrimSpace(req.RecordingSessionID)
	if recordingSessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recording_session_id is required", "code": "recording_session_id_invalid"})
		return
	}
	session, err := h.loadRecordingSessionByID(recordingSessionID)
	if err != nil || session.ProjectID != projectID || session.VersionID != versionID || (req.CapturedPageID != 0 && session.PageID != req.CapturedPageID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "RecordingSession not found", "code": "recording_session_not_found"})
		return
	}
	result, lifecycleErr := h.recordingLifecycleService().Capture(c.Request.Context(), captureRecordingLifecycleInput{OperationID: req.OperationID, ProjectID: projectID, VersionID: versionID, Session: session, Name: req.Name, CapturedURL: req.CapturedURL, OriginAllowlist: req.OriginAllowlist, Replace: req.Replace})
	h.writeRecordingLifecycleResult(c, result, lifecycleErr)
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
	targetURL := strings.TrimSpace(req.TargetURL)
	if targetURL == "" {
		targetURL = buildPageURL(version.BaseURL, page.Path)
	}
	result, lifecycleErr := h.recordingLifecycleService().Start(c.Request.Context(), startRecordingLifecycleInput{
		OperationID: req.OperationID, ProjectID: projectID, VersionID: versionID, PageID: pageID,
		RecordingKind: strings.TrimSpace(req.RecordingKind), AuthContext: strings.TrimSpace(req.AuthContext), TargetURL: targetURL,
		BrowserInstanceID: strings.TrimSpace(req.BrowserInstanceID), RuntimePageID: strings.TrimSpace(req.RuntimePageID), CreatedBy: c.GetString("user_id"),
	})
	h.writeRecordingLifecycleResult(c, result, lifecycleErr)
}

func recordingSessionMeta(session models.RecordingSession) map[string]any {
	return map[string]any{
		"schema_version": 1,
		"recording_kind": session.RecordingKind,
		"auth_context":   session.AuthContext,
		"auth_state_id":  session.SourceAuthStateID,
		"target_url":     session.TargetURL,
	}
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
	if strings.TrimSpace(auth.StateCiphertext) == "" {
		// P4.7.6 deliberately has no plaintext fallback. Existing plaintext must
		// be removed by the explicit deployment migration before this version is
		// enabled; startup never mutates user auth state silently.
		return nil, fmt.Errorf("encrypted project auth state is unavailable")
	}
	cipher, err := newProjectAuthStateCipher(h.config)
	if err != nil {
		return nil, err
	}
	plain, err := cipher.decrypt(auth)
	if err != nil {
		return nil, err
	}
	auth.StateJSON = plain
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
	manager                            *browser.Manager
	captureActiveProjectAuthStateFn    func(context.Context, map[string]any) (map[string]any, error)
	peekRecordingStorageStateFn        func(browser.RecordingStorageScope) map[string]any
	acknowledgeRecordingStorageStateFn func(browser.RecordingStorageScope)
	stopRecordingWithStorageScopeFn    func(context.Context, browser.RecordingStorageScope) ([]models.ScriptAction, []models.DownloadedFile, error)
	recordingArtifactStorageKeyFn      func(models.DownloadedFile) string
}

func newBrowserProjectAuthRuntime(manager *browser.Manager) projectAuthRuntime {
	if manager == nil {
		return nil
	}
	return &browserProjectAuthRuntime{manager: manager}
}

func (r *browserProjectAuthRuntime) CaptureProjectAuthState(ctx context.Context, input map[string]any) (map[string]any, error) {
	scope := browser.RecordingStorageScope{
		ProjectID:          uintFromAny(input["project_id"]),
		VersionID:          uintFromAny(input["version_id"]),
		PageID:             uintFromAny(input["captured_page_id"]),
		RecordingSessionID: stringFromAny(input["recording_session_id"]),
		BrowserInstanceID:  stringFromAny(input["browser_instance_id"]),
		RuntimePageID:      stringFromAny(input["runtime_page_id"]),
		RuntimeGeneration:  stringFromAny(input["runtime_generation"]),
		LeaseGeneration:    stringFromAny(input["lease_generation"]),
	}
	if strings.TrimSpace(scope.RecordingSessionID) != "" {
		operationID := stringFromAny(input["operation_id"])
		if operationID != "" && r.manager != nil {
			state, receiptID, claimGeneration, err := r.manager.ClaimRecordingStorageState(scope, operationID)
			if err != nil {
				return nil, errRecordingSessionStorageSnapshotUnavailable
			}
			state["runtime_snapshot_receipt_id"] = receiptID
			state["runtime_snapshot_claim_generation"] = claimGeneration
			return state, nil
		}
		if snapshot := r.peekRecordingStorageState(scope); snapshot != nil {
			return snapshot, nil
		}
		return nil, errRecordingSessionStorageSnapshotUnavailable
	}

	state, err := r.captureActiveProjectAuthState(ctx, input)
	if err == nil {
		return state, nil
	}
	if snapshot := r.peekRecordingStorageState(scope); snapshot != nil {
		return snapshot, nil
	}
	return nil, err
}

func (r *browserProjectAuthRuntime) captureActiveProjectAuthState(ctx context.Context, input map[string]any) (map[string]any, error) {
	if r.captureActiveProjectAuthStateFn != nil {
		return r.captureActiveProjectAuthStateFn(ctx, input)
	}
	if r.manager == nil {
		return nil, fmt.Errorf("browser manager is not configured")
	}
	instanceID := stringFromAny(input["browser_instance_id"])
	page := r.manager.GetActivePageForInstance(instanceID)
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

func (r *browserProjectAuthRuntime) peekRecordingStorageState(scope browser.RecordingStorageScope) map[string]any {
	if r.peekRecordingStorageStateFn != nil {
		return r.peekRecordingStorageStateFn(scope)
	}
	if r.manager == nil {
		return nil
	}
	return r.manager.PeekLastRecordingStorageState(scope)
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

// recordingLifecycleRuntimeScope translates the persisted lifecycle scope into
// the Manager identity. Lifecycle inputs use page_id; captured_page_id belongs
// only to the standalone auth-capture request shape.
func recordingLifecycleRuntimeScope(input map[string]any) browser.RecordingStorageScope {
	return browser.RecordingStorageScope{
		ProjectID:          uintFromAny(input["project_id"]),
		VersionID:          uintFromAny(input["version_id"]),
		PageID:             uintFromAny(input["page_id"]),
		RecordingSessionID: stringFromAny(input["recording_session_id"]),
		BrowserInstanceID:  stringFromAny(input["browser_instance_id"]),
		RuntimePageID:      stringFromAny(input["runtime_page_id"]),
		RuntimeGeneration:  stringFromAny(input["runtime_generation"]),
		LeaseGeneration:    stringFromAny(input["lease_generation"]),
	}
}

func (r *browserProjectAuthRuntime) StartPageRecording(ctx context.Context, input map[string]any) (map[string]any, error) {
	targetURL := strings.TrimSpace(stringFromAny(input["target_url"]))
	if targetURL == "" {
		return nil, fmt.Errorf("target_url is required")
	}
	instanceID := stringFromAny(input["browser_instance_id"])
	scope := recordingLifecycleRuntimeScope(input)
	driverToken := strings.TrimSpace(stringFromAny(input["runtime_driver_token"]))
	driverGeneration := uint64(uintFromAny(input["runtime_driver_claim_generation"]))
	if r.manager == nil {
		return nil, fmt.Errorf("browser manager is not configured")
	}
	driverCtx, running, err := r.manager.AcquireRecordingStartTarget(ctx, scope, driverToken, driverGeneration)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errProjectRecordingStartInProgress, err)
	}
	if running {
		return map[string]any{"recording_session_id": scope.RecordingSessionID}, nil
	}
	if driverCtx == nil {
		return nil, fmt.Errorf("%w: missing runtime start driver context", errProjectRecordingStartInProgress)
	}
	started := false
	var isolatedPage *rod.Page
	defer func() {
		if !started {
			// The page belongs to this reservation until Recorder.Start commits
			// it. A fenced/cancelled driver must close only its own target before
			// releasing the reservation, never leave it as the instance active
			// page for a later generation.
			r.manager.CloseIsolatedRecordingPage(context.Background(), instanceID, isolatedPage)
			r.manager.ReleaseRecordingStartTarget(scope, driverToken, driverGeneration)
		}
	}()
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-driverCtx.Done():
				return
			case <-ticker.C:
				if r.manager.HeartbeatRecordingStartTarget(scope, driverToken, driverGeneration) != nil {
					return
				}
			}
		}
	}()
	checkFence := func() error {
		if err := driverCtx.Err(); err != nil {
			return fmt.Errorf("%w: runtime start driver was cancelled", errProjectRecordingStartInProgress)
		}
		if err := r.manager.HeartbeatRecordingStartTarget(scope, driverToken, driverGeneration); err != nil {
			return fmt.Errorf("%w: %v", errProjectRecordingStartInProgress, err)
		}
		return nil
	}
	if err := checkFence(); err != nil {
		return nil, err
	}
	if r.manager != nil && r.manager.IsRecordingStorageScopeActive(scope) {
		started = true
		return map[string]any{"recording_session_id": scope.RecordingSessionID}, nil
	}
	if !r.manager.IsRunning() {
		if err := r.manager.StartWithoutGlobalCookieStore(driverCtx); err != nil {
			return nil, err
		}
	}
	if err := checkFence(); err != nil {
		return nil, err
	}
	if err := r.manager.OpenIsolatedPage(driverCtx, "about:blank", "", instanceID); err != nil {
		return nil, err
	}
	isolatedPage = r.manager.GetActivePageForInstance(instanceID)
	if isolatedPage == nil {
		return nil, fmt.Errorf("isolated recording page is not available")
	}
	if stringFromAny(input["auth_context"]) == authContextProjectSaved {
		if err := checkFence(); err != nil {
			return nil, err
		}
		if err := r.RestoreProjectAuthState(driverCtx, input); err != nil {
			return nil, err
		}
	}
	if err := checkFence(); err != nil {
		return nil, err
	}
	page := isolatedPage.Context(driverCtx)
	if err := page.Timeout(60 * time.Second).Navigate(targetURL); err != nil {
		return nil, fmt.Errorf("failed to navigate isolated recording page: %w", err)
	}
	if err := checkFence(); err != nil {
		return nil, err
	}
	if err := page.Timeout(60 * time.Second).WaitLoad(); err != nil {
		return nil, fmt.Errorf("failed to wait isolated recording page load: %w", err)
	}
	if err := checkFence(); err != nil {
		return nil, err
	}
	if err := r.manager.StartRecordingWithStorageScopeReservation(driverCtx, instanceID, scope, driverToken, driverGeneration); err != nil {
		if errors.Is(err, browser.ErrRecordingStartReservationInProgress) {
			return nil, fmt.Errorf("%w: %v", errProjectRecordingStartInProgress, err)
		}
		return nil, err
	}
	started = true
	return map[string]any{"recording_session_id": fmt.Sprintf("project-recording-%d", time.Now().UnixNano())}, nil
}

func (r *browserProjectAuthRuntime) ActivePageRecording() (string, bool) {
	if r.manager == nil {
		return "", false
	}
	scope, active := r.manager.ActiveRecordingStorageScope()
	if !active {
		return "", false
	}
	if scope.RecordingSessionID == "" {
		return "", true
	}
	return scope.RecordingSessionID, true
}

// HasActivePageRecordingScope is the lifecycle recovery adapter. It never
// uses Manager's compatibility/global active pointer: the full persisted
// runtime scope must match before a pending Stop defers its database closure.
func (r *browserProjectAuthRuntime) HasActivePageRecordingScope(_ context.Context, input map[string]any) bool {
	if r.manager == nil {
		return false
	}
	return r.manager.IsRecordingStorageScopeActive(browser.RecordingStorageScope{
		ProjectID:          uintFromAny(input["project_id"]),
		VersionID:          uintFromAny(input["version_id"]),
		PageID:             uintFromAny(input["page_id"]),
		RecordingSessionID: stringFromAny(input["recording_session_id"]),
		BrowserInstanceID:  stringFromAny(input["browser_instance_id"]),
		RuntimePageID:      stringFromAny(input["runtime_page_id"]),
		RuntimeGeneration:  stringFromAny(input["runtime_generation"]),
		LeaseGeneration:    stringFromAny(input["lease_generation"]),
	})
}

func (r *browserProjectAuthRuntime) PendingStoppedPageRecording() (string, bool) {
	if r.manager == nil {
		return "", false
	}
	scope, pending := r.manager.PendingStoppedRecordingStorageScope()
	if !pending {
		return "", false
	}
	return scope.RecordingSessionID, true
}

func (r *browserProjectAuthRuntime) HasPendingStoppedPageRecordingScope(_ context.Context, input map[string]any) bool {
	if r.manager == nil {
		return false
	}
	return r.manager.HasPendingFinalRecordingReceipt(browser.RecordingStorageScope{
		ProjectID:          uintFromAny(input["project_id"]),
		VersionID:          uintFromAny(input["version_id"]),
		PageID:             uintFromAny(input["page_id"]),
		RecordingSessionID: stringFromAny(input["recording_session_id"]),
		BrowserInstanceID:  stringFromAny(input["browser_instance_id"]),
		RuntimePageID:      stringFromAny(input["runtime_page_id"]),
		RuntimeGeneration:  stringFromAny(input["runtime_generation"]),
		LeaseGeneration:    stringFromAny(input["lease_generation"]),
	})
}

func (r *browserProjectAuthRuntime) AcknowledgeStoppedPageRecording(_ context.Context, input map[string]any) {
	if r.manager == nil {
		return
	}
	r.manager.AcknowledgeFinalRecordingReceipt(browser.RecordingStorageScope{
		ProjectID:          uintFromAny(input["project_id"]),
		VersionID:          uintFromAny(input["version_id"]),
		PageID:             uintFromAny(input["page_id"]),
		RecordingSessionID: stringFromAny(input["recording_session_id"]),
		BrowserInstanceID:  stringFromAny(input["browser_instance_id"]),
		RuntimePageID:      stringFromAny(input["runtime_page_id"]),
		RuntimeGeneration:  stringFromAny(input["runtime_generation"]),
		LeaseGeneration:    stringFromAny(input["lease_generation"]),
	}, stringFromAny(input["runtime_final_receipt_id"]), stringFromAny(input["operation_id"]), uint64(uintFromAny(input["runtime_final_receipt_claim_generation"])))
}

func (r *browserProjectAuthRuntime) DiscardStoppedPageRecording(_ context.Context, input map[string]any) {
	if r.manager == nil {
		return
	}
	r.manager.ReleaseFinalRecordingReceipt(browser.RecordingStorageScope{
		ProjectID:          uintFromAny(input["project_id"]),
		VersionID:          uintFromAny(input["version_id"]),
		PageID:             uintFromAny(input["page_id"]),
		RecordingSessionID: stringFromAny(input["recording_session_id"]),
		BrowserInstanceID:  stringFromAny(input["browser_instance_id"]),
		RuntimePageID:      stringFromAny(input["runtime_page_id"]),
		RuntimeGeneration:  stringFromAny(input["runtime_generation"]),
		LeaseGeneration:    stringFromAny(input["lease_generation"]),
	}, stringFromAny(input["runtime_final_receipt_id"]), stringFromAny(input["operation_id"]), uint64(uintFromAny(input["runtime_final_receipt_claim_generation"])))
}

func (r *browserProjectAuthRuntime) ReleaseRecordingSessionResources(_ context.Context, input map[string]any) {
	if r.manager == nil {
		return
	}
	r.manager.ReleaseRecordingSessionResources(browser.RecordingStorageScope{
		ProjectID:          uintFromAny(input["project_id"]),
		VersionID:          uintFromAny(input["version_id"]),
		PageID:             uintFromAny(input["page_id"]),
		RecordingSessionID: stringFromAny(input["recording_session_id"]),
		BrowserInstanceID:  stringFromAny(input["browser_instance_id"]),
		RuntimePageID:      stringFromAny(input["runtime_page_id"]),
		RuntimeGeneration:  stringFromAny(input["runtime_generation"]),
		LeaseGeneration:    stringFromAny(input["lease_generation"]),
	})
}

func (r *browserProjectAuthRuntime) AcknowledgeProjectAuthStateCapture(_ context.Context, input map[string]any) {
	scope := browser.RecordingStorageScope{
		ProjectID:          uintFromAny(input["project_id"]),
		VersionID:          uintFromAny(input["version_id"]),
		PageID:             uintFromAny(input["page_id"]),
		RecordingSessionID: stringFromAny(input["recording_session_id"]),
		BrowserInstanceID:  stringFromAny(input["browser_instance_id"]),
		RuntimePageID:      stringFromAny(input["runtime_page_id"]),
		RuntimeGeneration:  stringFromAny(input["runtime_generation"]),
		LeaseGeneration:    stringFromAny(input["lease_generation"]),
	}
	if r.manager != nil {
		r.manager.AcknowledgeClaimedRecordingStorageState(scope, stringFromAny(input["runtime_snapshot_receipt_id"]), stringFromAny(input["operation_id"]), uint64(uintFromAny(input["runtime_snapshot_claim_generation"])))
		return
	}
	if r.acknowledgeRecordingStorageStateFn != nil {
		r.acknowledgeRecordingStorageStateFn(scope)
	}
}

func (r *browserProjectAuthRuntime) DiscardProjectAuthStateCapture(_ context.Context, input map[string]any) {
	scope := browser.RecordingStorageScope{
		ProjectID:          uintFromAny(input["project_id"]),
		VersionID:          uintFromAny(input["version_id"]),
		PageID:             uintFromAny(input["page_id"]),
		RecordingSessionID: stringFromAny(input["recording_session_id"]),
		BrowserInstanceID:  stringFromAny(input["browser_instance_id"]),
		RuntimePageID:      stringFromAny(input["runtime_page_id"]),
		RuntimeGeneration:  stringFromAny(input["runtime_generation"]),
		LeaseGeneration:    stringFromAny(input["lease_generation"]),
	}
	if r.manager != nil {
		r.manager.ReleaseClaimedRecordingStorageState(scope, stringFromAny(input["runtime_snapshot_receipt_id"]), stringFromAny(input["operation_id"]), uint64(uintFromAny(input["runtime_snapshot_claim_generation"])))
	}
}

func (r *browserProjectAuthRuntime) HasProjectAuthStateCapture(_ context.Context, input map[string]any) bool {
	return r.peekRecordingStorageState(browser.RecordingStorageScope{
		ProjectID:          uintFromAny(input["project_id"]),
		VersionID:          uintFromAny(input["version_id"]),
		PageID:             uintFromAny(input["page_id"]),
		RecordingSessionID: stringFromAny(input["recording_session_id"]),
		BrowserInstanceID:  stringFromAny(input["browser_instance_id"]),
		RuntimePageID:      stringFromAny(input["runtime_page_id"]),
		RuntimeGeneration:  stringFromAny(input["runtime_generation"]),
		LeaseGeneration:    stringFromAny(input["lease_generation"]),
	}) != nil
}

func (r *browserProjectAuthRuntime) StopPageRecording(ctx context.Context, input map[string]any) (map[string]any, error) {
	scope := browser.RecordingStorageScope{
		ProjectID:          uintFromAny(input["project_id"]),
		VersionID:          uintFromAny(input["version_id"]),
		PageID:             uintFromAny(input["page_id"]),
		RecordingSessionID: stringFromAny(input["recording_session_id"]),
		BrowserInstanceID:  stringFromAny(input["browser_instance_id"]),
		RuntimePageID:      stringFromAny(input["runtime_page_id"]),
		RuntimeGeneration:  stringFromAny(input["runtime_generation"]),
		LeaseGeneration:    stringFromAny(input["lease_generation"]),
	}
	operationID := stringFromAny(input["operation_id"])
	var actions []models.ScriptAction
	var downloadedFiles []models.DownloadedFile
	var domSnapshot json.RawMessage
	var err error
	if operationID != "" && r.manager != nil {
		actions, downloadedFiles, err = r.manager.StopRecordingWithClaim(ctx, scope, operationID)
	} else {
		actions, downloadedFiles, err = r.stopRecordingWithStorageScope(ctx, scope)
	}
	if err != nil {
		if errors.Is(err, browser.ErrRecordingStopInProgress) {
			return nil, errProjectRecordingStopInProgress
		}
		return nil, err
	}
	if r.manager != nil {
		domSnapshot = r.manager.FinalRecordingDOMSnapshot(scope)
	}
	artifacts := make([]map[string]any, 0, len(downloadedFiles))
	for _, file := range downloadedFiles {
		storagePath := r.recordingArtifactStorageKey(file)
		if storagePath == "" {
			logger.Warn(ctx, "Skipping uncontrolled download artifact metadata")
			continue
		}
		artifacts = append(artifacts, map[string]any{
			"artifact_type":   "download",
			"storage_backend": "local",
			"storage_path":    storagePath,
			"file_name":       file.FileName,
			"mime_type":       file.MimeType,
			"size_bytes":      file.Size,
			"sensitive":       true,
		})
	}
	receiptID := ""
	receiptRevision := uint64(0)
	if r.manager != nil {
		receiptID, receiptRevision, claimGeneration := r.manager.FinalRecordingReceiptInfo(scope)
		return map[string]any{
			"actions":                                actions,
			"artifacts":                              artifacts,
			"dom_snapshot":                           domSnapshot,
			"runtime_final_receipt_id":               receiptID,
			"runtime_final_receipt_claim_generation": claimGeneration,
			"runtime_final_sync_revision":            receiptRevision,
		}, nil
	}
	return map[string]any{
		"actions":                     actions,
		"artifacts":                   artifacts,
		"dom_snapshot":                domSnapshot,
		"runtime_final_receipt_id":    receiptID,
		"runtime_final_sync_revision": receiptRevision,
	}, nil
}

func (r *browserProjectAuthRuntime) stopRecordingWithStorageScope(ctx context.Context, scope browser.RecordingStorageScope) ([]models.ScriptAction, []models.DownloadedFile, error) {
	if r.stopRecordingWithStorageScopeFn != nil {
		return r.stopRecordingWithStorageScopeFn(ctx, scope)
	}
	if r.manager == nil {
		return nil, nil, fmt.Errorf("browser manager is not configured")
	}
	return r.manager.StopRecordingWithStorageScope(ctx, scope)
}

func (r *browserProjectAuthRuntime) recordingArtifactStorageKey(file models.DownloadedFile) string {
	if r.recordingArtifactStorageKeyFn != nil {
		return r.recordingArtifactStorageKeyFn(file)
	}
	if r.manager == nil {
		return ""
	}
	return r.manager.RecordingArtifactStorageKey(file)
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
	instanceID := stringFromAny(input["browser_instance_id"])
	page := r.manager.GetActivePageForInstance(instanceID)
	if page == nil {
		if err := r.manager.OpenIsolatedPage(ctx, "about:blank", "", instanceID); err != nil {
			return err
		}
		page = r.manager.GetActivePageForInstance(instanceID)
	}
	if page == nil {
		return fmt.Errorf("browser page is not available")
	}
	cookies := cookiesForRestore(state["cookies"])
	if len(cookies) > 0 {
		if err := page.Context(ctx).SetCookies(cookies); err != nil {
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
		if err := page.Context(ctx).Timeout(60 * time.Second).Navigate(origin); err != nil {
			return fmt.Errorf("navigate to auth origin %s failed: %w", origin, err)
		}
		if err := page.Context(ctx).Timeout(60 * time.Second).WaitLoad(); err != nil {
			return fmt.Errorf("wait auth origin %s load failed: %w", origin, err)
		}
		script := fmt.Sprintf(`(items) => {
			for (const item of items.local_storage || []) localStorage.setItem(item.name, item.value || "");
			for (const item of items.session_storage || []) sessionStorage.setItem(item.name, item.value || "");
		}`)
		if _, err := page.Context(ctx).Eval(script, map[string]any{
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
	case uint64:
		return uint(v)
	case uint32:
		return uint(v)
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
