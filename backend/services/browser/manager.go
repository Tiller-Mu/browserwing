package browser

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/browserwing/browserwing/config"
	"github.com/browserwing/browserwing/llm"
	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/pkg/logger"
	"github.com/browserwing/browserwing/storage"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	"github.com/google/uuid"
)

//go:embed scripts/float_button.js
var floatButtonScript string

//go:embed scripts/xhr_interceptor.js
var xhrInterceptorScriptForManager string

var callBrowserDownloadBehavior = func(browser *rod.Browser, request *proto.BrowserSetDownloadBehavior) error {
	return request.Call(browser)
}

var errRecordingReceiptUnavailable = errors.New("recording runtime receipt is unavailable")
var ErrRecordingStartReservationInProgress = errors.New("recording start target is already reserved")
var errRecordingStartReservationInProgress = ErrRecordingStartReservationInProgress
var ErrRecordingStopInProgress = errors.New("recording stop is already in progress")

func commonChromiumBinaryPaths() []string {
	paths := []string{
		"/usr/bin/google-chrome",
		"/usr/bin/chromium-browser",
		"/usr/bin/chromium",
		"/usr/bin/google-chrome-stable",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
		"C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe",
		"C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe",
		"C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe",
	}

	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		paths = append(paths,
			filepath.Join(homeDir, "AppData", "Local", "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(homeDir, "AppData", "Local", "Microsoft", "Edge", "Application", "msedge.exe"),
		)
	}

	return paths
}

func findLocalChromiumBinary() string {
	for _, path := range commonChromiumBinaryPaths() {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func isBrowserBinaryNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "can't find a browser binary")
}

func isEdgeBinary(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == "msedge" || base == "msedge.exe"
}

func shouldUseEdgeDefaultUserDataDir(binPath, userDataDir string) bool {
	if !isEdgeBinary(binPath) {
		return false
	}
	normalized := strings.ToLower(filepath.Clean(userDataDir))
	return normalized == "chrome_user_data" || normalized == filepath.Clean("./chrome_user_data")
}

func applyChromiumLaunchArg(l *launcher.Launcher, arg string) *launcher.Launcher {
	arg = strings.TrimSpace(strings.TrimPrefix(arg, "--"))
	if arg == "" {
		return l
	}

	parts := strings.SplitN(arg, "=", 2)
	name := parts[0]
	switch name {
	case "excludeSwitches", "useAutomationExtension":
		return l
	}

	if len(parts) == 2 {
		return l.Set(flags.Flag(name), parts[1])
	}
	return l.Set(flags.Flag(name))
}

func cloneLauncherForRetry(source *launcher.Launcher) *launcher.Launcher {
	retry := launcher.New()
	retry.Flags = make(map[flags.Flag][]string, len(source.Flags))
	for flag, values := range source.Flags {
		retry.Flags[flag] = append([]string(nil), values...)
	}
	return retry
}

const browserProfileOwnerWriteAttempts = 2

var (
	writeBrowserProfileOwnerAfterLaunch  = writeBrowserProfileOwner
	killLauncherAfterProfileOwnerFailure = func(l *launcher.Launcher) {
		l.Kill()
	}
	browserProfileOwnerRetryDelay = 100 * time.Millisecond
)

// persistLaunchedBrowserProfileOwner makes the profile-recovery marker part of
// local browser startup. Without a durable marker, a later process crash cannot
// safely identify the profile owner, so the newly launched browser must stop.
func persistLaunchedBrowserProfileOwner(ctx context.Context, l *launcher.Launcher, userDataDir, controlURL string) error {
	if strings.TrimSpace(userDataDir) == "" {
		return nil
	}

	owner := browserProfileOwner{
		PID:        l.PID(),
		ControlURL: controlURL,
	}
	var lastErr error
	for attempt := 1; attempt <= browserProfileOwnerWriteAttempts; attempt++ {
		if err := writeBrowserProfileOwnerAfterLaunch(userDataDir, owner); err == nil {
			return nil
		} else {
			lastErr = err
			logger.Warn(ctx, "Failed to persist browser profile owner marker (attempt %d/%d): %v", attempt, browserProfileOwnerWriteAttempts, err)
		}
		if attempt < browserProfileOwnerWriteAttempts && browserProfileOwnerRetryDelay > 0 {
			time.Sleep(browserProfileOwnerRetryDelay)
		}
	}

	killLauncherAfterProfileOwnerFailure(l)
	return fmt.Errorf("persist browser profile owner marker after %d attempts: %w", browserProfileOwnerWriteAttempts, lastErr)
}

// parseProxyURL 解析代理 URL，提取认证信息和地址
// 输入: http://user:pass@host:port 或 socks5://user:pass@host:port
// 返回: (proxyAddr, username, password, error)
// proxyAddr 格式: protocol://host:port
func parseProxyURL(proxyURL string) (string, string, string, error) {
	if proxyURL == "" {
		return "", "", "", nil
	}

	u, err := url.Parse(proxyURL)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid proxy URL: %w", err)
	}

	var username, password string
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}

	// 重建不带认证信息的代理地址
	proxyAddr := fmt.Sprintf("%s://%s", u.Scheme, u.Host)

	return proxyAddr, username, password, nil
}

// resolveWebSocketURL 从 HTTP control URL 解析 WebSocket URL
// 如果输入已经是 ws:// 或 wss:// URL，则直接返回
// 如果是 http:// 或 https:// URL，则查询 /json/version 获取 webSocketDebuggerUrl
func resolveWebSocketURL(controlURL string) (string, error) {
	// 如果已经是 WebSocket URL，直接返回
	if strings.HasPrefix(controlURL, "ws://") || strings.HasPrefix(controlURL, "wss://") {
		return controlURL, nil
	}

	// HTTP URL，需要查询 /json/version
	versionURL := strings.TrimRight(controlURL, "/") + "/json/version"
	resp, err := http.Get(versionURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch browser version from %s: %w", versionURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("unexpected status code %d from %s: %s", resp.StatusCode, versionURL, string(body))
	}

	var result struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response from %s: %w", versionURL, err)
	}

	if result.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("webSocketDebuggerUrl not found in response from %s", versionURL)
	}

	return result.WebSocketDebuggerURL, nil
}

// AgentManagerInterface 定义 Agent 管理器需要的接口
// 避免直接依赖 agent 包
type AgentManagerInterface interface {
	// SendMessageInterface 发送消息并流式返回响应
	// sessionID: 会话ID，如果不存在会自动创建
	// userMessage: 用户输入的消息
	// streamChan: 用于接收流式响应块的通道
	// llmConfigID: LLM配置ID，为空则使用默认配置
	SendMessageInterface(ctx context.Context, sessionID, userMessage string, streamChan chan<- any, llmConfigID string) error
}

// BrowserInstanceRuntime 浏览器实例运行时信息
type BrowserInstanceRuntime struct {
	instance   *models.BrowserInstance // 实例配置
	browser    *rod.Browser            // 浏览器对象
	launcher   *launcher.Launcher      // 启动器（仅本地模式）
	activePage *rod.Page               // 当前活动页面
	startTime  time.Time               // 启动时间
}

type startInstanceOptions struct {
	openStartupPage bool
}

type recordingReceiptState string

const (
	recordingReceiptAvailable recordingReceiptState = "available"
	recordingReceiptClaimed   recordingReceiptState = "claimed"
	recordingReceiptAcked     recordingReceiptState = "acked"
	recordingReceiptReleased  recordingReceiptState = "released"
	recordingReceiptExpired   recordingReceiptState = "expired"
)

const (
	recordingReceiptClaimTTL = 5 * time.Minute
	recordingReceiptTTL      = 15 * time.Minute
)

// RecordingRuntimeEvent is a session-bound runtime observation. Manager emits
// it but never interprets database lifecycle state; the application-level
// lifecycle coordinator is the only consumer allowed to commit it.
type RecordingRuntimeEvent struct {
	ID              string
	Kind            string // draft_sync | recording_stopped | recording_receipt_expired | runtime_lease_lost
	Scope           RecordingStorageScope
	ReceiptID       string
	OperationID     string
	ClaimGeneration uint64
	SyncRevision    uint64
	Actions         []models.ScriptAction
	DOMSnapshot     json.RawMessage
}

type RecordingRuntimeEventSink func(RecordingRuntimeEvent)

// recordingFinalReceipt is runtime-only state. It never decides a recording
// session business status; PostgreSQL does that in RecordingLifecycleService.
type recordingFinalReceipt struct {
	receiptID        string
	scope            RecordingStorageScope
	page             *rod.Page
	startURL         string
	actions          []models.ScriptAction
	downloadedFiles  []models.DownloadedFile
	domSnapshot      json.RawMessage
	syncRevision     uint64
	state            recordingReceiptState
	claimOperationID string
	claimedAt        time.Time
	claimGeneration  uint64
	frozenAt         time.Time
}

type recordingAuthSnapshotReceipt struct {
	receiptID        string
	scope            RecordingStorageScope
	storageState     map[string]any
	state            recordingReceiptState
	claimOperationID string
	claimedAt        time.Time
	claimGeneration  uint64
	frozenAt         time.Time
}

type recordingRuntimeRegistry struct {
	activeByInstance   map[string]RecordingStorageScope
	startBySession     map[string]*recordingStartReservation
	stopBySession      map[string]*recordingStopReservation
	draftBySession     map[string]*recordingDraftReceipt
	finalBySession     map[string]*recordingFinalReceipt
	authBySession      map[string]*recordingAuthSnapshotReceipt
	expiredBySession   map[string]RecordingRuntimeEvent
	leaseLostBySession map[string]RecordingRuntimeEvent
}

// recordingStopReservation prevents two callers from running a potentially
// multi-second Recorder stop for the same scope while Manager's global mutex
// is intentionally released for other browser instances.
type recordingStopReservation struct {
	scope       RecordingStorageScope
	operationID string
}

// recordingStartReservation is runtime-only fencing state. PostgreSQL owns
// the business operation, while this prevents two local goroutines from
// driving the same browser target before an active recorder is registered.
type recordingStartReservation struct {
	scope      RecordingStorageScope
	token      string
	generation uint64
	state      string // reserved | cancelling | running
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	doneOnce   sync.Once
	heartbeat  time.Time
}

// recordingDraftReceipt keeps only the latest complete draft per session.
// It is a re-drive queue entry, not a second lifecycle fact source.
type recordingDraftReceipt struct {
	event RecordingRuntimeEvent
}

func newRecordingRuntimeRegistry() *recordingRuntimeRegistry {
	return &recordingRuntimeRegistry{activeByInstance: make(map[string]RecordingStorageScope), startBySession: make(map[string]*recordingStartReservation), stopBySession: make(map[string]*recordingStopReservation), draftBySession: make(map[string]*recordingDraftReceipt), finalBySession: make(map[string]*recordingFinalReceipt), authBySession: make(map[string]*recordingAuthSnapshotReceipt), expiredBySession: make(map[string]RecordingRuntimeEvent), leaseLostBySession: make(map[string]RecordingRuntimeEvent)}
}

// Manager 浏览器管理器
type Manager struct {
	config       *config.Config
	db           storage.Store
	llmManager   *llm.Manager
	agentManager AgentManagerInterface // Agent 管理器接口（用于 AI 控制功能）
	mu           sync.Mutex
	recorder     *Recorder
	recorders    map[string]*Recorder // browser instance ID -> recorder; runtime-only

	// 多实例管理
	instances         map[string]*BrowserInstanceRuntime // 实例 ID -> 运行时信息
	currentInstanceID string                             // 当前活动实例 ID

	// 共享配置
	defaultBrowserConfig    *models.BrowserConfig   // 默认浏览器配置
	siteConfigs             []*models.BrowserConfig // 网站特定配置列表
	recordingRegistry       *recordingRuntimeRegistry
	runtimeEventSink        RecordingRuntimeEventSink
	receiptJanitorOnce      sync.Once
	projectDownloadsDenied  bool         // 项目录制期间拒绝真实下载
	projectDownloadBrowser  *rod.Browser // 需要恢复下载策略的浏览器
	projectDownloadBrowsers map[*rod.Browser]struct{}
	currentLanguage         string // 当前前端语言设置
	downloadPath            string // 下载目录路径

	// 向后兼容（废弃）
	browser    *rod.Browser
	launcher   *launcher.Launcher
	isRunning  bool
	startTime  time.Time
	activePage *rod.Page
}

// NewManager 创建浏览器管理器
func NewManager(cfg *config.Config, db storage.Store, llmManager *llm.Manager) *Manager {
	recorder := NewRecorder()
	// 设置 API 服务器端口
	if cfg.Server != nil && cfg.Server.Port != "" {
		recorder.SetAPIServerPort(cfg.Server.Port)
	}

	// 设置 LLM 管理器
	if llmManager != nil {
		recorder.SetLLMManager(llmManager)
	}

	// 设置数据库接口
	if db != nil {
		recorder.SetDB(db)
	}

	manager := &Manager{
		config:            cfg,
		db:                db,
		llmManager:        llmManager,
		recorder:          recorder,
		recorders:         map[string]*Recorder{"default": recorder},
		instances:         make(map[string]*BrowserInstanceRuntime),
		recordingRegistry: newRecordingRuntimeRegistry(),
	}
	recorder.SetRecordingDraftSink(manager.publishRecorderDraft)
	return manager
}

func (m *Manager) configuredRecorderLocked() *Recorder {
	recorder := NewRecorder()
	if m.config != nil && m.config.Server != nil && m.config.Server.Port != "" {
		recorder.SetAPIServerPort(m.config.Server.Port)
	}
	if m.llmManager != nil {
		recorder.SetLLMManager(m.llmManager)
	}
	if m.db != nil {
		recorder.SetDB(m.db)
	}
	if m.downloadPath != "" {
		recorder.SetDownloadPath(m.downloadPath)
	}
	return recorder
}

func (m *Manager) recordingInstanceIDLocked(instanceID string) string {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		instanceID = strings.TrimSpace(m.currentInstanceID)
	}
	if instanceID == "" {
		return "default"
	}
	return instanceID
}

func (m *Manager) recorderForInstanceLocked(instanceID string) *Recorder {
	instanceID = m.recordingInstanceIDLocked(instanceID)
	if m.recorders == nil {
		m.recorders = make(map[string]*Recorder)
	}
	if recorder := m.recorders[instanceID]; recorder != nil {
		return recorder
	}
	if instanceID == "default" && m.recorder != nil {
		m.recorders[instanceID] = m.recorder
		return m.recorder
	}
	recorder := m.configuredRecorderLocked()
	recorder.SetRecordingDraftSink(m.publishRecorderDraft)
	m.recorders[instanceID] = recorder
	return recorder
}

func (m *Manager) recorderForScopeLocked(scope RecordingStorageScope) *Recorder {
	for instanceID, activeScope := range m.recordingRuntimeRegistryLocked().activeByInstance {
		if activeScope.matches(scope) {
			return m.recorderForInstanceLocked(instanceID)
		}
	}
	for _, recorder := range m.recorders {
		if recorder != nil && recorder.PeekLastStorageState(scope) != nil {
			return recorder
		}
	}
	return m.recorderForInstanceLocked("")
}

// SetAgentManager 设置 Agent 管理器
func (m *Manager) SetAgentManager(agentManager AgentManagerInterface) {
	m.agentManager = agentManager
}

// SetRecordingRuntimeEventSink connects runtime observations to the
// application-owned coordinator. Manager only emits scoped data/receipt IDs;
// it never reads a RecordingSession or chooses a business terminal state.
func (m *Manager) SetRecordingRuntimeEventSink(sink RecordingRuntimeEventSink) {
	m.mu.Lock()
	m.runtimeEventSink = sink
	m.mu.Unlock()
}

// StartRecordingReceiptJanitor expires unclaimed stopped receipts/snapshots.
// Active claims are intentionally preserved until their shorter claim timeout
// has elapsed, so a transient DB failure can retry with the same operation.
func (m *Manager) StartRecordingReceiptJanitor(ctx context.Context) {
	m.receiptJanitorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					m.mu.Lock()
					m.cleanupRuntimeReceiptsLocked(time.Now().UTC())
					m.mu.Unlock()
				case <-ctx.Done():
					return
				}
			}
		}()
	})
}

func (m *Manager) publishRecorderDraft(scope RecordingStorageScope, revision uint64, actions []models.ScriptAction, domSnapshot json.RawMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if scope.isZero() || !m.scopeIsActiveLocked(scope) {
		return
	}
	event := RecordingRuntimeEvent{
		ID:           deterministicRuntimeEventID("draft_sync", scope, revision, ""),
		Kind:         "draft_sync",
		Scope:        scope,
		SyncRevision: revision,
		Actions:      append([]models.ScriptAction(nil), actions...),
		DOMSnapshot:  append(json.RawMessage(nil), domSnapshot...),
	}
	m.recordingRuntimeRegistryLocked().draftBySession[scope.RecordingSessionID] = &recordingDraftReceipt{event: cloneRecordingRuntimeEvent(event)}
	m.emitRuntimeEventLocked(event)
}

func (m *Manager) scopeIsActiveLocked(scope RecordingStorageScope) bool {
	for _, active := range m.recordingRuntimeRegistryLocked().activeByInstance {
		if active.matches(scope) {
			return true
		}
	}
	return false
}

func (m *Manager) emitRuntimeEventLocked(event RecordingRuntimeEvent) {
	sink := m.runtimeEventSink
	if sink == nil {
		return
	}
	// Runtime event delivery must not block the browser manager. The receipt
	// stays in the registry until the lifecycle path ACKs/releases it, so a
	// retry can safely re-drive the same deterministic operation.
	go sink(event)
}

func cloneRecordingRuntimeEvent(event RecordingRuntimeEvent) RecordingRuntimeEvent {
	event.Actions = append([]models.ScriptAction(nil), event.Actions...)
	event.DOMSnapshot = append(json.RawMessage(nil), event.DOMSnapshot...)
	return event
}

// PendingRecordingRuntimeEvents exposes unacknowledged runtime observations to
// the application coordinator. The registry remains the only runtime fact
// source; this method deliberately makes no lifecycle/database decisions.
func (m *Manager) PendingRecordingRuntimeEvents() []RecordingRuntimeEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupRuntimeReceiptsLocked(time.Now().UTC())
	registry := m.recordingRuntimeRegistryLocked()
	events := make([]RecordingRuntimeEvent, 0, len(registry.draftBySession)+len(registry.finalBySession)+len(registry.expiredBySession)+len(registry.leaseLostBySession))
	for _, draft := range registry.draftBySession {
		if draft != nil {
			events = append(events, cloneRecordingRuntimeEvent(draft.event))
		}
	}
	for _, receipt := range registry.finalBySession {
		if receipt == nil || (receipt.state != recordingReceiptAvailable && receipt.state != recordingReceiptClaimed) {
			continue
		}
		events = append(events, RecordingRuntimeEvent{
			// Event identity belongs to the immutable final receipt, not to the
			// temporary claim owner.  Claim handoff must retain retry/backoff state.
			ID:              deterministicRuntimeEventID("recording_stopped", receipt.scope, receipt.syncRevision, receipt.receiptID),
			Kind:            "recording_stopped",
			Scope:           receipt.scope,
			ReceiptID:       receipt.receiptID,
			OperationID:     receipt.claimOperationID,
			ClaimGeneration: receipt.claimGeneration,
			SyncRevision:    receipt.syncRevision,
			Actions:         append([]models.ScriptAction(nil), receipt.actions...),
			DOMSnapshot:     append(json.RawMessage(nil), receipt.domSnapshot...),
		})
	}
	for _, event := range registry.expiredBySession {
		events = append(events, cloneRecordingRuntimeEvent(event))
	}
	for _, event := range registry.leaseLostBySession {
		events = append(events, cloneRecordingRuntimeEvent(event))
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].Scope.RecordingSessionID != events[j].Scope.RecordingSessionID {
			return events[i].Scope.RecordingSessionID < events[j].Scope.RecordingSessionID
		}
		if events[i].Kind != events[j].Kind {
			return events[i].Kind == "draft_sync"
		}
		return events[i].ID < events[j].ID
	})
	return events
}

// AcknowledgeRecordingRuntimeEvent removes only a successfully persisted
// observation. Final receipts are acknowledged through their dedicated ACK
// after Stop commits; expired tombstones are acknowledged here after the
// lifecycle has converged the session.
func (m *Manager) AcknowledgeRecordingRuntimeEvent(event RecordingRuntimeEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	registry := m.recordingRuntimeRegistryLocked()
	if draft := registry.draftBySession[event.Scope.RecordingSessionID]; draft != nil && draft.event.ID == event.ID {
		delete(registry.draftBySession, event.Scope.RecordingSessionID)
	}
	if lost, ok := registry.leaseLostBySession[event.Scope.RecordingSessionID]; ok && lost.ID == event.ID {
		delete(registry.leaseLostBySession, event.Scope.RecordingSessionID)
	}
	if expired, ok := registry.expiredBySession[event.Scope.RecordingSessionID]; ok && expired.ID == event.ID {
		delete(registry.expiredBySession, event.Scope.RecordingSessionID)
	}
}

// ReleaseRecordingRuntimeEvent consumes only the event that reached a durable
// event-local discard outcome. It must never discard another receipt merely
// because the two observations share a recording session; final/auth/start
// cleanup is a separate session-level operation after LifecycleService has
// committed an incompatible terminal state.
func (m *Manager) ReleaseRecordingRuntimeEvent(event RecordingRuntimeEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	registry := m.recordingRuntimeRegistryLocked()
	switch event.Kind {
	case "draft_sync":
		if draft := registry.draftBySession[event.Scope.RecordingSessionID]; draft != nil && draft.event.ID == event.ID {
			delete(registry.draftBySession, event.Scope.RecordingSessionID)
		}
	case "recording_stopped":
		if receipt := registry.finalBySession[event.Scope.RecordingSessionID]; receipt != nil && receipt.scope.matches(event.Scope) && receipt.receiptID == event.ReceiptID && strings.TrimSpace(event.OperationID) != "" && receipt.claimOperationID == strings.TrimSpace(event.OperationID) && event.ClaimGeneration != 0 && receipt.claimGeneration == event.ClaimGeneration {
			receipt.state = recordingReceiptReleased
			delete(registry.finalBySession, event.Scope.RecordingSessionID)
		}
	case "recording_receipt_expired":
		if expired, ok := registry.expiredBySession[event.Scope.RecordingSessionID]; ok && expired.ID == event.ID {
			delete(registry.expiredBySession, event.Scope.RecordingSessionID)
		}
	case "runtime_lease_lost":
		if lost, ok := registry.leaseLostBySession[event.Scope.RecordingSessionID]; ok && lost.ID == event.ID {
			delete(registry.leaseLostBySession, event.Scope.RecordingSessionID)
		}
	}
}

// ReleaseRecordingSessionResources discards every runtime-only observation for
// one exact scope after LifecycleService has durably chosen an incompatible
// business terminal state. Manager never makes that decision itself.
func (m *Manager) ReleaseRecordingSessionResources(scope RecordingStorageScope) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseRecordingSessionResourcesLocked(scope)
}

func (m *Manager) releaseRecordingSessionResourcesLocked(scope RecordingStorageScope) {
	registry := m.recordingRuntimeRegistryLocked()
	for instanceID, active := range registry.activeByInstance {
		if active.matches(scope) {
			delete(registry.activeByInstance, instanceID)
		}
	}
	if reservation := registry.startBySession[scope.RecordingSessionID]; reservation != nil && reservation.scope.matches(scope) {
		if reservation.cancel != nil {
			reservation.cancel()
		}
		delete(registry.startBySession, scope.RecordingSessionID)
		if reservation.done != nil {
			reservation.doneOnce.Do(func() { close(reservation.done) })
		}
	}
	if stopping := registry.stopBySession[scope.RecordingSessionID]; stopping != nil && stopping.scope.matches(scope) {
		delete(registry.stopBySession, scope.RecordingSessionID)
	}
	if draft := registry.draftBySession[scope.RecordingSessionID]; draft != nil && draft.event.Scope.matches(scope) {
		delete(registry.draftBySession, scope.RecordingSessionID)
	}
	if receipt := registry.finalBySession[scope.RecordingSessionID]; receipt != nil && receipt.scope.matches(scope) {
		receipt.state = recordingReceiptReleased
		delete(registry.finalBySession, scope.RecordingSessionID)
	}
	if receipt := registry.authBySession[scope.RecordingSessionID]; receipt != nil && receipt.scope.matches(scope) {
		receipt.state = recordingReceiptReleased
		delete(registry.authBySession, scope.RecordingSessionID)
	}
	if expired, ok := registry.expiredBySession[scope.RecordingSessionID]; ok && expired.Scope.matches(scope) {
		delete(registry.expiredBySession, scope.RecordingSessionID)
	}
	if lost, ok := registry.leaseLostBySession[scope.RecordingSessionID]; ok && lost.Scope.matches(scope) {
		delete(registry.leaseLostBySession, scope.RecordingSessionID)
	}
}

func deterministicRuntimeEventID(kind string, scope RecordingStorageScope, revision uint64, receiptID string) string {
	name := fmt.Sprintf("%s|%d|%d|%d|%s|%s|%s|%d|%s", kind, scope.ProjectID, scope.VersionID, scope.PageID, scope.RecordingSessionID, scope.RuntimeGeneration, scope.RuntimePageID, revision, receiptID)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}

// runtimeStopOperationID is the stable runtime-driver identity for an
// in-page Stop. It is not another database lifecycle path: Coordinator uses
// this exact UUID when it asks RecordingLifecycleService to commit the receipt.
func runtimeStopOperationID(scope RecordingStorageScope) string {
	name := fmt.Sprintf("runtime-stop|%d|%d|%d|%s|%s|%s|%s", scope.ProjectID, scope.VersionID, scope.PageID, scope.RecordingSessionID, scope.BrowserInstanceID, scope.RuntimePageID, scope.RuntimeGeneration)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}

// GetConfig returns the manager's Config reference (read-only, for path inspection).
func (m *Manager) GetConfig() *config.Config {
	return m.config
}

// AdoptBrowser registers an externally-connected rod.Browser into the instance
// system so that subsequent calls to getInstanceBrowser / IsRunning work correctly.
// This is used to "adopt" an orphaned Chrome that was reconnected via DevToolsActivePort.
func (m *Manager) AdoptBrowser(ctx context.Context, instanceID string, browser *rod.Browser) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if instanceID == "" {
		instanceID = "default"
	}

	// Load instance metadata from DB (best-effort)
	instance, _ := m.db.GetBrowserInstance(instanceID)

	rt := &BrowserInstanceRuntime{
		instance:  instance,
		browser:   browser,
		startTime: time.Now(),
	}

	m.instances[instanceID] = rt

	// Update backward-compat legacy fields
	if m.currentInstanceID == "" || m.currentInstanceID == instanceID {
		m.currentInstanceID = instanceID
		m.browser = browser
		m.isRunning = true
		m.startTime = rt.startTime
	}

	logger.Info(ctx, "✓ Adopted orphaned browser into instance: %s", instanceID)
}

// Start 启动浏览器
func (m *Manager) Start(ctx context.Context) error {
	return m.start(ctx, true)
}

// StartWithoutGlobalCookieStore 启动浏览器但不加载固定 browser Cookie Store。
func (m *Manager) StartWithoutGlobalCookieStore(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.startInstanceInternalWithOptions(ctx, "", startInstanceOptions{openStartupPage: false})
}

func (m *Manager) start(ctx context.Context, loadGlobalCookieStore bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.isRunning {
		return nil
	}

	logger.Info(ctx, "Starting browser...")

	// 加载默认配置
	defaultConfig, err := m.db.GetDefaultBrowserConfig()
	if err != nil {
		logger.Warn(ctx, "Default browser configuration not found, using system defaults")
		defaultConfig = m.getDefaultBrowserConfig()
	}
	m.defaultBrowserConfig = defaultConfig

	// 加载所有网站特定配置
	allConfigs, err := m.db.ListBrowserConfigs()
	if err != nil {
		logger.Warn(ctx, "Failed to load site configurations: %v", err)
		m.siteConfigs = []*models.BrowserConfig{}
	} else {
		// 过滤出有URL模式的配置
		m.siteConfigs = []*models.BrowserConfig{}
		for i := range allConfigs {
			if allConfigs[i].URLPattern != "" && !allConfigs[i].IsDefault {
				m.siteConfigs = append(m.siteConfigs, &allConfigs[i])
			}
		}
		logger.Info(ctx, "Loaded %d site-specific configurations", len(m.siteConfigs))
	}

	logger.Info(ctx, fmt.Sprintf("Using default configuration: %s", defaultConfig.Name))

	var url string
	var browser *rod.Browser
	var proxyUsername, proxyPassword string // 代理认证信息
	localUserDataDir := ""

	// 检查是否配置了远程 Chrome URL
	if m.config.Browser != nil && m.config.Browser.ControlURL != "" {
		// 使用远程 Chrome
		controlURL := m.config.Browser.ControlURL
		logger.Info(ctx, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		logger.Info(ctx, "Using remote Chrome browser")
		logger.Info(ctx, fmt.Sprintf("Control URL: %s", controlURL))
		logger.Info(ctx, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

		// 解析 WebSocket URL
		wsURL, err := resolveWebSocketURL(controlURL)
		if err != nil {
			return fmt.Errorf("failed to resolve WebSocket URL from %s: %w", controlURL, err)
		}
		logger.Info(ctx, fmt.Sprintf("Resolved WebSocket URL: %s", wsURL))
		url = wsURL

		// 连接到远程浏览器
		browser = rod.New().ControlURL(url)
	} else {
		// 启动本地浏览器
		logger.Info(ctx, "Starting local Chrome browser...")

		// 创建启动器
		// 根据配置决定是否使用 headless 模式
		headless := false // 默认不使用 headless
		if defaultConfig.Headless != nil {
			headless = *defaultConfig.Headless
		}
		logger.Info(ctx, fmt.Sprintf("Headless mode: %v", headless))

		l := launcher.New().
			Headless(headless).
			Devtools(false).
			Leakless(false)
		if !headless && loadGlobalCookieStore {
			l = l.Delete("no-startup-window").StartURL("about:blank")
		}

		// NoSandbox 配置
		if defaultConfig.NoSandbox != nil && *defaultConfig.NoSandbox {
			l = l.NoSandbox(true)
			logger.Info(ctx, "NoSandbox enabled by configuration")
		}

		// 代理配置
		if defaultConfig.Proxy != "" {
			// 解析代理 URL，提取认证信息
			proxyAddr, username, password, err := parseProxyURL(defaultConfig.Proxy)
			if err != nil {
				logger.Warn(ctx, "Failed to parse proxy URL: %v", err)
			} else {
				// 设置代理地址（不包含用户名密码）
				l = l.Proxy(proxyAddr)
				proxyUsername = username
				proxyPassword = password

				if username != "" {
					logger.Info(ctx, fmt.Sprintf("Using proxy: %s (with authentication)", proxyAddr))
				} else {
					logger.Info(ctx, fmt.Sprintf("Using proxy: %s", proxyAddr))
				}
			}
		}

		// 打印启动参数
		logger.Info(ctx, fmt.Sprintf("Number of launch arguments: %d", len(defaultConfig.LaunchArgs)))
		for i, arg := range defaultConfig.LaunchArgs {
			logger.Info(ctx, fmt.Sprintf("  [%d] %s", i+1, arg))
		}

		// 应用默认配置的启动参数
		for _, arg := range defaultConfig.LaunchArgs {
			l = applyChromiumLaunchArg(l, arg)
		}

		// 设置浏览器路径
		binPath := ""
		if m.config.Browser != nil && m.config.Browser.BinPath != "" {
			binPath = m.config.Browser.BinPath
		} else {
			binPath = findLocalChromiumBinary()
		}
		if binPath != "" {
			l = l.Bin(binPath)
			logger.Info(ctx, fmt.Sprintf("Using browser path: %s", binPath))
		} else {
			logger.Warn(ctx, "No local Chrome/Edge path found, launcher may attempt to download Chromium")
		}

		// 设置用户数据目录 - 关键：这会保存登录状态
		if m.config.Browser != nil && m.config.Browser.UserDataDir != "" {
			userDataDir := m.config.Browser.UserDataDir
			if shouldUseEdgeDefaultUserDataDir(binPath, userDataDir) {
				userDataDir = "./edge_user_data"
				logger.Info(ctx, "Using Edge-specific user data directory: %s", userDataDir)
			}

			// 确保目录存在
			if err := os.MkdirAll(userDataDir, 0o755); err != nil {
				logger.Warn(ctx, fmt.Sprintf("Failed to create user data directory: %v", err))
				logger.Warn(ctx, "Will not use user data directory")
			} else {
				// 检查目录是否可写
				testFile := userDataDir + "/.test"
				if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
					logger.Warn(ctx, fmt.Sprintf("User data directory is not writable: %v", err))
					logger.Warn(ctx, "Will not use user data directory, may cause startup failure")
				} else {
					os.Remove(testFile)

					// 清理可能存在的锁文件（在启动前）
					logger.Info(ctx, "Checking and cleaning up lock files before launch...")
					if terminated, err := m.killTrackedChromeProfileOwner(ctx, userDataDir); err != nil {
						logger.Warn(ctx, "Failed to terminate tracked Chrome profile owner: %v", err)
					} else if terminated {
						logger.Info(ctx, "Terminated the tracked Chrome profile owner before launch")
					}
					// 首先尝试杀死可能存在的孤儿 Chrome 进程（重启后残留的进程）
					if err := m.killOrphanedChromeForDir(ctx, userDataDir); err != nil {
						logger.Warn(ctx, "Failed to kill orphaned Chrome processes: %v", err)
					}
					// 然后清理锁文件
					if err := m.cleanupSingletonLock(ctx, userDataDir); err != nil {
						logger.Warn(ctx, "Failed to cleanup singleton lock: %v", err)
					}

					// 检查锁文件是否仍然存在（说明有进程正在持有它们）
					if m.lockFilesStillExist(userDataDir) {
						logger.Warn(ctx, "Lock files still exist after cleanup, killing orphaned Chrome processes...")
						m.killChromeByUserDataDir(ctx, userDataDir)
						time.Sleep(1 * time.Second)
						m.cleanupSingletonLock(ctx, userDataDir)
					}

					l = l.UserDataDir(userDataDir)
					localUserDataDir = userDataDir
					logger.Info(ctx, fmt.Sprintf("✓ Using user data directory: %s", userDataDir))
				}
			}
		} else {
			logger.Warn(ctx, "User data directory not configured, login state will not be saved")
		}

		logger.Info(ctx, "Starting browser process...")
		// 启动浏览器（失败后自动重试一次）
		var err error
		url, err = l.Launch()
		if err != nil {
			if isBrowserBinaryNotFound(err) {
				logger.Error(ctx, "Failed to find local Chrome/Edge browser: %v", err)
				return fmt.Errorf("failed to find local Chrome/Edge browser binary; configure browser.bin_path: %w", err)
			}
			logger.Error(ctx, "Failed to start browser: %v, attempting to kill orphaned processes and retry...", err)

			// 杀死残留的 Chrome 进程并清理锁文件
			if m.config.Browser != nil && m.config.Browser.UserDataDir != "" {
				m.killChromeByUserDataDir(ctx, m.config.Browser.UserDataDir)
			} else {
				m.KillOrphanedChromeProcesses(ctx)
			}
			time.Sleep(2 * time.Second)
			if m.config.Browser != nil && m.config.Browser.UserDataDir != "" {
				m.cleanupSingletonLock(ctx, m.config.Browser.UserDataDir)
			}

			// 重试启动
			logger.Info(ctx, "Retrying browser launch...")
			l = cloneLauncherForRetry(l)
			url, err = l.Launch()
			if err != nil {
				logger.Error(ctx, "Browser launch failed on retry: %v", err)
				return fmt.Errorf("failed to start browser (tried killing orphaned processes): %w", err)
			}
			logger.Info(ctx, "✓ Browser launched successfully on retry")
		}
		if err := persistLaunchedBrowserProfileOwner(ctx, l, localUserDataDir, url); err != nil {
			return err
		}

		logger.Info(ctx, fmt.Sprintf("Browser control URL: %s", url))

		// 连接到浏览器
		browser = rod.New().ControlURL(url)

		// 保存 launcher 实例用于后续清理
		m.launcher = l
	}
	if err := browser.Connect(); err != nil {
		return fmt.Errorf("failed to connect browser: %w", err)
	}

	// 如果代理需要认证，启动认证处理
	if proxyUsername != "" && proxyPassword != "" {
		logger.Info(ctx, "Setting up proxy authentication handler...")
		go browser.HandleAuth(proxyUsername, proxyPassword)()
	}

	// 获取并显示浏览器版本信息
	version, err := browser.Version()
	if err != nil {
		logger.Warn(ctx, "Failed to get browser version: %v", err)
	} else {
		logger.Info(ctx, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		logger.Info(ctx, "Browser version information:")
		logger.Info(ctx, "  Product: %s", version.Product)
		logger.Info(ctx, "  User-Agent: %s", version.UserAgent)
		logger.Info(ctx, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	}

	// 尝试从数据库加载保存的 Cookie
	if loadGlobalCookieStore && m.db != nil {
		cookieStore, err := m.db.GetCookies("browser")
		if err == nil && cookieStore != nil && len(cookieStore.Cookies) > 0 {
			// 将 NetworkCookie 转换为 NetworkCookieParam
			cookieParams := make([]*proto.NetworkCookieParam, 0, len(cookieStore.Cookies))
			for _, cookie := range cookieStore.Cookies {
				cookieParams = append(cookieParams, &proto.NetworkCookieParam{
					Name:     cookie.Name,
					Value:    cookie.Value,
					Domain:   cookie.Domain,
					Path:     cookie.Path,
					Secure:   cookie.Secure,
					HTTPOnly: cookie.HTTPOnly,
					SameSite: cookie.SameSite,
					Expires:  cookie.Expires,
				})
			}

			// 设置 Cookie 到浏览器
			if err := browser.SetCookies(cookieParams); err != nil {
				logger.Warn(ctx, "Failed to set Cookie: %v", err)
			} else {
				logger.Info(ctx, "Loaded %d saved Cookies", len(cookieParams))
			}
		} else {
			logger.Info(ctx, "No saved Cookies found")
		}
	} else if !loadGlobalCookieStore {
		logger.Info(ctx, "Skipped global browser Cookie Store for isolated project recording")
	}

	downloadRoot := "."
	if m.config != nil && strings.TrimSpace(m.config.AssetsDir) != "" {
		downloadRoot = m.config.AssetsDir
	}
	downloadPath := filepath.Join(downloadRoot, "downloads")
	absDownloadPath, err := filepath.Abs(downloadPath)
	if err == nil {
		downloadPath = absDownloadPath
	}
	// 判断文件夹是否存在，不存在则创建
	if _, err := os.Stat(downloadPath); os.IsNotExist(err) {
		err := os.MkdirAll(downloadPath, 0o755)
		if err != nil {
			logger.Warn(ctx, "Failed to create download directory: %v", err)
		} else {
			logger.Info(ctx, "Download directory created: %s", downloadPath)
		}
	}

	downloadBehavior := &proto.BrowserSetDownloadBehavior{
		Behavior:      proto.BrowserSetDownloadBehaviorBehaviorAllow,
		DownloadPath:  downloadPath, // ⚠ 必须是已存在目录
		EventsEnabled: true,
	}
	err = downloadBehavior.Call(browser)
	if err != nil {
		logger.Warn(ctx, "Failed to set download behavior: %v", err)
	} else {
		logger.Info(ctx, "Download behavior set: %s, path: %s", downloadBehavior.Behavior, downloadBehavior.DownloadPath)
	}

	// 保存下载路径到 Manager 和 Recorder
	m.downloadPath = downloadPath
	for _, recorder := range m.recorders {
		if recorder != nil {
			recorder.SetDownloadPath(downloadPath)
		}
	}
	if m.recorder != nil {
		m.recorder.SetDownloadPath(downloadPath)
	}

	// 授予剪贴板权限，避免粘贴时弹出权限请求
	grantPermissions := &proto.BrowserGrantPermissions{
		Permissions: []proto.BrowserPermissionType{
			proto.BrowserPermissionTypeClipboardReadWrite,
			proto.BrowserPermissionTypeClipboardSanitizedWrite,
		},
	}
	err = grantPermissions.Call(browser)
	if err != nil {
		logger.Warn(ctx, "Failed to grant clipboard permissions: %v", err)
	} else {
		logger.Info(ctx, "✓ Clipboard permissions granted (read/write)")
	}

	if err := checkBrowserConnection(browser); err != nil {
		if m.launcher != nil {
			m.launcher.Kill()
			m.launcher = nil
		}
		return fmt.Errorf("browser connection closed after launch: %w", err)
	}

	m.browser = browser
	m.isRunning = true
	m.startTime = time.Now()

	logger.Info(ctx, "Browser started successfully")
	return nil
}

// Stop 停止浏览器
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.isRunning {
		return fmt.Errorf("browser is not running")
	}

	ctx := context.Background()

	// The legacy fields mirror the current instance. Prefer the instance runtime
	// whenever it exists so Stop never reads or kills another instance's profile.
	var currentRuntime *BrowserInstanceRuntime
	if m.currentInstanceID != "" {
		currentRuntime = m.instances[m.currentInstanceID]
	}
	isRemoteMode := m.config != nil && m.config.Browser != nil && m.config.Browser.ControlURL != ""
	if currentRuntime != nil && currentRuntime.instance != nil {
		isRemoteMode = currentRuntime.instance.Type == "remote"
	}
	localUserDataDir := m.currentBrowserUserDataDirLocked()

	if isRemoteMode {
		logger.Info(ctx, "Disconnecting from remote browser...")
	} else {
		logger.Info(ctx, "Closing browser...")
	}

	// 1. 先关闭所有页面，让浏览器有机会保存数据
	if m.browser != nil {
		if !isRemoteMode {
			// 仅在本地模式下关闭页面
			pages, err := m.browser.Pages()
			if err == nil {
				for _, page := range pages {
					_ = page.Close()
				}
				logger.Info(ctx, fmt.Sprintf("Closed %d pages", len(pages)))
			}

			// 2. 等待一下，让浏览器保存数据
			time.Sleep(1 * time.Second)
		}

		// 3. 优雅关闭浏览器连接
		if err := m.browser.Close(); err != nil {
			logger.Warn(ctx, fmt.Sprintf("Error when closing browser connection: %v", err))
		}
	}

	// 4. 仅在本地模式下关闭浏览器进程
	if !isRemoteMode {
		// 再等待一下，确保数据完全写入磁盘
		time.Sleep(1 * time.Second)

		// 5. ⚠️ 重要：不调用 launcher.Cleanup()，因为它会删除用户数据目录！
		// 浏览器进程会在连接关闭后自动退出
		// 如果需要强制杀死进程，可以调用 launcher.Kill() 而不是 Cleanup()
		currentLauncher := m.launcher
		if currentRuntime != nil && currentRuntime.launcher != nil {
			currentLauncher = currentRuntime.launcher
		}
		if currentLauncher != nil {
			// Always terminate the launcher associated with the current instance.
			// Recovery markers are only for a later process restart, never for Stop.
			currentLauncher.Kill()
			logger.Info(ctx, "Browser process terminated")
		}

		// 6. 清理本地浏览器的锁文件
		if localUserDataDir != "" {
			// 等待浏览器完全退出
			time.Sleep(500 * time.Millisecond)

			if err := m.cleanupSingletonLock(ctx, localUserDataDir); err != nil {
				logger.Warn(ctx, "Failed to cleanup singleton lock after stop: %v", err)
			} else {
				logger.Info(ctx, "✓ Cleaned up singleton lock files")
			}
		}
	}

	if m.currentInstanceID != "" {
		m.finalizeStoppedCurrentInstanceRuntimeLocked(ctx, m.currentInstanceID)
	} else {
		m.browser = nil
		m.launcher = nil
		m.isRunning = false
		m.activePage = nil
		m.startTime = time.Time{}
	}

	if isRemoteMode {
		logger.Info(ctx, "Disconnected from remote browser successfully")
	} else {
		logger.Info(ctx, "Browser fully closed, user data saved")
	}
	return nil
}

// currentBrowserUserDataDirLocked returns the profile of the browser represented
// by the legacy current fields. The caller must hold m.mu.
func (m *Manager) currentBrowserUserDataDirLocked() string {
	if m.currentInstanceID != "" {
		if currentRuntime := m.instances[m.currentInstanceID]; currentRuntime != nil && currentRuntime.instance != nil && currentRuntime.instance.Type != "remote" {
			if userDataDir := strings.TrimSpace(currentRuntime.instance.UserDataDir); userDataDir != "" {
				return userDataDir
			}
		}
	}
	if m.config != nil && m.config.Browser != nil {
		return strings.TrimSpace(m.config.Browser.UserDataDir)
	}
	return ""
}

// IsRunning 检查浏览器是否运行
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查是否有当前实例ID
	if m.currentInstanceID == "" {
		return m.isRunning // 向后兼容：如果没有实例ID，使用旧逻辑
	}

	// 直接检查实例，避免调用 IsInstanceRunning 导致死锁
	runtime, exists := m.instances[m.currentInstanceID]
	return exists && runtime != nil && runtime.browser != nil
}

func (m *Manager) IsInstanceRunning(instanceID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if instanceID == "" && m.currentInstanceID == "" {
		return m.isRunning // 向后兼容：如果没有实例ID，使用旧逻辑
	}
	if instanceID == "" {
		instanceID = m.currentInstanceID
	}

	runtime, exists := m.instances[instanceID]
	return exists && runtime != nil && runtime.browser != nil
}

// SetInstanceHeadless sets the headless mode for an instance before it starts.
func (m *Manager) SetInstanceHeadless(instanceID string, headless bool) {
	if instanceID == "" {
		instanceID = "default"
	}
	instance, err := m.db.GetBrowserInstance(instanceID)
	if err != nil || instance == nil {
		instance = &models.BrowserInstance{
			ID:   instanceID,
			Name: instanceID,
			Type: "local",
		}
	}
	instance.Headless = &headless
	_ = m.db.SaveBrowserInstance(instance)
}

// SetInstanceHeadlessTemp temporarily overrides headless mode for one run.
// Returns a restore function that reverts to the original value.
func (m *Manager) SetInstanceHeadlessTemp(instanceID string, headless bool) func() {
	if instanceID == "" {
		instanceID = "default"
	}
	instance, err := m.db.GetBrowserInstance(instanceID)
	if err != nil || instance == nil {
		instance = &models.BrowserInstance{
			ID:   instanceID,
			Name: instanceID,
			Type: "local",
		}
	}

	origHeadless := instance.Headless

	instance.Headless = &headless
	_ = m.db.SaveBrowserInstance(instance)

	return func() {
		inst, err := m.db.GetBrowserInstance(instanceID)
		if err != nil || inst == nil {
			return
		}
		inst.Headless = origHeadless
		_ = m.db.SaveBrowserInstance(inst)
	}
}

// GetActivePage 获取当前活动页面
func (m *Manager) GetActivePage() *rod.Page {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activePage
}

// GetActivePageForInstance returns the page selected for one browser instance
// without treating the Manager-wide compatibility pointer as runtime truth.
func (m *Manager) GetActivePageForInstance(instanceID string) *rod.Page {
	m.mu.Lock()
	defer m.mu.Unlock()
	instanceID = m.recordingInstanceIDLocked(instanceID)
	if runtime := m.instances[instanceID]; runtime != nil && runtime.activePage != nil {
		return runtime.activePage
	}
	return m.activePage
}

// SetActivePage 设置当前活动页面（用于脚本回放等场景）
func (m *Manager) SetActivePage(page *rod.Page) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activePage = page
}

// OpenIsolatedPage opens a page in an incognito browser context for project-scoped recording.
func (m *Manager) OpenIsolatedPage(ctx context.Context, targetURL string, language string, instanceID string) error {
	m.mu.Lock()
	if language != "" {
		m.currentLanguage = language
	}
	browser, _, instance, err := m.getInstanceBrowser(instanceID)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if instance != nil {
		instanceID = instance.ID
	}
	m.mu.Unlock()

	incognito, err := browser.Incognito()
	if err != nil {
		return fmt.Errorf("failed to create isolated browser context: %w", err)
	}
	page, err := incognito.Page(proto.TargetCreateTarget{})
	if err != nil {
		return fmt.Errorf("failed to create isolated page: %w", err)
	}
	closePage := true
	defer func() {
		if closePage {
			_ = page.Close()
		}
	}()
	m.setPageWindow(page)

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := page.Context(ctx).Timeout(60 * time.Second).Navigate(targetURL); err != nil {
		return fmt.Errorf("failed to navigate isolated page: %w", err)
	}
	if err := page.Context(ctx).Timeout(60 * time.Second).WaitLoad(); err != nil {
		return fmt.Errorf("failed to wait for isolated page load: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.setInstanceActivePage(instanceID, page); err != nil {
		return err
	}
	m.activePage = page
	closePage = false
	return nil
}

// CloseIsolatedRecordingPage removes a page created for a fenced Start when
// it never became an active recorder. It only clears the exact page pointer,
// so a newer driver cannot lose its own active target to an old driver's
// deferred cleanup.
func (m *Manager) CloseIsolatedRecordingPage(_ context.Context, instanceID string, page *rod.Page) {
	if page == nil {
		return
	}
	_ = page.Close()
	m.mu.Lock()
	defer m.mu.Unlock()
	instanceID = m.recordingInstanceIDLocked(instanceID)
	if runtime := m.instances[instanceID]; runtime != nil && runtime.activePage == page {
		runtime.activePage = nil
	}
	if m.activePage == page {
		m.activePage = nil
	}
}

// CloseActivePage 关闭当前活动页面
func (m *Manager) CloseActivePage(ctx context.Context, page *rod.Page) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.isRunning || m.browser == nil {
		return fmt.Errorf("browser is not running")
	}

	if page == nil {
		logger.Warn(ctx, "No active page to close")
		return nil
	}

	logger.Info(ctx, "Closing active page...")
	if err := page.Close(); err != nil {
		return fmt.Errorf("failed to close active page: %w", err)
	}

	logger.Info(ctx, "Active page closed")
	return nil
}

// Status 获取浏览器状态
func (m *Manager) Status() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	status := map[string]interface{}{
		"is_running": m.isRunning,
	}

	if m.isRunning {
		status["start_time"] = m.startTime.Format(time.RFC3339)
		status["uptime"] = time.Since(m.startTime).String()

		// 获取浏览器页面数量
		if m.browser != nil {
			pages, err := m.browser.Pages()
			if err == nil {
				status["pages_count"] = len(pages)
			}
		}
	}

	return status
}

func (m *Manager) setPageWindow(page *rod.Page) {
	ctx := context.Background()

	const (
		defaultWindowWidth    = 1400
		defaultWindowHeight   = 900
		defaultViewportWidth  = 1280
		defaultViewportHeight = 800
		minScreenWidth        = 1024
		minScreenHeight       = 768
	)

	var windowWidth, windowHeight int
	var viewportWidth, viewportHeight int

	screenInfo, err := page.Eval(`() => ({
		width: window.screen.availWidth,
		height: window.screen.availHeight
	})`)

	useDefault := true

	if err == nil && screenInfo != nil {
		if info, ok := screenInfo.Value.Val().(map[string]interface{}); ok {
			screenWidth := int(info["width"].(float64))
			screenHeight := int(info["height"].(float64))

			logger.Info(ctx, "Detected screen size: %dx%d", screenWidth, screenHeight)

			if screenWidth >= minScreenWidth && screenHeight >= minScreenHeight {
				windowWidth = int(float64(screenWidth) * 0.9)
				windowHeight = int(float64(screenHeight) * 0.9)
				viewportWidth = windowWidth - 120
				viewportHeight = windowHeight - 100
				useDefault = false
			} else {
				logger.Warn(ctx, "Screen size %dx%d is too small (likely headless/virtual), using default viewport", screenWidth, screenHeight)
			}
		}
	} else {
		logger.Warn(ctx, "Failed to get screen size: %v", err)
	}

	if useDefault {
		windowWidth = defaultWindowWidth
		windowHeight = defaultWindowHeight
		viewportWidth = defaultViewportWidth
		viewportHeight = defaultViewportHeight
	}

	logger.Info(ctx, "Calculated window size: %dx%d", windowWidth, windowHeight)
	logger.Info(ctx, "Calculated viewport size: %dx%d", viewportWidth, viewportHeight)

	page.MustSetWindow(0, 0, windowWidth, windowHeight)

	page.MustSetViewport(
		viewportWidth,
		viewportHeight,
		1,
		false,
	)
}

// OpenPage 打开一个新页面
// instanceID: 指定实例ID，空字符串表示使用当前实例
func (m *Manager) OpenPage(url string, language string, instanceID string, norecord ...bool) (err error) {
	// 捕获 panic 并转换为错误
	defer func() {
		if r := recover(); r != nil {
			ctx := context.Background()
			logger.Error(ctx, "Panic in OpenPage: %v", r)
			if instanceID != "" {
				m.mu.Lock()
				m.clearInstanceRuntimeLocked(ctx, instanceID)
				m.mu.Unlock()
			}
			err = fmt.Errorf("failed to open page: browser connection may be closed (panic: %v)", r)
		}
	}()

	// 自动补全 URL schema
	url = normalizeURLSchema(url)

	var noRecord bool
	if len(norecord) > 0 {
		noRecord = norecord[0]
	}

	// 获取指定实例的浏览器（需要锁保护）
	m.mu.Lock()
	browser, _, instance, err := m.getInstanceBrowser(instanceID)
	if err != nil {
		m.mu.Unlock()
		return err
	}

	// 使用实际的实例ID（可能从空字符串转换为 default）
	if instance != nil {
		instanceID = instance.ID
	} else if instanceID == "" {
		// 向后兼容：如果没有 instance 对象，使用 currentInstanceID
		instanceID = m.currentInstanceID
	}

	// 保存当前语言设置,用于后续注入脚本时的文本替换
	if language == "" {
		language = "zh-CN" // 默认简体中文
	}
	m.currentLanguage = language

	// 根据URL匹配配置
	config := m.getConfigForURL(url)
	m.mu.Unlock() // 释放锁，准备执行耗时操作

	// 检查浏览器连接是否仍然有效
	ctx := context.Background()
	if err := checkBrowserConnection(browser); err != nil {
		logger.Warn(ctx, "Browser connection check failed, restarting instance %s: %v", instanceID, err)

		m.mu.Lock()
		m.clearInstanceRuntimeLocked(ctx, instanceID)
		if restartErr := m.startInstanceInternal(ctx, instanceID); restartErr != nil {
			m.mu.Unlock()
			return fmt.Errorf("browser connection is closed and restart failed: %w", restartErr)
		}
		browser, _, instance, err = m.getInstanceBrowser(instanceID)
		if err != nil {
			m.mu.Unlock()
			return err
		}
		if instance != nil {
			instanceID = instance.ID
		}
		config = m.getConfigForURL(url)
		m.mu.Unlock()

		if err := checkBrowserConnection(browser); err != nil {
			logger.Error(ctx, "Browser connection check failed after restart: %v", err)
			return fmt.Errorf("browser connection is closed after restart: %w", err)
		}
	}

	logger.Info(ctx, fmt.Sprintf("URL: %s, using configuration: %s, language: %s", url, config.Name, language))

	var page *rod.Page

	// 根据配置决定是否使用 stealth
	useStealth := true // 默认使用stealth
	if config.UseStealth != nil {
		useStealth = *config.UseStealth
	}

	if useStealth {
		page, err = stealth.Page(browser)
		if err != nil {
			logger.Warn(ctx, "Failed to create stealth page, restarting instance %s and retrying stealth mode: %v", instanceID, err)
			m.mu.Lock()
			m.clearInstanceRuntimeLocked(ctx, instanceID)
			if restartErr := m.startInstanceInternal(ctx, instanceID); restartErr != nil {
				m.mu.Unlock()
				return fmt.Errorf("failed to create stealth page and restart browser: %w", restartErr)
			}
			browser, _, instance, err = m.getInstanceBrowser(instanceID)
			if err != nil {
				m.mu.Unlock()
				return err
			}
			if instance != nil {
				instanceID = instance.ID
			}
			m.mu.Unlock()

			page, err = stealth.Page(browser)
			if err != nil {
				m.mu.Lock()
				m.clearInstanceRuntimeLocked(ctx, instanceID)
				m.mu.Unlock()
				return fmt.Errorf("failed to create stealth page after browser restart: %w", err)
			}
			logger.Info(ctx, "Using Stealth mode after browser restart")
		} else {
			logger.Info(ctx, "Using Stealth mode")
		}
	} else {
		page, err = browser.Page(proto.TargetCreateTarget{})
		if err != nil {
			m.mu.Lock()
			m.clearInstanceRuntimeLocked(ctx, instanceID)
			m.mu.Unlock()
			return fmt.Errorf("failed to create page: %w", err)
		}
		logger.Info(ctx, "Not using Stealth mode")
	}

	m.setPageWindow(page)

	// 设置 User Agent
	userAgent := config.UserAgent
	if userAgent == "" {
		userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"
	}
	if err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent: userAgent,
	}); err != nil {
		m.mu.Lock()
		m.clearInstanceRuntimeLocked(ctx, instanceID)
		m.mu.Unlock()
		return fmt.Errorf("failed to set user agent: %w", err)
	}

	// 导航到目标 URL（设置60秒超时）- 这是耗时操作，不持有锁
	if err := page.Timeout(60 * time.Second).Navigate(url); err != nil {
		return fmt.Errorf("failed to navigate to page: %w", err)
	}

	if err := page.Timeout(60 * time.Second).WaitLoad(); err != nil {
		logger.Warn(ctx, "Failed to wait for page load: %v", err)
	}

	// 为当前页面授予剪贴板权限
	pageInfo, _ := page.Info()
	if pageInfo != nil {
		grantPagePermissions := &proto.BrowserGrantPermissions{
			Origin: pageInfo.URL,
			Permissions: []proto.BrowserPermissionType{
				proto.BrowserPermissionTypeClipboardReadWrite,
				proto.BrowserPermissionTypeClipboardSanitizedWrite,
			},
		}
		if err := grantPagePermissions.Call(browser); err != nil {
			logger.Warn(ctx, "Failed to grant clipboard permissions for page: %v", err)
		} else {
			logger.Info(ctx, "✓ Clipboard permissions granted for page: %s", pageInfo.URL)
		}
	}

	if !noRecord {
		// 注入浮动录制按钮
		time.Sleep(500 * time.Millisecond) // 等待页面稳定

		// 获取当前语言（需要锁保护）
		m.mu.Lock()
		currentLang := m.currentLanguage
		m.mu.Unlock()

		// 替换浮动按钮脚本中的多语言占位符
		localizedFloatButtonScript := ReplaceI18nPlaceholders(floatButtonScript, currentLang, FloatButtonI18n)
		_, err := page.Eval(`() => { ` + localizedFloatButtonScript + ` return true; }`)
		if err != nil {
			logger.Warn(ctx, "Failed to inject float button script: %v", err)
		} else {
			logger.Info(ctx, "✓ Float recording button injected successfully (language: %s)", currentLang)

			// 设置 API 端口信息
			if m.config.Server != nil && m.config.Server.Port != "" {
				apiPort := m.config.Server.Port
				setPortScript := fmt.Sprintf(`() => { window.__browserwingAPIPort__ = "%s"; }`, apiPort)
				if _, err := page.Eval(setPortScript); err != nil {
					logger.Warn(ctx, "Failed to set API port: %v", err)
				}
			}
		}
		// 启动轮询检查页面内的录制请求
		m.mu.Lock()
		recorder := m.recorderForInstanceLocked(instanceID)
		m.mu.Unlock()
		go m.checkInPageRecordingRequests(ctx, page, recorder, instanceID, RecordingStorageScope{})
	}

	// 保存当前活动页面到指定实例（需要锁保护）
	m.mu.Lock()
	if err := m.setInstanceActivePage(instanceID, page); err != nil {
		logger.Warn(ctx, "Failed to set active page: %v", err)
	}
	m.mu.Unlock()

	logger.Info(ctx, fmt.Sprintf("Page opened: %s", url))
	return nil
}

// getConfigForURL 根据URL获取匹配的配置
func (m *Manager) getConfigForURL(url string) *models.BrowserConfig {
	ctx := context.Background()
	logger.Info(ctx, fmt.Sprintf("Starting URL matching: %s, total %d site configurations", url, len(m.siteConfigs)))

	// 遍历所有网站特定配置，找到第一个匹配的
	for _, config := range m.siteConfigs {
		if config.URLPattern != "" {
			logger.Info(ctx, fmt.Sprintf("Trying to match pattern: %s (configuration: %s)", config.URLPattern, config.Name))
			// 使用正则表达式匹配
			matched, err := regexp.MatchString(config.URLPattern, url)
			if err != nil {
				logger.Info(ctx, fmt.Sprintf("Regular expression error: %v", err))
			} else if matched {
				logger.Info(ctx, fmt.Sprintf("✓ URL %s matched pattern %s (configuration: %s)", url, config.URLPattern, config.Name))
				return config
			} else {
				logger.Info(ctx, "✗ Not matched")
			}
		}
	}

	// 没有匹配的，返回默认配置
	logger.Info(ctx, "No matching site configuration found, using default configuration")

	// 如果默认配置未初始化，尝试加载或创建一个
	if m.defaultBrowserConfig == nil {
		logger.Info(ctx, "Default configuration not initialized, loading from database")
		defaultConfig, err := m.db.GetDefaultBrowserConfig()
		if err != nil {
			logger.Warn(ctx, "Failed to load default configuration, using system defaults")
			defaultConfig = m.getDefaultBrowserConfig()
		}
		m.defaultBrowserConfig = defaultConfig

		// 同时加载网站特定配置
		allConfigs, err := m.db.ListBrowserConfigs()
		if err != nil {
			logger.Warn(ctx, "Failed to load site configurations: %v", err)
			m.siteConfigs = []*models.BrowserConfig{}
		} else {
			m.siteConfigs = []*models.BrowserConfig{}
			for i := range allConfigs {
				if allConfigs[i].URLPattern != "" && !allConfigs[i].IsDefault {
					m.siteConfigs = append(m.siteConfigs, &allConfigs[i])
				}
			}
			logger.Info(ctx, "Loaded %d site-specific configurations", len(m.siteConfigs))
		}
	}

	return m.defaultBrowserConfig
}

// GetCurrentPageCookies 获取当前活动页面的所有 Cookie
func (m *Manager) GetCurrentPageCookies() (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.isRunning || m.browser == nil {
		return nil, fmt.Errorf("browser is not running")
	}

	// 获取浏览器的所有 Cookie
	cookies, err := m.browser.GetCookies()
	if err != nil {
		return nil, fmt.Errorf("failed to get cookies: %w", err)
	}

	return cookies, nil
}

// StartRecording 开始录制操作
// instanceID: 指定实例ID，空字符串表示使用当前实例
func (m *Manager) StartRecording(ctx context.Context, instanceID string) error {
	return m.startRecording(ctx, instanceID, nil)
}

func (m *Manager) StartRecordingWithStorageScope(ctx context.Context, instanceID string, scope RecordingStorageScope) error {
	return m.startRecordingWithReservation(ctx, instanceID, &scope, "", 0)
}

// AcquireRecordingStartTarget grants the sole local driver context for a
// pending Start. A higher DB fencing generation never starts beside a slow
// predecessor: it cancels the predecessor and waits for its done signal to
// remove the reservation before a later retry may acquire a new target.
//
// The returned running value means an already-created recorder was rebound to
// the caller's current database fence and must be reused rather than started
// again. A non-running successful result owns driverCtx and may run browser
// side effects exactly once.
func (m *Manager) AcquireRecordingStartTarget(parent context.Context, scope RecordingStorageScope, token string, generation uint64) (driverCtx context.Context, running bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if scope.isZero() || strings.TrimSpace(token) == "" || generation == 0 {
		return nil, false, fmt.Errorf("recording start reservation identity is required")
	}
	registry := m.recordingRuntimeRegistryLocked()
	existing := registry.startBySession[scope.RecordingSessionID]
	if existing == nil {
		ctx, cancel := context.WithCancel(parent)
		registry.startBySession[scope.RecordingSessionID] = &recordingStartReservation{
			scope: scope, token: token, generation: generation, state: "reserved", ctx: ctx, cancel: cancel,
			done: make(chan struct{}), heartbeat: time.Now().UTC(),
		}
		return ctx, false, nil
	}
	if !existing.scope.matches(scope) {
		return nil, false, errRecordingStartReservationInProgress
	}
	if existing.token == token && existing.generation == generation {
		if existing.state == "running" {
			return nil, true, nil
		}
		return nil, false, errRecordingStartReservationInProgress
	}
	if generation <= existing.generation {
		return nil, false, errRecordingStartReservationInProgress
	}
	if existing.state == "running" {
		// A recorder is already running for the same persisted target. Rebind
		// the local fence so only the newer DB claimant may complete Start;
		// no page navigation or Recorder.Start call is repeated.
		existing.token = token
		existing.generation = generation
		existing.heartbeat = time.Now().UTC()
		return nil, true, nil
	}
	if existing.state != "cancelling" {
		existing.state = "cancelling"
		if existing.cancel != nil {
			existing.cancel()
		}
	}
	return nil, false, errRecordingStartReservationInProgress
}

// ReserveRecordingStartTarget remains for callers that only need the legacy
// running/in-progress shape. New runtime drivers must use
// AcquireRecordingStartTarget so their browser work receives the cancellation
// context and cannot race a fenced predecessor.
func (m *Manager) ReserveRecordingStartTarget(scope RecordingStorageScope, token string, generation uint64) (running bool, err error) {
	_, running, err = m.AcquireRecordingStartTarget(context.Background(), scope, token, generation)
	return running, err
}

// HeartbeatRecordingStartTarget keeps the Manager half of a Start fence alive
// while navigation or auth restoration is in progress. A fenced or cancelled
// driver receives an in-progress error and must stop using its context.
func (m *Manager) HeartbeatRecordingStartTarget(scope RecordingStorageScope, token string, generation uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	reservation := m.recordingRuntimeRegistryLocked().startBySession[scope.RecordingSessionID]
	if reservation == nil || !reservation.scope.matches(scope) || reservation.token != token || reservation.generation != generation || reservation.state == "cancelling" {
		return errRecordingStartReservationInProgress
	}
	reservation.heartbeat = time.Now().UTC()
	return nil
}

func (m *Manager) finishRecordingStartTargetLocked(scope RecordingStorageScope, token string, generation uint64, running bool) error {
	registry := m.recordingRuntimeRegistryLocked()
	reservation := registry.startBySession[scope.RecordingSessionID]
	if reservation == nil || !reservation.scope.matches(scope) || reservation.token != token || reservation.generation != generation {
		return errRecordingStartReservationInProgress
	}
	if reservation.state == "cancelling" {
		delete(registry.startBySession, scope.RecordingSessionID)
		if reservation.done != nil {
			reservation.doneOnce.Do(func() { close(reservation.done) })
		}
		return errRecordingStartReservationInProgress
	}
	if running {
		reservation.state = "running"
		reservation.heartbeat = time.Now().UTC()
	} else {
		delete(registry.startBySession, scope.RecordingSessionID)
	}
	if reservation.done != nil {
		reservation.doneOnce.Do(func() { close(reservation.done) })
	}
	return nil
}

func (m *Manager) ReleaseRecordingStartTarget(scope RecordingStorageScope, token string, generation uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reservation := m.recordingRuntimeRegistryLocked().startBySession[scope.RecordingSessionID]
	if reservation != nil && reservation.scope.matches(scope) && reservation.token == token && reservation.generation == generation {
		if reservation.cancel != nil {
			reservation.cancel()
		}
		delete(m.recordingRuntimeRegistryLocked().startBySession, scope.RecordingSessionID)
		if reservation.done != nil {
			reservation.doneOnce.Do(func() { close(reservation.done) })
		}
	}
}

func (m *Manager) StartRecordingWithStorageScopeReservation(ctx context.Context, instanceID string, scope RecordingStorageScope, token string, generation uint64) error {
	return m.startRecordingWithReservation(ctx, instanceID, &scope, token, generation)
}

func (m *Manager) startRecording(ctx context.Context, instanceID string, scope *RecordingStorageScope) error {
	return m.startRecordingWithReservation(ctx, instanceID, scope, "", 0)
}

func (m *Manager) startRecordingWithReservation(ctx context.Context, instanceID string, scope *RecordingStorageScope, token string, generation uint64) error {
	m.mu.Lock()
	currentLang := m.currentLanguage
	if currentLang == "" {
		currentLang = "zh-CN" // 默认简体中文
	}
	m.mu.Unlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if scope != nil && strings.TrimSpace(token) != "" {
		reservation := m.recordingRuntimeRegistryLocked().startBySession[scope.RecordingSessionID]
		if reservation == nil || !reservation.scope.matches(*scope) || reservation.token != token || reservation.generation != generation {
			return errRecordingStartReservationInProgress
		}
		if reservation.state == "running" {
			return nil
		}
	}

	// 获取指定实例的浏览器和活动页面
	instanceBrowser, activePage, _, err := m.getInstanceBrowser(instanceID)
	if err != nil {
		return err
	}

	if activePage == nil {
		return fmt.Errorf("please open a page first")
	}

	// 获取当前页面URL
	info, err := activePage.Info()
	if err != nil {
		if instanceID == "" {
			instanceID = m.currentInstanceID
		}
		if instanceID != "" {
			m.clearInstanceRuntimeLocked(ctx, instanceID)
		}
		return fmt.Errorf("failed to get page info: %w", err)
	}

	instanceID = m.recordingInstanceIDLocked(instanceID)
	recorder := m.recorderForInstanceLocked(instanceID)
	scopes := []RecordingStorageScope{}
	if scope != nil {
		scopes = append(scopes, *scope)
		if err := m.setProjectDownloadBehaviorLocked(instanceBrowser, true); err != nil {
			return err
		}
	}
	err = recorder.StartRecording(ctx, activePage, info.URL, currentLang, scopes...)
	if err != nil {
		if scope != nil {
			m.restoreProjectDownloadBehaviorLocked(ctx, instanceBrowser)
		}
		return err
	}
	if scope != nil {
		if instanceID == "" {
			instanceID = m.currentInstanceID
		}
		if strings.TrimSpace(token) != "" {
			if err := m.finishRecordingStartTargetLocked(*scope, token, generation, true); err != nil {
				// The driver was fenced while Recorder.Start completed. Undo the
				// local runtime state before reporting in-progress so an old
				// completion cannot leave a hidden active recorder behind.
				_, _, _ = recorder.StopRecordingWithStorageScope(context.Background(), *scope)
				m.restoreProjectDownloadBehaviorLocked(context.Background(), instanceBrowser)
				return err
			}
		}
		m.recordingRuntimeRegistryLocked().activeByInstance[instanceID] = *scope
		delete(m.recordingRuntimeRegistryLocked().finalBySession, scope.RecordingSessionID)
	}

	// StartRecording may be called without OpenPage, for example project-scoped
	// isolated recording. Bind in-page controls to the recording lifecycle, not
	// to the short-lived HTTP request that starts recording.
	recordingCtx := recorder.RecordingContext()
	if recordingCtx == nil {
		recordingCtx = context.WithoutCancel(ctx)
	}
	loopScope := RecordingStorageScope{}
	if scope != nil {
		loopScope = *scope
	}
	go m.checkInPageRecordingRequests(recordingCtx, activePage, recorder, instanceID, loopScope)

	// 启动录制后,显示录制UI面板
	_, _ = activePage.Eval(`() => {
		window.__isRecordingActive__ = true;
		if (typeof createRecorderUI === 'function') createRecorderUI();
		if (typeof createHighlightElement === 'function') createHighlightElement();
	}`)

	return nil
}

// StopRecording 停止录制
func (m *Manager) StopRecording(ctx context.Context) ([]models.ScriptAction, []models.DownloadedFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	recorder := m.recorderForInstanceLocked("")
	scope, projectScoped := recorder.CurrentStorageScope()
	actions, downloadedFiles, err := recorder.StopRecording(ctx)
	if projectScoped {
		m.restoreProjectDownloadBehaviorLocked(ctx)
		m.publishFinalRecordingReceiptLocked(scope, recorder.GetStartURL(), actions, downloadedFiles, recorder.LastSemanticDOMSnapshot(), recorder.SyncRevision(), "")
		m.publishAuthSnapshotReceiptLocked(scope, recorder.TakeLastStorageState(scope))
	}
	return actions, downloadedFiles, err
}

// StopRecordingWithStorageScope stops recording only when the active browser recorder
// belongs to the requested persisted recording session scope.
func (m *Manager) StopRecordingWithStorageScope(ctx context.Context, scope RecordingStorageScope) ([]models.ScriptAction, []models.DownloadedFile, error) {
	return m.stopRecordingWithStorageScope(ctx, scope, runtimeStopOperationID(scope))
}

// StopRecordingWithClaim returns a stopped runtime receipt only to its owning
// lifecycle operation. A different pending operation must not drive the same
// recorder a second time or consume the same final receipt.
func (m *Manager) StopRecordingWithClaim(ctx context.Context, scope RecordingStorageScope, operationID string) ([]models.ScriptAction, []models.DownloadedFile, error) {
	return m.stopRecordingWithStorageScope(ctx, scope, operationID)
}

func (m *Manager) stopRecordingWithStorageScope(ctx context.Context, scope RecordingStorageScope, operationID string) ([]models.ScriptAction, []models.DownloadedFile, error) {
	m.mu.Lock()
	m.cleanupRuntimeReceiptsLocked(time.Now().UTC())

	if actions, downloadedFiles, _, _, ok, err := m.claimFinalRecordingReceiptLocked(scope, operationID); err != nil {
		m.mu.Unlock()
		return nil, nil, err
	} else if ok {
		m.mu.Unlock()
		return actions, downloadedFiles, nil
	}
	registry := m.recordingRuntimeRegistryLocked()
	if stopping := registry.stopBySession[scope.RecordingSessionID]; stopping != nil && stopping.scope.matches(scope) {
		m.mu.Unlock()
		return nil, nil, ErrRecordingStopInProgress
	}

	recorder := m.recorderForScopeLocked(scope)
	startURL := recorder.GetStartURL()
	registry.stopBySession[scope.RecordingSessionID] = &recordingStopReservation{scope: scope, operationID: operationID}
	m.mu.Unlock()

	// Recorder stop can inspect DOM/storage and wait on browser operations. It
	// must not hold the Manager-wide registry lock, otherwise an instance A
	// stop serializes unrelated instance B Start/Stop and coordinator claims.
	actions, downloadedFiles, err := recorder.StopRecordingWithStorageScope(ctx, scope)

	m.mu.Lock()
	defer m.mu.Unlock()
	registry = m.recordingRuntimeRegistryLocked()
	ownedStop := false
	if stopping := registry.stopBySession[scope.RecordingSessionID]; stopping != nil && stopping.scope.matches(scope) && stopping.operationID == operationID {
		delete(registry.stopBySession, scope.RecordingSessionID)
		ownedStop = true
	}
	// A terminal lifecycle cleanup may have fenced this in-flight browser stop
	// while the Manager mutex was deliberately released.  Never publish its
	// late result into a session whose runtime resources have already been
	// released; the lifecycle/recovery path owns the subsequent convergence.
	if !ownedStop {
		return nil, nil, ErrRecordingStopInProgress
	}
	if err != nil {
		return nil, nil, err
	}
	m.restoreProjectDownloadBehaviorLocked(ctx, m.browserForScopeLocked(scope))
	m.publishFinalRecordingReceiptLocked(scope, startURL, actions, downloadedFiles, recorder.LastSemanticDOMSnapshot(), recorder.SyncRevision(), operationID)
	m.publishAuthSnapshotReceiptLocked(scope, recorder.TakeLastStorageState(scope))
	if actions, downloadedFiles, _, _, ok, err := m.claimFinalRecordingReceiptLocked(scope, operationID); err != nil {
		return nil, nil, err
	} else if ok {
		for instanceID, activeScope := range m.recordingRuntimeRegistryLocked().activeByInstance {
			if activeScope.matches(scope) {
				delete(m.recordingRuntimeRegistryLocked().activeByInstance, instanceID)
			}
		}
		return actions, downloadedFiles, nil
	}
	for instanceID, activeScope := range m.recordingRuntimeRegistryLocked().activeByInstance {
		if activeScope.matches(scope) {
			delete(m.recordingRuntimeRegistryLocked().activeByInstance, instanceID)
		}
	}
	return actions, downloadedFiles, nil
}

// AcknowledgeFinalRecordingReceipt consumes exactly the final receipt adopted
// by the database. Scope, operation owner and claim generation are all
// required: a delayed claimant must not delete a receipt taken over after TTL.
func (m *Manager) AcknowledgeFinalRecordingReceipt(scope RecordingStorageScope, receiptID, operationID string, claimGeneration uint64) bool {
	m.mu.Lock()
	receipt, ok := m.recordingRuntimeRegistryLocked().finalBySession[scope.RecordingSessionID]
	if !ok || !receipt.scope.matches(scope) || strings.TrimSpace(receiptID) == "" || receipt.receiptID != strings.TrimSpace(receiptID) || strings.TrimSpace(operationID) == "" || receipt.claimOperationID != strings.TrimSpace(operationID) || claimGeneration == 0 || receipt.claimGeneration != claimGeneration {
		m.mu.Unlock()
		return false
	}
	page := receipt.page
	receipt.state = recordingReceiptAcked
	delete(m.recordingRuntimeRegistryLocked().finalBySession, scope.RecordingSessionID)
	delete(m.recordingRuntimeRegistryLocked().draftBySession, scope.RecordingSessionID)
	delete(m.recordingRuntimeRegistryLocked().startBySession, scope.RecordingSessionID)
	m.clearAcknowledgedRecordingPageLocked(page)
	m.mu.Unlock()

	// The page was created in an isolated context for this exact recording
	// receipt. Closing it only after the matching receipt ACK means a failed
	// Stop, a competing owner, or a still-pending database transaction leaves
	// the recording window available for diagnosis and recovery.
	if page != nil {
		if err := page.Close(); err != nil {
			logger.Warn(context.Background(), "Failed to close acknowledged recording page: %v", err)
		}
	}
	return true
}

// clearAcknowledgedRecordingPageLocked clears only the frozen page pointer.
// A later recording owns a different isolated page and must never be cleared
// by an old receipt acknowledgement. The caller must hold m.mu.
func (m *Manager) clearAcknowledgedRecordingPageLocked(page *rod.Page) {
	if page == nil {
		return
	}
	for _, runtime := range m.instances {
		if runtime != nil && runtime.activePage == page {
			runtime.activePage = nil
		}
	}
	if m.activePage == page {
		m.activePage = nil
	}
}

// ReleaseFinalRecordingReceipt relinquishes exactly one runtime claim after a
// durable lifecycle outcome. It preserves the receipt for a new owner rather
// than deleting another operation's claim by session ID alone.
func (m *Manager) ReleaseFinalRecordingReceipt(scope RecordingStorageScope, receiptID, operationID string, claimGeneration uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	receipt, ok := m.recordingRuntimeRegistryLocked().finalBySession[scope.RecordingSessionID]
	if !ok || !receipt.scope.matches(scope) || strings.TrimSpace(receiptID) == "" || receipt.receiptID != strings.TrimSpace(receiptID) || strings.TrimSpace(operationID) == "" || receipt.claimOperationID != strings.TrimSpace(operationID) || claimGeneration == 0 || receipt.claimGeneration != claimGeneration {
		return false
	}
	receipt.state = recordingReceiptAvailable
	receipt.claimOperationID = ""
	receipt.claimedAt = time.Time{}
	return true
}

// RecordingArtifactStorageKey converts a browser download path into a storage key
// relative to the configured artifact root.
func (m *Manager) RecordingArtifactStorageKey(file models.DownloadedFile) string {
	storagePath := strings.TrimSpace(file.FilePath)
	if storagePath == "" {
		return ""
	}
	if !filepath.IsAbs(storagePath) {
		cleanRel := filepath.Clean(strings.TrimLeft(storagePath, `/\`))
		if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(os.PathSeparator)) {
			return ""
		}
		return filepath.ToSlash(cleanRel)
	}
	root := "."
	if m.config != nil && strings.TrimSpace(m.config.AssetsDir) != "" {
		root = m.config.AssetsDir
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(rootAbs, storagePath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

func (m *Manager) setProjectDownloadBehaviorLocked(browser *rod.Browser, deny bool) error {
	if browser == nil {
		return fmt.Errorf("project recording browser is unavailable")
	}
	behavior := proto.BrowserSetDownloadBehaviorBehaviorDeny
	downloadPath := ""
	if !deny {
		behavior = proto.BrowserSetDownloadBehaviorBehaviorAllow
		downloadPath = strings.TrimSpace(m.downloadPath)
		if downloadPath == "" {
			return fmt.Errorf("download path is unavailable")
		}
	}
	if err := callBrowserDownloadBehavior(browser, &proto.BrowserSetDownloadBehavior{
		Behavior:      behavior,
		DownloadPath:  downloadPath,
		EventsEnabled: true,
	}); err != nil {
		return fmt.Errorf("set project recording download behavior: %w", err)
	}
	if m.projectDownloadBrowsers == nil {
		m.projectDownloadBrowsers = make(map[*rod.Browser]struct{})
	}
	if deny {
		m.projectDownloadBrowsers[browser] = struct{}{}
		m.projectDownloadBrowser = browser
	} else {
		delete(m.projectDownloadBrowsers, browser)
		if m.projectDownloadBrowser == browser {
			m.projectDownloadBrowser = nil
			for remaining := range m.projectDownloadBrowsers {
				m.projectDownloadBrowser = remaining
				break
			}
		}
	}
	m.projectDownloadsDenied = len(m.projectDownloadBrowsers) > 0
	return nil
}

func (m *Manager) restoreProjectDownloadBehaviorLocked(ctx context.Context, targets ...*rod.Browser) {
	if !m.projectDownloadsDenied && len(targets) == 0 {
		return
	}
	var browser *rod.Browser
	if len(targets) > 0 {
		browser = targets[0]
	}
	if browser == nil {
		browser = m.projectDownloadBrowser
	}
	if browser == nil {
		browser = m.browser
	}
	if err := m.setProjectDownloadBehaviorLocked(browser, false); err != nil {
		logger.Warn(ctx, "Failed to restore download behavior after project recording: %v", err)
	}
}

func (m *Manager) browserForScopeLocked(scope RecordingStorageScope) *rod.Browser {
	for instanceID, activeScope := range m.recordingRuntimeRegistryLocked().activeByInstance {
		if activeScope.matches(scope) {
			if runtime := m.instances[instanceID]; runtime != nil {
				return runtime.browser
			}
		}
	}
	return m.projectDownloadBrowser
}

// IsRecording 检查是否正在录制
func (m *Manager) IsRecording() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, recorder := range m.recorders {
		if recorder != nil && recorder.IsRecording() {
			return true
		}
	}
	return m.recorder != nil && m.recorder.IsRecording()
}

// CurrentRecordingStorageScope reports the active project recording scope only
// while this manager still owns the recorder lifecycle.
func (m *Manager) CurrentRecordingStorageScope() (RecordingStorageScope, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, recorder := range m.recorders {
		if recorder != nil {
			if scope, ok := recorder.CurrentStorageScope(); ok {
				return scope, true
			}
		}
	}
	return RecordingStorageScope{}, false
}

// ActiveRecordingStorageScope reports the current recorder lifecycle and its
// project scope when the recorder belongs to a persisted recording session.
func (m *Manager) ActiveRecordingStorageScope() (RecordingStorageScope, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, recorder := range m.recorders {
		if recorder != nil {
			if scope, ok := recorder.ActiveStorageScope(); ok {
				return scope, true
			}
		}
	}
	return RecordingStorageScope{}, false
}

// IsRecordingStorageScopeActive reports whether this process still owns the
// exact persisted runtime scope. Lifecycle retries use it to finish the same
// Start operation without opening a second isolated page when another browser
// instance happens to be the manager's current active recorder.
func (m *Manager) IsRecordingStorageScopeActive(scope RecordingStorageScope) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, activeScope := range m.recordingRuntimeRegistryLocked().activeByInstance {
		if activeScope.matches(scope) {
			return true
		}
	}
	return false
}

// PendingStoppedRecordingStorageScope reports a page-local stop whose actions
// and storage snapshot are still waiting for its RecordingSession to persist.
func (m *Manager) PendingStoppedRecordingStorageScope() (RecordingStorageScope, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, receipt := range m.recordingRuntimeRegistryLocked().finalBySession {
		if receipt.state == recordingReceiptAvailable || receipt.state == recordingReceiptClaimed {
			return receipt.scope, true
		}
	}
	return RecordingStorageScope{}, false
}

// GetRecordingInfo 获取录制信息
func (m *Manager) GetRecordingInfo() map[string]interface{} {
	m.mu.Lock()
	info := m.recorderForInstanceLocked("").GetRecordingInfo()
	for _, receipt := range m.recordingRuntimeRegistryLocked().finalBySession {
		if receipt.state == recordingReceiptAvailable || receipt.state == recordingReceiptClaimed {
			info["in_page_stopped"] = true
			info["actions"] = append([]models.ScriptAction(nil), receipt.actions...)
			info["count"] = len(receipt.actions)
			info["downloaded_files"] = append([]models.DownloadedFile(nil), receipt.downloadedFiles...)
			if receipt.startURL != "" {
				info["start_url"] = receipt.startURL
			}
			break
		}
	}
	m.mu.Unlock()

	return info
}

// PeekLastRecordingStorageState is a read-only compatibility view. New
// lifecycle capture uses ClaimRecordingStorageState so an auth snapshot has a
// concrete operation owner before any database work starts.
func (m *Manager) PeekLastRecordingStorageState(scope RecordingStorageScope) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupRuntimeReceiptsLocked(time.Now().UTC())
	receipt, ok := m.recordingRuntimeRegistryLocked().authBySession[scope.RecordingSessionID]
	if !ok || !receipt.scope.matches(scope) || (receipt.state != recordingReceiptAvailable && receipt.state != recordingReceiptClaimed) {
		return nil
	}
	return cloneStorageState(receipt.storageState)
}

// ClaimRecordingStorageState gives one Capture operation exclusive temporary
// ownership. A database failure leaves the claim intact so the same operation
// can retry; a stale claim is safely reclaimable after its bounded TTL.
func (m *Manager) ClaimRecordingStorageState(scope RecordingStorageScope, operationID string) (map[string]any, string, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupRuntimeReceiptsLocked(time.Now().UTC())
	if strings.TrimSpace(operationID) == "" {
		return nil, "", 0, errRecordingReceiptUnavailable
	}
	receipt, ok := m.recordingRuntimeRegistryLocked().authBySession[scope.RecordingSessionID]
	if !ok || !receipt.scope.matches(scope) {
		return nil, "", 0, errRecordingReceiptUnavailable
	}
	now := time.Now().UTC()
	if receipt.state == recordingReceiptClaimed && now.Sub(receipt.claimedAt) >= recordingReceiptClaimTTL {
		receipt.state = recordingReceiptAvailable
		receipt.claimOperationID = ""
		receipt.claimedAt = time.Time{}
	}
	switch receipt.state {
	case recordingReceiptAvailable:
		receipt.state = recordingReceiptClaimed
		receipt.claimOperationID = operationID
		receipt.claimedAt = now
		receipt.claimGeneration++
	case recordingReceiptClaimed:
		if receipt.claimOperationID != operationID {
			return nil, "", 0, fmt.Errorf("recording auth snapshot is claimed by another operation")
		}
	case recordingReceiptAcked, recordingReceiptReleased, recordingReceiptExpired:
		return nil, "", 0, errRecordingReceiptUnavailable
	}
	return cloneStorageState(receipt.storageState), receipt.receiptID, receipt.claimGeneration, nil
}

func (m *Manager) AcknowledgeClaimedRecordingStorageState(scope RecordingStorageScope, receiptID, operationID string, claimGeneration uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	receipt, ok := m.recordingRuntimeRegistryLocked().authBySession[scope.RecordingSessionID]
	if !ok || !receipt.scope.matches(scope) || strings.TrimSpace(receiptID) == "" || receipt.receiptID != strings.TrimSpace(receiptID) || strings.TrimSpace(operationID) == "" || receipt.claimOperationID != strings.TrimSpace(operationID) || claimGeneration == 0 || receipt.claimGeneration != claimGeneration {
		return false
	}
	receipt.state = recordingReceiptAcked
	delete(m.recordingRuntimeRegistryLocked().authBySession, scope.RecordingSessionID)
	return true
}

// ReleaseClaimedRecordingStorageState abandons only the matching Capture
// claim. The snapshot becomes available for a later operation; an old owner
// cannot affect a receipt that has been reclaimed with a new generation.
func (m *Manager) ReleaseClaimedRecordingStorageState(scope RecordingStorageScope, receiptID, operationID string, claimGeneration uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	receipt, ok := m.recordingRuntimeRegistryLocked().authBySession[scope.RecordingSessionID]
	if !ok || !receipt.scope.matches(scope) || strings.TrimSpace(receiptID) == "" || receipt.receiptID != strings.TrimSpace(receiptID) || strings.TrimSpace(operationID) == "" || receipt.claimOperationID != strings.TrimSpace(operationID) || claimGeneration == 0 || receipt.claimGeneration != claimGeneration {
		return false
	}
	receipt.state = recordingReceiptAvailable
	receipt.claimOperationID = ""
	receipt.claimedAt = time.Time{}
	return true
}

// ClearInPageRecordingState 清除页面内录制状态(供前端保存或取消后调用)
func (m *Manager) ClearInPageRecordingState() {
	m.mu.Lock()
	m.clearFinalRecordingReceiptsLocked()
	m.mu.Unlock()
}

func (m *Manager) peekFinalRecordingReceiptLocked(scope RecordingStorageScope) ([]models.ScriptAction, []models.DownloadedFile, bool) {
	receipt, ok := m.recordingRuntimeRegistryLocked().finalBySession[scope.RecordingSessionID]
	if !ok || !receipt.scope.matches(scope) || (receipt.state != recordingReceiptAvailable && receipt.state != recordingReceiptClaimed) {
		return nil, nil, false
	}
	actions := append([]models.ScriptAction(nil), receipt.actions...)
	downloadedFiles := append([]models.DownloadedFile(nil), receipt.downloadedFiles...)
	return actions, downloadedFiles, true
}

// FinalRecordingDOMSnapshot returns the semantic DOM bundled with an
// unacknowledged final receipt. It is used only by the runtime adapter to pass
// the receipt to LifecycleService; callers cannot obtain storage/cookie data.
func (m *Manager) FinalRecordingDOMSnapshot(scope RecordingStorageScope) json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupRuntimeReceiptsLocked(time.Now().UTC())
	receipt, ok := m.recordingRuntimeRegistryLocked().finalBySession[scope.RecordingSessionID]
	if !ok || !receipt.scope.matches(scope) || (receipt.state != recordingReceiptAvailable && receipt.state != recordingReceiptClaimed) {
		return nil
	}
	return append(json.RawMessage(nil), receipt.domSnapshot...)
}

// FinalRecordingReceiptInfo exposes only durable receipt identity/version to
// the lifecycle adapter. The receipt payload itself remains Manager-owned
// until LifecycleService commits and ACKs it.
func (m *Manager) FinalRecordingReceiptInfo(scope RecordingStorageScope) (string, uint64, uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupRuntimeReceiptsLocked(time.Now().UTC())
	receipt, ok := m.recordingRuntimeRegistryLocked().finalBySession[scope.RecordingSessionID]
	if !ok || !receipt.scope.matches(scope) || (receipt.state != recordingReceiptAvailable && receipt.state != recordingReceiptClaimed) {
		return "", 0, 0
	}
	return receipt.receiptID, receipt.syncRevision, receipt.claimGeneration
}

// HasPendingFinalRecordingReceipt is a full-scope lookup used by lifecycle
// recovery. It intentionally does not expose the old global stopped-recorder
// mirror, which may point at another browser instance.
func (m *Manager) HasPendingFinalRecordingReceipt(scope RecordingStorageScope) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupRuntimeReceiptsLocked(time.Now().UTC())
	receipt := m.recordingRuntimeRegistryLocked().finalBySession[scope.RecordingSessionID]
	return receipt != nil && receipt.scope.matches(scope) && (receipt.state == recordingReceiptAvailable || receipt.state == recordingReceiptClaimed)
}

func (m *Manager) claimFinalRecordingReceiptLocked(scope RecordingStorageScope, operationID string) ([]models.ScriptAction, []models.DownloadedFile, string, uint64, bool, error) {
	receipt, ok := m.recordingRuntimeRegistryLocked().finalBySession[scope.RecordingSessionID]
	if !ok || !receipt.scope.matches(scope) {
		return nil, nil, "", 0, false, nil
	}
	now := time.Now().UTC()
	if receipt.state == recordingReceiptClaimed && !receipt.claimedAt.IsZero() && now.Sub(receipt.claimedAt) >= recordingReceiptClaimTTL {
		receipt.state = recordingReceiptAvailable
		receipt.claimOperationID = ""
		receipt.claimedAt = time.Time{}
	}
	switch receipt.state {
	case recordingReceiptAvailable:
		if operationID != "" {
			receipt.state = recordingReceiptClaimed
			receipt.claimOperationID = operationID
			receipt.claimedAt = now
			receipt.claimGeneration++
		}
	case recordingReceiptClaimed:
		if operationID == "" || receipt.claimOperationID != operationID {
			return nil, nil, "", 0, false, fmt.Errorf("%w: final receipt is claimed by another operation", ErrRecordingStopInProgress)
		}
	case recordingReceiptReleased, recordingReceiptExpired, recordingReceiptAcked:
		return nil, nil, "", 0, false, fmt.Errorf("recording receipt is no longer available")
	}
	return append([]models.ScriptAction(nil), receipt.actions...), append([]models.DownloadedFile(nil), receipt.downloadedFiles...), receipt.receiptID, receipt.claimGeneration, true, nil
}

func (m *Manager) publishFinalRecordingReceiptLocked(scope RecordingStorageScope, startURL string, actions []models.ScriptAction, downloadedFiles []models.DownloadedFile, domSnapshot json.RawMessage, syncRevision uint64, operationIDs ...string) {
	operationID := ""
	if len(operationIDs) > 0 {
		operationID = operationIDs[0]
	}
	receiptID := uuid.NewString()
	receipt := &recordingFinalReceipt{receiptID: receiptID, scope: scope, page: m.activeRecordingPageLocked(scope), startURL: startURL, actions: append([]models.ScriptAction(nil), actions...), downloadedFiles: append([]models.DownloadedFile(nil), downloadedFiles...), domSnapshot: append(json.RawMessage(nil), domSnapshot...), syncRevision: syncRevision, state: recordingReceiptAvailable, frozenAt: time.Now().UTC()}
	if strings.TrimSpace(operationID) != "" {
		receipt.state = recordingReceiptClaimed
		receipt.claimOperationID = strings.TrimSpace(operationID)
		receipt.claimedAt = time.Now().UTC()
		receipt.claimGeneration = 1
	}
	m.recordingRuntimeRegistryLocked().finalBySession[scope.RecordingSessionID] = receipt
	m.emitRuntimeEventLocked(RecordingRuntimeEvent{ID: deterministicRuntimeEventID("recording_stopped", scope, receipt.syncRevision, receiptID), Kind: "recording_stopped", Scope: scope, ReceiptID: receiptID, OperationID: receipt.claimOperationID, ClaimGeneration: receipt.claimGeneration, SyncRevision: receipt.syncRevision, Actions: append([]models.ScriptAction(nil), actions...), DOMSnapshot: append(json.RawMessage(nil), domSnapshot...)})
}

// activeRecordingPageLocked snapshots the page that belongs to the current
// scoped recorder while Stop is freezing its final receipt. The caller must
// hold m.mu.
func (m *Manager) activeRecordingPageLocked(scope RecordingStorageScope) *rod.Page {
	for instanceID, activeScope := range m.recordingRuntimeRegistryLocked().activeByInstance {
		if activeScope.matches(scope) {
			if runtime := m.instances[instanceID]; runtime != nil {
				return runtime.activePage
			}
		}
	}
	return nil
}

func (m *Manager) publishAuthSnapshotReceiptLocked(scope RecordingStorageScope, state map[string]any) {
	if len(state) == 0 || scope.isZero() {
		return
	}
	m.recordingRuntimeRegistryLocked().authBySession[scope.RecordingSessionID] = &recordingAuthSnapshotReceipt{receiptID: uuid.NewString(), scope: scope, storageState: cloneStorageState(state), state: recordingReceiptAvailable, frozenAt: time.Now().UTC()}
}

func (m *Manager) cleanupRuntimeReceiptsLocked(now time.Time) {
	registry := m.recordingRuntimeRegistryLocked()
	for sessionID, receipt := range registry.finalBySession {
		if receipt.state == recordingReceiptClaimed && now.Sub(receipt.claimedAt) < recordingReceiptClaimTTL {
			continue
		}
		if !receipt.frozenAt.IsZero() && now.Sub(receipt.frozenAt) >= recordingReceiptTTL {
			receipt.state = recordingReceiptExpired
			registry.expiredBySession[sessionID] = RecordingRuntimeEvent{
				ID:           deterministicRuntimeEventID("recording_receipt_expired", receipt.scope, receipt.syncRevision, receipt.receiptID),
				Kind:         "recording_receipt_expired",
				Scope:        receipt.scope,
				ReceiptID:    receipt.receiptID,
				SyncRevision: receipt.syncRevision,
			}
			delete(registry.finalBySession, sessionID)
		}
	}
	for sessionID, receipt := range registry.authBySession {
		if receipt.state == recordingReceiptClaimed && now.Sub(receipt.claimedAt) < recordingReceiptClaimTTL {
			continue
		}
		if !receipt.frozenAt.IsZero() && now.Sub(receipt.frozenAt) >= recordingReceiptTTL {
			receipt.state = recordingReceiptExpired
			delete(registry.authBySession, sessionID)
		}
	}
}

func (m *Manager) clearFinalRecordingReceiptsLocked() {
	m.recordingRuntimeRegistryLocked().activeByInstance = make(map[string]RecordingStorageScope)
	m.recordingRuntimeRegistryLocked().startBySession = make(map[string]*recordingStartReservation)
	m.recordingRuntimeRegistryLocked().stopBySession = make(map[string]*recordingStopReservation)
	m.recordingRuntimeRegistryLocked().draftBySession = make(map[string]*recordingDraftReceipt)
	m.recordingRuntimeRegistryLocked().finalBySession = make(map[string]*recordingFinalReceipt)
	m.recordingRuntimeRegistryLocked().authBySession = make(map[string]*recordingAuthSnapshotReceipt)
	m.recordingRuntimeRegistryLocked().expiredBySession = make(map[string]RecordingRuntimeEvent)
	m.recordingRuntimeRegistryLocked().leaseLostBySession = make(map[string]RecordingRuntimeEvent)
}

func (m *Manager) recordingRuntimeRegistryLocked() *recordingRuntimeRegistry {
	if m.recordingRegistry == nil {
		m.recordingRegistry = newRecordingRuntimeRegistry()
	}
	if m.recordingRegistry.activeByInstance == nil {
		m.recordingRegistry.activeByInstance = make(map[string]RecordingStorageScope)
	}
	if m.recordingRegistry.startBySession == nil {
		m.recordingRegistry.startBySession = make(map[string]*recordingStartReservation)
	}
	if m.recordingRegistry.stopBySession == nil {
		m.recordingRegistry.stopBySession = make(map[string]*recordingStopReservation)
	}
	if m.recordingRegistry.draftBySession == nil {
		m.recordingRegistry.draftBySession = make(map[string]*recordingDraftReceipt)
	}
	if m.recordingRegistry.finalBySession == nil {
		m.recordingRegistry.finalBySession = make(map[string]*recordingFinalReceipt)
	}
	if m.recordingRegistry.authBySession == nil {
		m.recordingRegistry.authBySession = make(map[string]*recordingAuthSnapshotReceipt)
	}
	if m.recordingRegistry.expiredBySession == nil {
		m.recordingRegistry.expiredBySession = make(map[string]RecordingRuntimeEvent)
	}
	if m.recordingRegistry.leaseLostBySession == nil {
		m.recordingRegistry.leaseLostBySession = make(map[string]RecordingRuntimeEvent)
	}
	return m.recordingRegistry
}

// PlayScript 回放脚本
// instanceID: 指定实例ID，空字符串表示使用当前实例
func (m *Manager) PlayScript(ctx context.Context, script *models.Script, instanceID string) (result *models.PlayResult, page *rod.Page, err error) {
	// 捕获 panic 并转换为错误
	defer func() {
		if r := recover(); r != nil {
			logger.Error(ctx, "Panic in PlayScript: %v", r)
			err = fmt.Errorf("failed to play script: browser connection may be closed (panic: %v)", r)
			result = nil
			page = nil
		}
	}()

	// 获取指定实例的浏览器
	browser, _, instance, err := m.getInstanceBrowser(instanceID)
	if err != nil {
		return nil, nil, err
	}

	// 检查浏览器连接是否仍然有效
	if err := checkBrowserConnection(browser); err != nil {
		logger.Error(ctx, "Browser connection check failed: %v", err)
		return nil, nil, fmt.Errorf("browser connection is closed or invalid: %w", err)
	}

	// 确定使用的实例ID（从 instance 对象获取，可能从空字符串转换为 default）
	usedInstanceID := instanceID
	instanceName := ""
	if instance != nil {
		usedInstanceID = instance.ID
		instanceName = instance.Name
	} else if usedInstanceID == "" {
		// 向后兼容：如果没有 instance 对象，使用 currentInstanceID
		usedInstanceID = m.currentInstanceID
	}

	// 创建执行记录
	executionID := fmt.Sprintf("%s-%d", script.ID, time.Now().UnixNano())
	execution := &models.ScriptExecution{
		ID:           executionID,
		ScriptID:     script.ID,
		ScriptName:   script.Name,
		InstanceID:   usedInstanceID,
		InstanceName: instanceName,
		StartTime:    time.Now(),
		TotalSteps:   len(script.Actions),
		CreatedAt:    time.Now(),
	}

	// 根据脚本的URL匹配配置
	scriptURL := script.URL
	if scriptURL == "" && len(script.Actions) > 0 {
		// 如果脚本没有URL，尝试从第一个action获取
		scriptURL = script.Actions[0].URL
	}

	config := m.getConfigForURL(scriptURL)
	logger.Info(ctx, fmt.Sprintf("Replay script URL: %s, using configuration: %s", scriptURL, config.Name))

	// 创建新页面用于回放
	// 根据配置决定是否使用 stealth
	useStealth := true // 默认使用stealth
	if config.UseStealth != nil {
		useStealth = *config.UseStealth
	}

	if useStealth {
		page = stealth.MustPage(browser)
		logger.Info(ctx, "Replay using Stealth mode")
	} else {
		page = browser.MustPage()
		logger.Info(ctx, "Replay not using Stealth mode")
	}

	m.setPageWindow(page)

	// 设置 User Agent
	userAgent := config.UserAgent
	if userAgent == "" {
		userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"
	}
	page = page.MustSetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent: userAgent,
	})

	// 为回放页面授予剪贴板权限
	if scriptURL != "" {
		grantPlayPermissions := &proto.BrowserGrantPermissions{
			Origin: scriptURL,
			Permissions: []proto.BrowserPermissionType{
				proto.BrowserPermissionTypeClipboardReadWrite,
				proto.BrowserPermissionTypeClipboardSanitizedWrite,
			},
		}
		if err := grantPlayPermissions.Call(browser); err != nil {
			logger.Warn(ctx, "Failed to grant clipboard permissions for playback: %v", err)
		} else {
			logger.Info(ctx, "✓ Clipboard permissions granted for playback")
		}
	}

	// 创建播放器，传入当前语言设置
	currentLang := m.currentLanguage
	if currentLang == "" {
		currentLang = "zh-CN" // 默认简体中文
	}
	player := NewPlayer(currentLang)
	player.agentManager = m.agentManager // 设置 Agent 管理器用于 AI 控制功能
	player.browserManager = m            // 设置 Browser 管理器用于同步活跃页面

	// 设置下载路径并启动下载监听
	if m.downloadPath != "" {
		player.SetDownloadPath(m.downloadPath)
		player.StartDownloadListener(ctx, browser)
		logger.Info(ctx, "Download tracking enabled for playback, path: %s", m.downloadPath)
	}

	// 检查是否需要录制视频
	recordingConfig := m.db.GetDefaultRecordingConfig()
	var videoPath string
	if recordingConfig.Enabled {
		// 创建输出目录
		outputDir := recordingConfig.OutputDir
		if outputDir == "" {
			outputDir = "recordings"
		}
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			logger.Warn(ctx, "Failed to create recording directory: %v", err)
		} else {
			// 生成视频文件名
			timestamp := time.Now().Format("20060102_150405")
			// 强制使用 gif 格式
			videoPath = fmt.Sprintf("%s/%s_%s.gif", outputDir, script.Name, timestamp)

			// 开始录制
			frameRate := recordingConfig.FrameRate
			if frameRate <= 0 {
				frameRate = 15
			}
			quality := recordingConfig.Quality
			if quality <= 0 || quality > 100 {
				quality = 70
			}

			logger.Info(ctx, "Starting video recording: %s (frame rate: %d, quality: %d)", videoPath, frameRate, quality)
			if err := player.StartVideoRecording(page, videoPath, frameRate, quality); err != nil {
				logger.Warn(ctx, "Failed to start video recording: %v", err)
				videoPath = "" // 清空路径，表示录制失败
			}
		}
	}

	// 执行回放
	playErr := player.PlayScript(ctx, page, script, m.currentLanguage)

	// 停止下载监听
	if m.downloadPath != "" {
		player.StopDownloadListener(ctx)
	}

	// 停止视频录制
	if videoPath != "" {
		logger.Info(ctx, "Stopping video recording")
		if err := player.StopVideoRecording(videoPath, recordingConfig.FrameRate); err != nil {
			logger.Warn(ctx, "Failed to stop video recording: %v", err)
		} else {
			execution.VideoPath = videoPath
			logger.Info(ctx, "Video saved: %s", videoPath)
		}
	}

	// 记录结束时间和耗时
	execution.EndTime = time.Now()
	execution.Duration = execution.EndTime.Sub(execution.StartTime).Milliseconds()

	// 记录统计信息
	execution.SuccessSteps = player.GetSuccessCount()
	execution.FailedSteps = player.GetFailCount()
	execution.ExtractedData = player.GetExtractedData()

	// 判断是否成功
	if playErr != nil {
		execution.Success = false
		execution.ErrorMsg = playErr.Error()
		execution.Message = "Script execution failed"
	} else {
		execution.Success = true
		execution.Message = "Script execution successful"
	}

	// 保存执行记录到数据库
	if m.db != nil {
		if err := m.db.SaveScriptExecution(execution); err != nil {
			logger.Warn(ctx, "Failed to save script execution record: %v", err)
		} else {
			logger.Info(ctx, "Script execution record saved: %s", executionID)
		}
	}

	// 如果执行失败，返回错误
	if playErr != nil {
		return &models.PlayResult{
			Success: false,
			Message: playErr.Error(),
			Errors:  []string{playErr.Error()},
		}, page, playErr
	}

	// 返回回放结果，包含抓取的数据
	extractedData := player.GetExtractedData()
	logger.Info(ctx, "[PlayScript] Extracted data length: %d", len(extractedData))
	if len(extractedData) > 0 {
		keys := make([]string, 0, len(extractedData))
		for k := range extractedData {
			keys = append(keys, k)
		}
		logger.Info(ctx, "[PlayScript] Extracted data keys: %v", keys)
	}

	// 添加下载的文件路径到提取数据
	downloadedFiles := player.GetDownloadedFiles()
	if len(downloadedFiles) > 0 {
		extractedData["downloaded_files"] = downloadedFiles
		logger.Info(ctx, "[PlayScript] Downloaded files count: %d", len(downloadedFiles))
		for i, file := range downloadedFiles {
			logger.Info(ctx, "[PlayScript] Downloaded file #%d: %s", i+1, file)
		}
	}

	return &models.PlayResult{
		Success:       true,
		Message:       "Script replay completed",
		ExtractedData: extractedData,
	}, page, nil
}

// checkInPageRecordingRequests 检查页面内的录制控制请求
func (m *Manager) checkInPageRecordingRequests(ctx context.Context, page *rod.Page, recorder *Recorder, instanceID string, scope RecordingStorageScope) {
	if recorder == nil {
		m.mu.Lock()
		recorder = m.recorderForInstanceLocked("")
		m.mu.Unlock()
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// 检查是否有录制开始请求
			result, err := page.Eval(`() => {
				if (window.__startRecordingRequest__) {
					var req = window.__startRecordingRequest__;
					delete window.__startRecordingRequest__;
					return { type: 'start_recording', data: req };
				}
				if (window.__screenshotRequest__) {
					var req = window.__screenshotRequest__;
					delete window.__screenshotRequest__;
					return { type: 'screenshot', data: req };
				}
				return null;
			}`)

			if err == nil && result != nil && !result.Value.Nil() {
				// 解析请求类型
				resultMap, ok := result.Value.Val().(map[string]interface{})
				if !ok {
					continue
				}

				reqType, _ := resultMap["type"].(string)

				if reqType == "start_recording" {
					logger.Info(ctx, "Detected in-page recording start request")

					// 获取当前页面URL
					info, err := page.Info()
					if err == nil {
						// 获取当前语言设置（需要锁保护）
						m.mu.Lock()
						currentLang := m.currentLanguage
						m.mu.Unlock()

						if currentLang == "" {
							currentLang = "zh-CN"
						}
						// 开始录制
						if err := recorder.StartRecording(ctx, page, info.URL, currentLang); err != nil {
							logger.Error(ctx, "Failed to start recording from in-page request: %v", err)
						} else {
							logger.Info(ctx, "✓ Recording started from in-page button")
							// 通知页面显示录制UI
							_, _ = page.Eval(`() => {
								window.__isRecordingActive__ = true;
								if (typeof createRecorderUI === 'function') createRecorderUI();
								if (typeof createHighlightElement === 'function') createHighlightElement();
							}`)
						}
					} else {
						logger.Error(ctx, "Failed to get page info for in-page recording start: %v", err)
					}
				} else if reqType == "screenshot" {
					logger.Info(ctx, "Detected in-page screenshot request")

					// 提取截图请求数据（仅用于日志）
					data, _ := resultMap["data"].(map[string]interface{})
					mode, _ := data["mode"].(string)

					if mode == "" {
						mode = "viewport"
					}

					// 注意：不在后端添加 action，因为前端已经通过 recordAction() 添加了
					// 停止录制时会从前端的 window.__recordedActions__ 同步过来
					// 这样避免重复添加截图操作
					logger.Info(ctx, "Screenshot action will be synced from frontend: mode=%s", mode)
				}
			}

			// 检查是否有录制停止请求
			stopResult, err := page.Eval(`() => {
				if (window.__stopRecordingRequest__) {
					var req = window.__stopRecordingRequest__;
					delete window.__stopRecordingRequest__;
					return req;
				}
				return null;
			}`)

			if err == nil && stopResult != nil && !stopResult.Value.Nil() {
				logger.Info(ctx, "Detected in-page recording stop request")

				actions, downloadedFiles, driven, err := m.driveInPageRecordingStop(ctx, scope)
				if !driven {
					// P4.7.6 has no unscoped page Stop driver. Consuming this legacy
					// signal must not bypass stopBySession or tell the page it stopped.
					logger.Warn(ctx, "Ignoring unscoped in-page recording Stop request")
					continue
				}
				if err != nil {
					if errors.Is(err, ErrRecordingStopInProgress) {
						logger.Info(ctx, "In-page Stop waits for the existing scoped stop driver")
						continue
					}
					logger.Error(ctx, "Failed to stop project recording from in-page request: %v", err)
					continue
				}
				logger.Info(ctx, "Project recording Stop request completed in runtime, operation=%s actions=%d downloads=%d", runtimeStopOperationID(scope), len(actions), len(downloadedFiles))

				logger.Info(ctx, "✓ Recording stopped from in-page button")
				// 通知页面:录制已停止
				_, _ = page.Eval(`() => {
					window.__recordingStoppedByInPage__ = true;
				}`)
			}

		case <-ctx.Done():
			return
		}

		// Project recording is owned by its scoped instance lease, not by the
		// legacy global activePage mirror. A different browser instance must
		// never stop this page's in-page Stop bridge.
		m.mu.Lock()
		isActive := m.recordingLoopOwnsScopeLocked(instanceID, page, scope)
		m.mu.Unlock()
		if !isActive {
			return
		}
	}
}

// driveInPageRecordingStop is deliberately scoped: page UI may request a Stop
// but only a RecordingSession owner can drive Recorder through stopBySession.
// Returning driven=false for legacy zero scope prevents a second direct
// Recorder.StopRecording path from reappearing in the polling loop.
func (m *Manager) driveInPageRecordingStop(ctx context.Context, scope RecordingStorageScope) ([]models.ScriptAction, []models.DownloadedFile, bool, error) {
	if scope.isZero() {
		return nil, nil, false, nil
	}
	actions, downloadedFiles, err := m.StopRecordingWithClaim(ctx, scope, runtimeStopOperationID(scope))
	return actions, downloadedFiles, true, err
}

func (m *Manager) recordingLoopOwnsScopeLocked(instanceID string, page *rod.Page, scope RecordingStorageScope) bool {
	if !scope.isZero() {
		active, ok := m.recordingRuntimeRegistryLocked().activeByInstance[instanceID]
		return ok && active.matches(scope)
	}
	runtime := m.instances[instanceID]
	return runtime != nil && runtime.activePage == page
}

// isHeadlessEnvironment 检测当前环境是否为无GUI环境
func isHeadlessEnvironment() bool {
	// 1. 优先检查是否在 Docker 容器中
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}

	// 2. 检查 cgroup 文件是否包含 docker 或 containerd 标识（仅限 Linux）
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		content := string(data)
		if strings.Contains(content, "docker") || strings.Contains(content, "containerd") {
			return true
		}
	}

	// 3. 根据操作系统类型判断
	osType := strings.ToLower(os.Getenv("GOOS"))
	if osType == "" {
		// 如果 GOOS 环境变量不存在，使用 runtime.GOOS
		osType = strings.ToLower(runtime.GOOS)
	}

	// Windows 和 macOS 默认有 GUI 环境
	if osType == "windows" || osType == "darwin" {
		return false
	}

	// 4. Linux 环境下检查 DISPLAY 和 WAYLAND_DISPLAY 环境变量
	if osType == "linux" {
		display := os.Getenv("DISPLAY")
		waylandDisplay := os.Getenv("WAYLAND_DISPLAY")

		// 如果两个环境变量都为空，则认为是无GUI环境
		if display == "" && waylandDisplay == "" {
			return true
		}
	}

	// 默认认为有 GUI 环境
	return false
}

// GetDefaultBrowserConfig 获取默认浏览器配置（公开方法）
func (m *Manager) GetDefaultBrowserConfig() *models.BrowserConfig {
	return m.getDefaultBrowserConfig()
}

// getDefaultBrowserConfig 获取默认浏览器配置
func (m *Manager) getDefaultBrowserConfig() *models.BrowserConfig {
	useStealth := true
	// 根据环境自动设置 headless 默认值
	// 如果是无GUI环境（Docker、Linux服务器等），默认使用 headless 模式
	headless := isHeadlessEnvironment()

	// Linux 下默认启用 NoSandbox，避免权限问题导致启动失败
	noSandbox := runtime.GOOS == "linux"

	// 记录环境检测结果
	osType := runtime.GOOS
	display := os.Getenv("DISPLAY")
	waylandDisplay := os.Getenv("WAYLAND_DISPLAY")

	logger.Info(context.Background(),
		"detected browser environment: OS=%s, DISPLAY=%s, WAYLAND_DISPLAY=%s, headless=%v, noSandbox=%v",
		osType, display, waylandDisplay, headless, noSandbox)

	launchArgs := []string{
		"disable-blink-features=AutomationControlled",
		"no-first-run",
		"no-default-browser-check",
		"window-size=1920,1080",
		"start-maximized",
	}

	if headless {
		launchArgs = append(launchArgs,
			"disable-background-timer-throttling",
			"disable-backgrounding-occluded-windows",
			"disable-renderer-backgrounding",
			"disable-ipc-flooding-protection",
			"enable-features=NetworkService,NetworkServiceInProcess",
		)
	}

	return &models.BrowserConfig{
		ID:          "default",
		Name:        "默认配置",
		Description: "系统默认浏览器配置，适用于所有网站",
		URLPattern:  "", // 空表示默认配置
		UserAgent:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
		UseStealth:  &useStealth,
		Headless:    &headless,
		NoSandbox:   &noSandbox,
		LaunchArgs:  launchArgs,
		IsDefault:   true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

// ==================== 多实例管理 ====================

// checkBrowserConnection 检查浏览器连接是否仍然有效
// normalizeURLSchema 自动补全 URL 的 schema（http/https）
// localhost 和 127.0.0.1 补 http，其余补 https
func normalizeURLSchema(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return rawURL
	}

	// 已经有 schema 则不处理
	lower := strings.ToLower(rawURL)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "file://") || strings.HasPrefix(lower, "data:") ||
		strings.HasPrefix(lower, "about:") || strings.HasPrefix(lower, "chrome://") {
		return rawURL
	}

	// 提取 host 部分（去掉 path/port）
	host := rawURL
	if idx := strings.Index(host, "/"); idx != -1 {
		host = host[:idx]
	}
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}
	host = strings.ToLower(host)

	if host == "localhost" || host == "127.0.0.1" || host == "0.0.0.0" || host == "::1" {
		return "http://" + rawURL
	}
	return "https://" + rawURL
}

func checkBrowserConnection(browser *rod.Browser) error {
	if browser == nil {
		return fmt.Errorf("browser is nil")
	}

	// 尝试获取浏览器页面列表，这是一个轻量级的检查
	// 如果连接已关闭，这个调用会失败
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := browser.Context(ctx).Pages()
	if err != nil {
		return fmt.Errorf("failed to get browser pages: connection may be closed: %w", err)
	}

	return nil
}

// cleanupSingletonLock 清理 Chrome 的进程单例锁文件
// 这些锁文件在 Chrome 异常退出时可能没有被清理，导致无法启动新实例
func (m *Manager) cleanupSingletonLock(ctx context.Context, userDataDir string) error {
	// Chrome 在用户数据目录中创建的锁文件
	// Linux: SingletonLock, SingletonCookie, SingletonSocket
	// Windows: lockfile
	// Stale debug port file: DevToolsActivePort
	lockFiles := []string{
		"SingletonLock",
		"SingletonCookie",
		"SingletonSocket",
		"lockfile",           // Windows Chrome lock file
		"DevToolsActivePort", // Stale debug port info (prevents reconnection confusion)
	}

	var cleanedFiles []string
	var failedFiles []string

	for _, lockFile := range lockFiles {
		lockPath := filepath.Join(userDataDir, lockFile)

		// 检查文件是否存在
		if _, err := os.Stat(lockPath); err == nil {
			// 尝试删除锁文件，最多重试 3 次
			deleted := false
			for attempt := 1; attempt <= 3; attempt++ {
				if err := os.Remove(lockPath); err != nil {
					if attempt < 3 {
						// 等待一小段时间后重试
						time.Sleep(100 * time.Millisecond)
						continue
					}
					// 最后一次尝试失败
					logger.Warn(ctx, "Failed to remove lock file %s after %d attempts: %v", lockFile, attempt, err)
					failedFiles = append(failedFiles, lockFile)
				} else {
					deleted = true
					break
				}
			}

			if deleted {
				cleanedFiles = append(cleanedFiles, lockFile)
			}
		}
	}

	if len(cleanedFiles) > 0 {
		logger.Info(ctx, "Cleaned up lock files: %v", cleanedFiles)
	}

	if len(failedFiles) > 0 {
		logger.Warn(ctx, "Failed to clean some lock files: %v (may need manual cleanup or process is still running)", failedFiles)
	}

	return nil
}

// lockFilesStillExist checks if Chrome lock files exist in the user data directory.
// Returns true if any lock file is present (indicating a Chrome process may be holding them).
func (m *Manager) lockFilesStillExist(userDataDir string) bool {
	for _, name := range []string{"SingletonLock", "SingletonSocket", "lockfile"} {
		if _, err := os.Stat(filepath.Join(userDataDir, name)); err == nil {
			return true
		}
	}
	return false
}

// killTrackedChromeProfileOwner only terminates a process when its persisted
// BrowserWing marker, local DevTools endpoint, and listening PID all agree.
// This keeps profile recovery scoped to BrowserWing instead of user Chrome.
func (m *Manager) killTrackedChromeProfileOwner(ctx context.Context, userDataDir string) (bool, error) {
	if runtime.GOOS != "windows" {
		return false, nil
	}
	owner, err := readBrowserProfileOwner(userDataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !browserProfileOwnerDevtoolsResponding(ctx, owner) {
		logger.Warn(ctx, "[killOrphanedChrome] Tracked profile owner PID %d did not confirm its local DevTools endpoint; skipping termination", owner.PID)
		return false, nil
	}
	listenerPID, found, err := windowsListeningPIDForPort(owner.ControlURL)
	if err != nil {
		return false, err
	}
	if !found || listenerPID != owner.PID {
		logger.Warn(ctx, "[killOrphanedChrome] Tracked profile owner PID %d does not match the local DevTools listener; skipping termination", owner.PID)
		return false, nil
	}
	if err := terminateWindowsProcess(owner.PID); err != nil {
		return false, err
	}
	if err := os.Remove(browserProfileOwnerMarkerPath(userDataDir)); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("remove browser profile owner marker: %w", err)
	}
	return true, nil
}

func browserProfileOwnerDevtoolsResponding(ctx context.Context, owner browserProfileOwner) bool {
	endpoint, err := browserProfileOwnerDevtoolsEndpoint(owner)
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	var version struct {
		Browser              string `json:"Browser"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&version); err != nil {
		return false
	}
	if !browserProfileOwnerDevtoolsProductSupported(version.Browser) || version.WebSocketDebuggerURL == "" {
		return false
	}
	return browserProfileOwnerDevtoolsIdentityMatches(owner.ControlURL, version.WebSocketDebuggerURL)
}

type browserProfileOwnerDevtoolsIdentity struct {
	host      string
	port      int
	browserID string
}

// browserProfileOwnerDevtoolsIdentityMatches proves that /json/version belongs
// to the browser originally written into the profile-owner marker. Host and
// PID alone are reusable, so a browser WebSocket path without its opaque
// browser id is deliberately insufficient to authorize termination.
func browserProfileOwnerDevtoolsIdentityMatches(ownerURL, reportedURL string) bool {
	owner, ok := browserProfileOwnerDevtoolsIdentityFromURL(ownerURL)
	if !ok {
		return false
	}
	reported, ok := browserProfileOwnerDevtoolsIdentityFromURL(reportedURL)
	return ok && owner == reported
}

func browserProfileOwnerDevtoolsIdentityFromURL(rawURL string) (browserProfileOwnerDevtoolsIdentity, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || !strings.EqualFold(parsed.Scheme, "ws") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return browserProfileOwnerDevtoolsIdentity{}, false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if !isLocalDevtoolsHost(host) {
		return browserProfileOwnerDevtoolsIdentity{}, false
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return browserProfileOwnerDevtoolsIdentity{}, false
	}
	const browserPathPrefix = "/devtools/browser/"
	if !strings.HasPrefix(parsed.Path, browserPathPrefix) {
		return browserProfileOwnerDevtoolsIdentity{}, false
	}
	browserID := strings.TrimPrefix(parsed.Path, browserPathPrefix)
	if browserID == "" || strings.Contains(browserID, "/") {
		return browserProfileOwnerDevtoolsIdentity{}, false
	}
	return browserProfileOwnerDevtoolsIdentity{host: host, port: port, browserID: browserID}, true
}

func browserProfileOwnerDevtoolsProductSupported(product string) bool {
	normalized := strings.ToLower(strings.TrimSpace(product))
	return strings.Contains(normalized, "chrome") || strings.Contains(normalized, "edg/")
}

func windowsListeningPIDForPort(controlURL string) (int, bool, error) {
	parsed, err := url.Parse(controlURL)
	if err != nil {
		return 0, false, fmt.Errorf("parse local DevTools URL: %w", err)
	}
	port := parsed.Port()
	if port == "" {
		return 0, false, fmt.Errorf("local DevTools URL has no port")
	}
	output, err := exec.Command("netstat", "-ano", "-p", "tcp").CombinedOutput()
	if err != nil {
		return 0, false, fmt.Errorf("list TCP listeners: %w", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") || !strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		if !strings.HasSuffix(fields[1], ":"+port) {
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil || pid <= 0 {
			continue
		}
		return pid, true, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, false, fmt.Errorf("parse TCP listeners: %w", err)
	}
	return 0, false, nil
}

func terminateWindowsProcess(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("Chrome PID is invalid")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find tracked Chrome PID %d: %w", pid, err)
	}
	if err := process.Kill(); err != nil {
		return fmt.Errorf("terminate tracked Chrome PID %d: %w", pid, err)
	}
	return nil
}

// killChromeByUserDataDir kills Chrome processes associated with the given user data directory.
// It works on both Windows and Unix systems. Unlike KillOrphanedChromeProcesses which uses
// m.config.Browser.UserDataDir, this method accepts an explicit directory parameter.
func (m *Manager) killChromeByUserDataDir(ctx context.Context, userDataDir string) {
	if userDataDir == "" {
		return
	}

	dirName := filepath.Base(userDataDir)
	logger.Info(ctx, "[killChromeByUserDataDir] Killing Chrome processes using data dir: %s", dirName)

	if runtime.GOOS == "windows" {
		_ = m.killOrphanedChromeWindows(ctx, userDataDir, dirName)
	} else {
		_ = m.killOrphanedChromeUnix(ctx, userDataDir, dirName)
	}
}

// KillOrphanedChromeProcesses finds and kills Chrome processes that are using
// the configured user data directory. This is used as a last resort when lock
// files cannot be removed because a Chrome process from a previous session is
// still running and holding them.
func (m *Manager) KillOrphanedChromeProcesses(ctx context.Context) error {
	userDataDir := ""
	if m.config.Browser != nil {
		userDataDir = m.config.Browser.UserDataDir
	}
	return m.killOrphanedChromeForDir(ctx, userDataDir)
}

// killOrphanedChromeForDir kills orphaned Chrome processes for a specific user data directory.
// This is the core function that can be called with any userDataDir parameter.
func (m *Manager) killOrphanedChromeForDir(ctx context.Context, userDataDir string) error {
	if userDataDir == "" {
		return fmt.Errorf("user data dir is not configured")
	}

	// Use the base directory name for matching in process command lines.
	// e.g. "chrome_user_data" — safe for pattern matching, no special chars.
	dirName := filepath.Base(userDataDir)
	logger.Info(ctx, "[killOrphanedChrome] Looking for Chrome processes using data dir pattern: %s", dirName)

	if runtime.GOOS == "windows" {
		return m.killOrphanedChromeWindows(ctx, userDataDir, dirName)
	}
	return m.killOrphanedChromeUnix(ctx, userDataDir, dirName)
}

// killOrphanedChromeWindows kills orphaned Chrome processes on Windows.
func (m *Manager) killOrphanedChromeWindows(ctx context.Context, userDataDir, dirName string) error {
	// Step 1: Use WMIC to list all chrome.exe processes with their command lines.
	// WMIC output format (CSV): Node,CommandLine,ProcessId
	cmd := exec.Command("wmic", "process", "where", "name='chrome.exe'", "get", "processid,commandline", "/format:csv")
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Warn(ctx, "[killOrphanedChrome] WMIC query failed: %v (output: %s); skipping cleanup because the browser profile owner cannot be identified", err, strings.TrimSpace(string(output)))
		return nil
	}

	outputStr := string(output)
	logger.Info(ctx, "[killOrphanedChrome] WMIC returned %d bytes of process info", len(outputStr))

	// Step 2: Parse output to find PIDs whose command line contains our user data dir name.
	var killedPIDs []string
	scanner := bufio.NewScanner(strings.NewReader(outputStr))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "Node,") {
			continue
		}

		// Normalize slashes for matching (Chrome may use / or \)
		normalizedLine := strings.ReplaceAll(line, "/", "\\")
		if !strings.Contains(normalizedLine, dirName) && !strings.Contains(line, dirName) {
			continue
		}

		// Extract PID — last CSV field
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}
		pidStr := strings.TrimSpace(parts[len(parts)-1])
		if pidStr == "" || pidStr == "ProcessId" {
			continue
		}
		if _, err := strconv.Atoi(pidStr); err != nil {
			continue // not a valid PID
		}

		logger.Info(ctx, "[killOrphanedChrome] Found matching Chrome process PID: %s", pidStr)
		killCmd := exec.Command("taskkill", "/F", "/PID", pidStr)
		killOutput, killErr := killCmd.CombinedOutput()
		if killErr != nil {
			logger.Warn(ctx, "[killOrphanedChrome] Failed to kill PID %s: %v (%s)", pidStr, killErr, strings.TrimSpace(string(killOutput)))
		} else {
			killedPIDs = append(killedPIDs, pidStr)
			logger.Info(ctx, "[killOrphanedChrome] ✓ Killed Chrome PID: %s", pidStr)
		}
	}

	if len(killedPIDs) > 0 {
		logger.Info(ctx, "[killOrphanedChrome] Successfully killed %d Chrome process(es): %v", len(killedPIDs), killedPIDs)
		return nil
	}

	// A stale lockfile without an identified owner is not sufficient evidence to kill
	// unrelated Chrome windows that may belong to the user.
	lockPath := filepath.Join(userDataDir, "lockfile")
	if _, err := os.Stat(lockPath); err == nil {
		logger.Warn(ctx, "[killOrphanedChrome] No matching process found via WMIC, but lockfile exists; skipping cleanup because the owner could not be identified")
		return nil
	}

	logger.Info(ctx, "[killOrphanedChrome] No orphaned Chrome processes found")
	return nil
}

// killOrphanedChromeUnix kills orphaned Chrome processes on Linux/macOS.
func (m *Manager) killOrphanedChromeUnix(ctx context.Context, userDataDir, dirName string) error {
	// Try fuser to kill processes holding lock files
	for _, lockName := range []string{"SingletonLock", "lockfile"} {
		lockPath := filepath.Join(userDataDir, lockName)
		if _, err := os.Stat(lockPath); err != nil {
			continue
		}
		cmd := exec.Command("fuser", "-k", lockPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			logger.Warn(ctx, "[killOrphanedChrome] fuser -k %s failed: %v (%s)", lockName, err, strings.TrimSpace(string(output)))
		} else {
			logger.Info(ctx, "[killOrphanedChrome] ✓ Killed process holding %s", lockName)
		}
	}

	// Also try pkill with matching pattern
	cmd := exec.Command("pkill", "-f", fmt.Sprintf("chrome.*%s", dirName))
	if output, err := cmd.CombinedOutput(); err != nil {
		logger.Info(ctx, "[killOrphanedChrome] pkill result: %v (%s)", err, strings.TrimSpace(string(output)))
	}

	return nil
}

// getInstanceBrowser 获取指定实例的浏览器和活动页面
// 如果 instanceID 为空，则使用当前实例
// 如果 default 实例未运行，会自动启动它
// 返回: browser, activePage, instance, error
func (m *Manager) getInstanceBrowser(instanceID string) (*rod.Browser, *rod.Page, *models.BrowserInstance, error) {
	// 如果没有指定实例ID，使用当前实例
	if instanceID == "" {
		instanceID = m.currentInstanceID
	}

	// 如果还是空，说明没有运行中的实例
	if instanceID == "" {
		// 向后兼容：检查旧的 browser 字段
		if m.isRunning && m.browser != nil {
			return m.browser, m.activePage, nil, nil
		}

		// 尝试使用 default 实例
		instanceID = "default"
		ctx := context.Background()
		logger.Info(ctx, "No current instance, attempting to use default instance")
	}

	// 获取实例运行时信息
	runtime, exists := m.instances[instanceID]
	if !exists || runtime == nil {
		// 如果是 default 实例且未运行，尝试自动启动
		if instanceID == "default" {
			ctx := context.Background()
			logger.Info(ctx, "Default instance not running, attempting to auto-start...")

			// 调用内部启动函数（调用者已持有锁）
			err := m.startInstanceInternal(ctx, "default")
			if err != nil {
				logger.Error(ctx, "Failed to auto-start default instance: %v", err)
				return nil, nil, nil, fmt.Errorf("default instance not running and failed to start: %w", err)
			}

			logger.Info(ctx, "✓ Default instance auto-started successfully")

			// 重新获取运行时信息
			runtime, exists = m.instances["default"]
			if !exists || runtime == nil {
				return nil, nil, nil, fmt.Errorf("default instance started but runtime not found")
			}

			return runtime.browser, runtime.activePage, runtime.instance, nil
		}

		return nil, nil, nil, fmt.Errorf("instance %s is not running", instanceID)
	}

	return runtime.browser, runtime.activePage, runtime.instance, nil
}

// setInstanceActivePage 设置指定实例的活动页面
func (m *Manager) setInstanceActivePage(instanceID string, page *rod.Page) error {
	// 如果没有指定实例ID，使用当前实例
	if instanceID == "" {
		instanceID = m.currentInstanceID
	}

	// 如果还是空，说明没有运行中的实例
	if instanceID == "" {
		// 向后兼容：设置旧的 activePage 字段
		if m.isRunning && m.browser != nil {
			m.activePage = page
			return nil
		}
		return fmt.Errorf("no running instance available")
	}

	// 获取实例运行时信息
	runtime, exists := m.instances[instanceID]
	if !exists || runtime == nil {
		return fmt.Errorf("instance %s is not running", instanceID)
	}

	runtime.activePage = page

	// 如果是当前实例，也更新向后兼容的字段
	if instanceID == m.currentInstanceID {
		m.activePage = page
	}

	return nil
}

// StartInstance 启动指定浏览器实例
func (m *Manager) StartInstance(ctx context.Context, instanceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.startInstanceInternal(ctx, instanceID)
}

func (m *Manager) startInstanceInternal(ctx context.Context, instanceID string) error {
	return m.startInstanceInternalWithOptions(ctx, instanceID, startInstanceOptions{openStartupPage: true})
}

func (m *Manager) clearInstanceRuntimeLocked(ctx context.Context, instanceID string) {
	runtime, exists := m.instances[instanceID]
	if !exists || runtime == nil {
		return
	}
	m.releaseRuntimeReceiptsForInstanceLocked(instanceID)

	if runtime.launcher != nil {
		runtime.launcher.Kill()
	}
	if runtime.instance != nil {
		runtime.instance.IsActive = false
		runtime.instance.UpdatedAt = time.Now()
		if err := m.db.SaveBrowserInstance(runtime.instance); err != nil {
			logger.Warn(ctx, "Failed to update instance status after connection reset: %v", err)
		}
	}

	delete(m.instances, instanceID)
	if m.currentInstanceID == instanceID {
		m.currentInstanceID = ""
		m.browser = nil
		m.launcher = nil
		m.isRunning = false
		m.activePage = nil
		m.startTime = time.Time{}
	}
}

// finalizeStoppedCurrentInstanceRuntimeLocked removes a runtime whose browser
// process has already stopped and keeps the legacy current-instance mirror in
// sync. The caller must hold m.mu.
func (m *Manager) finalizeStoppedCurrentInstanceRuntimeLocked(ctx context.Context, instanceID string) {
	m.releaseRuntimeReceiptsForInstanceLocked(instanceID)
	runtime := m.instances[instanceID]
	if runtime != nil {
		if runtime.instance != nil {
			runtime.instance.IsActive = false
			runtime.instance.UpdatedAt = time.Now()
			if m.db != nil {
				if err := m.db.SaveBrowserInstance(runtime.instance); err != nil {
					logger.Warn(ctx, "Failed to update instance status after stop: %v", err)
				}
			}
		}
		delete(m.instances, instanceID)
	}

	if m.currentInstanceID != instanceID {
		return
	}

	m.currentInstanceID = ""
	m.browser = nil
	m.launcher = nil
	m.isRunning = false
	m.activePage = nil
	m.startTime = time.Time{}

	for id, candidate := range m.instances {
		if candidate == nil || candidate.browser == nil {
			continue
		}
		m.currentInstanceID = id
		m.browser = candidate.browser
		m.launcher = candidate.launcher
		m.isRunning = true
		m.activePage = candidate.activePage
		m.startTime = candidate.startTime
		return
	}
}

func (m *Manager) releaseRuntimeReceiptsForInstanceLocked(instanceID string) {
	registry := m.recordingRuntimeRegistryLocked()
	// Create the lease-lost tombstone before deleting any active runtime facts.
	// The Coordinator owns the later DB decision; this registry only preserves
	// the exact runtime observation long enough for it to be acknowledged.
	for activeInstanceID, scope := range registry.activeByInstance {
		if activeInstanceID != instanceID {
			continue
		}
		event := RecordingRuntimeEvent{
			ID:        deterministicRuntimeEventID("runtime_lease_lost", scope, 0, scope.LeaseGeneration),
			Kind:      "runtime_lease_lost",
			Scope:     scope,
			ReceiptID: scope.LeaseGeneration,
		}
		if draft := registry.draftBySession[scope.RecordingSessionID]; draft != nil {
			event.SyncRevision = draft.event.SyncRevision
			event.Actions = append([]models.ScriptAction(nil), draft.event.Actions...)
			event.DOMSnapshot = append(json.RawMessage(nil), draft.event.DOMSnapshot...)
		}
		registry.leaseLostBySession[scope.RecordingSessionID] = cloneRecordingRuntimeEvent(event)
		delete(registry.activeByInstance, activeInstanceID)
		delete(registry.draftBySession, scope.RecordingSessionID)
		if reservation := registry.startBySession[scope.RecordingSessionID]; reservation != nil {
			if reservation.cancel != nil {
				reservation.cancel()
			}
			if reservation.done != nil {
				reservation.doneOnce.Do(func() { close(reservation.done) })
			}
			delete(registry.startBySession, scope.RecordingSessionID)
		}
		m.emitRuntimeEventLocked(event)
	}
	for sessionID, reservation := range registry.startBySession {
		if reservation != nil && reservation.scope.BrowserInstanceID == instanceID {
			if _, exists := registry.leaseLostBySession[sessionID]; !exists {
				event := RecordingRuntimeEvent{ID: deterministicRuntimeEventID("runtime_lease_lost", reservation.scope, 0, reservation.scope.LeaseGeneration), Kind: "runtime_lease_lost", Scope: reservation.scope, ReceiptID: reservation.scope.LeaseGeneration}
				registry.leaseLostBySession[sessionID] = cloneRecordingRuntimeEvent(event)
				m.emitRuntimeEventLocked(event)
			}
			if reservation.cancel != nil {
				reservation.cancel()
			}
			if reservation.done != nil {
				reservation.doneOnce.Do(func() { close(reservation.done) })
			}
			delete(registry.startBySession, sessionID)
		}
	}
	for sessionID, receipt := range registry.finalBySession {
		if receipt.scope.BrowserInstanceID == instanceID {
			// A page-level Stop removes the active lease before the coordinator
			// commits its final receipt.  Instance shutdown must preserve that
			// final draft as a lease-lost tombstone instead of deleting the only
			// recovery evidence.
			if _, exists := registry.leaseLostBySession[sessionID]; !exists {
				event := RecordingRuntimeEvent{
					ID:           deterministicRuntimeEventID("runtime_lease_lost", receipt.scope, receipt.syncRevision, receipt.receiptID),
					Kind:         "runtime_lease_lost",
					Scope:        receipt.scope,
					ReceiptID:    receipt.receiptID,
					SyncRevision: receipt.syncRevision,
					Actions:      append([]models.ScriptAction(nil), receipt.actions...),
					DOMSnapshot:  append(json.RawMessage(nil), receipt.domSnapshot...),
				}
				registry.leaseLostBySession[sessionID] = cloneRecordingRuntimeEvent(event)
				m.emitRuntimeEventLocked(event)
			}
			receipt.state = recordingReceiptReleased
			delete(registry.finalBySession, sessionID)
		}
	}
	for sessionID, receipt := range registry.authBySession {
		if receipt.scope.BrowserInstanceID == instanceID {
			receipt.state = recordingReceiptReleased
			delete(registry.authBySession, sessionID)
		}
	}
	if recorder := m.recorders[instanceID]; recorder != nil {
		if recorder.IsRecording() {
			_, _, _ = recorder.StopRecording(context.Background())
		}
		delete(m.recorders, instanceID)
		if instanceID == "default" && m.recorder == recorder {
			m.recorder = nil
		}
	}
}

// startInstanceInternalWithOptions 内部启动函数，调用者必须已持有锁。
func (m *Manager) startInstanceInternalWithOptions(ctx context.Context, instanceID string, opts startInstanceOptions) error {
	// 空 instanceID 回退：优先使用当前实例，否则查找默认实例
	if instanceID == "" {
		if m.currentInstanceID != "" {
			instanceID = m.currentInstanceID
		} else {
			defaultInst, err := m.db.GetDefaultBrowserInstance()
			if err != nil {
				return fmt.Errorf("no instance ID specified and no default instance found: %w", err)
			}
			instanceID = defaultInst.ID
			logger.Info(ctx, "No instance ID specified, using default instance: %s (%s)", defaultInst.Name, defaultInst.ID)
		}
	}

	// 检查实例是否已启动
	if runtime, exists := m.instances[instanceID]; exists && runtime != nil {
		m.currentInstanceID = instanceID
		m.browser = runtime.browser
		m.launcher = runtime.launcher
		m.isRunning = true
		m.startTime = runtime.startTime
		m.activePage = runtime.activePage
		return nil
	}

	// 从数据库加载实例配置
	instance, err := m.db.GetBrowserInstance(instanceID)
	if err != nil {
		return fmt.Errorf("failed to load instance: %w", err)
	}

	logger.Info(ctx, "Starting browser instance: %s (%s)", instance.Name, instance.Type)

	// 加载默认配置和站点配置（如果还未加载）
	if m.defaultBrowserConfig == nil {
		defaultConfig, err := m.db.GetDefaultBrowserConfig()
		if err != nil {
			logger.Warn(ctx, "Default browser configuration not found, using system defaults")
			defaultConfig = m.getDefaultBrowserConfig()
		}
		m.defaultBrowserConfig = defaultConfig
		logger.Info(ctx, "Loaded default browser configuration: %s", defaultConfig.Name)
	}

	if m.siteConfigs == nil {
		// 加载所有网站特定配置
		allConfigs, err := m.db.ListBrowserConfigs()
		if err != nil {
			logger.Warn(ctx, "Failed to load site configurations: %v", err)
			m.siteConfigs = []*models.BrowserConfig{}
		} else {
			// 过滤出有URL模式的配置
			m.siteConfigs = []*models.BrowserConfig{}
			for i := range allConfigs {
				if allConfigs[i].URLPattern != "" && !allConfigs[i].IsDefault {
					m.siteConfigs = append(m.siteConfigs, &allConfigs[i])
				}
			}
			logger.Info(ctx, "Loaded %d site-specific configurations", len(m.siteConfigs))
		}
	}

	var browser *rod.Browser
	var launcherObj *launcher.Launcher
	var url string
	var proxyUsername, proxyPassword string // 代理认证信息
	var localUserDataDir string

	if instance.Type == "remote" {
		// 远程模式
		if instance.ControlURL == "" {
			return fmt.Errorf("control_url is required for remote instance")
		}
		controlURL := instance.ControlURL
		logger.Info(ctx, "Connecting to remote browser: %s", controlURL)

		// 解析 WebSocket URL
		wsURL, err := resolveWebSocketURL(controlURL)
		if err != nil {
			return fmt.Errorf("failed to resolve WebSocket URL from %s: %w", controlURL, err)
		}
		logger.Info(ctx, "Resolved WebSocket URL: %s", wsURL)
		url = wsURL

		browser = rod.New().ControlURL(url)
	} else {
		// 本地模式
		logger.Info(ctx, "Starting local browser instance...")

		// 创建启动器
		headless := false
		if instance.Headless != nil {
			headless = *instance.Headless
		}

		l := launcher.New().
			Headless(headless).
			Devtools(false).
			Leakless(false)
		if !headless && opts.openStartupPage {
			l = l.Delete("no-startup-window").StartURL("about:blank")
		}

		// NoSandbox 配置：优先使用实例配置，否则回退到默认配置
		noSandbox := instance.NoSandbox
		if noSandbox == nil && m.defaultBrowserConfig != nil {
			noSandbox = m.defaultBrowserConfig.NoSandbox
		}
		if noSandbox != nil && *noSandbox {
			l = l.NoSandbox(true)
			logger.Info(ctx, "NoSandbox enabled by configuration")
		}

		// 设置代理
		if instance.Proxy != "" {
			// 解析代理 URL，提取认证信息
			proxyAddr, username, password, err := parseProxyURL(instance.Proxy)
			if err != nil {
				logger.Warn(ctx, "Failed to parse proxy URL: %v", err)
			} else {
				// 设置代理地址（不包含用户名密码）
				l = l.Proxy(proxyAddr)
				proxyUsername = username
				proxyPassword = password

				if username != "" {
					logger.Info(ctx, "Using proxy: %s (with authentication)", proxyAddr)
				} else {
					logger.Info(ctx, "Using proxy: %s", proxyAddr)
				}
			}
		}

		// 设置启动参数
		launchArgs := instance.LaunchArgs
		if len(launchArgs) == 0 {
			// 使用默认启动参数
			launchArgs = []string{
				"disable-blink-features=AutomationControlled",
				"no-first-run",
				"no-default-browser-check",
				"window-size=1920,1080",
				"start-maximized",
			}
		}

		// headless 模式下追加防止后台节流的关键参数
		if headless {
			headlessArgs := []string{
				"disable-background-timer-throttling",
				"disable-backgrounding-occluded-windows",
				"disable-renderer-backgrounding",
				"disable-ipc-flooding-protection",
				"enable-features=NetworkService,NetworkServiceInProcess",
			}
			launchArgs = append(launchArgs, headlessArgs...)
			logger.Info(ctx, "Headless mode detected, added anti-throttling flags")
		}

		for _, arg := range launchArgs {
			l = applyChromiumLaunchArg(l, arg)
		}

		// 设置浏览器路径
		binPath := instance.BinPath
		if binPath == "" {
			// 如果没有指定路径，尝试查找系统中的 Chrome/Edge
			logger.Info(ctx, "BinPath not specified, searching for local Chrome/Edge...")
			binPath = findLocalChromiumBinary()
			if binPath != "" {
				logger.Info(ctx, "Found local Chrome/Edge at: %s", binPath)
			}

			// 如果配置文件中有指定路径，优先使用
			if m.config.Browser != nil && m.config.Browser.BinPath != "" {
				binPath = m.config.Browser.BinPath
				logger.Info(ctx, "Using browser path from config: %s", binPath)
			}
		}

		if binPath != "" {
			l = l.Bin(binPath)
			logger.Info(ctx, "Using browser path: %s", binPath)
		} else {
			logger.Warn(ctx, "No browser path found, will use launcher default (may download Chrome)")
		}

		// 设置用户数据目录
		if instance.UserDataDir != "" {
			if shouldUseEdgeDefaultUserDataDir(binPath, instance.UserDataDir) {
				instance.UserDataDir = "./edge_user_data"
				logger.Info(ctx, "Using Edge-specific user data directory: %s", instance.UserDataDir)
			}
			if err := os.MkdirAll(instance.UserDataDir, 0o755); err != nil {
				logger.Warn(ctx, "Failed to create user data directory: %v", err)
			} else {
				// 主动清理可能存在的锁文件（在启动前）
				logger.Info(ctx, "Checking and cleaning up lock files before launch...")
				if terminated, err := m.killTrackedChromeProfileOwner(ctx, instance.UserDataDir); err != nil {
					logger.Warn(ctx, "Failed to terminate tracked Chrome profile owner for instance: %v", err)
				} else if terminated {
					logger.Info(ctx, "Terminated the tracked Chrome profile owner before instance launch")
				}
				// 首先尝试杀死可能存在的孤儿 Chrome 进程（重启后残留的进程）
				if err := m.killOrphanedChromeForDir(ctx, instance.UserDataDir); err != nil {
					logger.Warn(ctx, "Failed to kill orphaned Chrome processes for instance: %v", err)
				}
				// 然后清理锁文件
				if err := m.cleanupSingletonLock(ctx, instance.UserDataDir); err != nil {
					logger.Warn(ctx, "Failed to cleanup singleton lock: %v", err)
				}

				// 检查锁文件是否仍然存在（说明有进程正在持有它们）
				if m.lockFilesStillExist(instance.UserDataDir) {
					logger.Warn(ctx, "Lock files still exist after cleanup, killing orphaned Chrome processes...")
					m.killChromeByUserDataDir(ctx, instance.UserDataDir)
					time.Sleep(1 * time.Second)
					m.cleanupSingletonLock(ctx, instance.UserDataDir)
				}

				l = l.UserDataDir(instance.UserDataDir)
				localUserDataDir = instance.UserDataDir
				logger.Info(ctx, "Using user data directory: %s", instance.UserDataDir)
			}
		}

		// 启动浏览器（失败后自动重试一次）
		logger.Info(ctx, "[startInstanceInternal] Launching Chrome...")
		url, err = l.Launch()
		if err != nil {
			if isBrowserBinaryNotFound(err) {
				logger.Error(ctx, "[startInstanceInternal] Failed to find local Chrome/Edge browser: %v", err)
				return fmt.Errorf("failed to find local Chrome/Edge browser binary; configure browser.bin_path: %w", err)
			}
			logger.Error(ctx, "[startInstanceInternal] Chrome launch failed: %v, attempting to kill orphaned processes and retry...", err)

			// 杀死残留的 Chrome 进程并清理锁文件
			if instance.UserDataDir != "" {
				m.killChromeByUserDataDir(ctx, instance.UserDataDir)
			} else {
				m.KillOrphanedChromeProcesses(ctx)
			}
			time.Sleep(2 * time.Second)
			if instance.UserDataDir != "" {
				m.cleanupSingletonLock(ctx, instance.UserDataDir)
			}

			// 重试启动
			logger.Info(ctx, "[startInstanceInternal] Retrying Chrome launch...")
			l = cloneLauncherForRetry(l)
			url, err = l.Launch()
			if err != nil {
				logger.Error(ctx, "[startInstanceInternal] Chrome launch failed on retry: %v", err)
				return fmt.Errorf("failed to launch browser (tried killing orphaned processes): %w", err)
			}
			logger.Info(ctx, "[startInstanceInternal] ✓ Chrome launched successfully on retry")
		}
		if err := persistLaunchedBrowserProfileOwner(ctx, l, localUserDataDir, url); err != nil {
			return err
		}

		browser = rod.New().ControlURL(url)
		launcherObj = l
		logger.Info(ctx, "Browser launched: %s", url)
	}

	// 连接浏览器
	if err := browser.Connect(); err != nil {
		if launcherObj != nil {
			launcherObj.Kill()
		}
		return fmt.Errorf("failed to connect browser: %w", err)
	}

	// 如果代理需要认证，启动认证处理
	if proxyUsername != "" && proxyPassword != "" {
		logger.Info(ctx, "Setting up proxy authentication handler...")
		go browser.HandleAuth(proxyUsername, proxyPassword)()
	}

	// 关键：在浏览器连接后立即设置XHR拦截器，确保所有页面（包括后续打开的）都会自动监听XHR
	// 这样用户在点击"开始录制"之前打开的页面，也能捕获到XHR请求
	logger.Info(ctx, "Setting up XHR interceptor for all pages...")

	// 获取所有现有页面并注入XHR拦截器
	pages, err := browser.Pages()
	if err == nil {
		for _, page := range pages {
			if page != nil {
				// 为每个现有页面设置EvalOnNewDocument
				_, evalErr := page.EvalOnNewDocument(xhrInterceptorScriptForManager)
				if evalErr != nil {
					logger.Warn(ctx, "Failed to set EvalOnNewDocument for existing page: %v", evalErr)
				}

				// 立即在现有页面注入（以防页面已经加载）
				_, injectErr := page.Eval(`() => { ` + xhrInterceptorScriptForManager + ` return true; }`)
				if injectErr != nil {
					logger.Warn(ctx, "Failed to inject XHR interceptor into existing page: %v", injectErr)
				}
			}
		}
		logger.Info(ctx, "✓ XHR interceptor configured for %d existing pages", len(pages))
	}

	// 为浏览器设置默认的EvalOnNewDocument（影响所有新页面）
	// 注意：rod目前没有直接在Browser级别设置EvalOnNewDocument的API
	// 所以我们需要在页面创建时处理，见下面的页面监听逻辑

	logger.Info(ctx, "✓ XHR interceptor setup completed")

	// 设置下载行为
	if m.downloadPath == "" {
		downloadPath := "./downloads"
		absDownloadPath, err := os.Getwd()
		if err == nil {
			downloadPath = absDownloadPath + "/downloads"
		}
		os.MkdirAll(downloadPath, 0o755)
		m.downloadPath = downloadPath
		for _, recorder := range m.recorders {
			if recorder != nil {
				recorder.SetDownloadPath(downloadPath)
			}
		}
		if m.recorder != nil {
			m.recorder.SetDownloadPath(downloadPath)
		}
	}

	downloadBehavior := &proto.BrowserSetDownloadBehavior{
		Behavior:      proto.BrowserSetDownloadBehaviorBehaviorAllow,
		DownloadPath:  m.downloadPath,
		EventsEnabled: true,
	}
	if err := downloadBehavior.Call(browser); err != nil {
		logger.Warn(ctx, "Failed to set download behavior: %v", err)
	}

	// 授予剪贴板权限
	grantPermissions := &proto.BrowserGrantPermissions{
		Permissions: []proto.BrowserPermissionType{
			proto.BrowserPermissionTypeClipboardReadWrite,
			proto.BrowserPermissionTypeClipboardSanitizedWrite,
		},
	}
	if err := grantPermissions.Call(browser); err != nil {
		logger.Warn(ctx, "Failed to grant clipboard permissions: %v", err)
	}

	if err := checkBrowserConnection(browser); err != nil {
		if launcherObj != nil {
			launcherObj.Kill()
		}
		return fmt.Errorf("browser connection closed after launch: %w", err)
	}

	// 创建运行时信息
	runtime := &BrowserInstanceRuntime{
		instance:  instance,
		browser:   browser,
		launcher:  launcherObj,
		startTime: time.Now(),
	}

	m.instances[instanceID] = runtime

	// 更新实例状态为运行中
	instance.IsActive = true
	instance.UpdatedAt = time.Now()
	if err := m.db.SaveBrowserInstance(instance); err != nil {
		logger.Warn(ctx, "Failed to update instance status: %v", err)
	}

	// 如果是第一个启动的实例或者是默认实例，设置为当前实例
	if m.currentInstanceID == "" || instance.IsDefault {
		m.currentInstanceID = instanceID
	}

	// 如果启动的是当前实例，更新向后兼容的旧字段
	if m.currentInstanceID == instanceID {
		m.browser = browser
		m.launcher = launcherObj
		m.isRunning = true
		m.startTime = runtime.startTime
	}

	// 启动新页面监听，自动为新打开的页面注入XHR拦截器
	go m.watchForNewPagesXHR(ctx, browser, instanceID)

	logger.Info(ctx, "✓ Browser instance started: %s", instance.Name)
	return nil
}

// watchForNewPagesXHR 监听新页面创建并自动注入XHR拦截器
// 这确保了用户在点击"开始录制"之前打开的所有页面都能捕获XHR请求
func (m *Manager) watchForNewPagesXHR(ctx context.Context, browser *rod.Browser, instanceID string) {
	logger.Info(ctx, "Starting XHR interceptor watcher for instance: %s", instanceID)

	// 记录已处理的页面
	processedPages := make(map[string]bool)

	// 定时检查新页面（每秒检查一次）
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// 检查实例是否还在运行
			m.mu.Lock()
			runtime, exists := m.instances[instanceID]
			m.mu.Unlock()

			if !exists || runtime == nil {
				logger.Info(ctx, "Instance %s stopped, stopping XHR watcher", instanceID)
				return
			}

			// 获取所有页面
			pages, err := browser.Pages()
			if err != nil {
				continue
			}

			// 为每个新页面注入XHR拦截器
			for _, page := range pages {
				if page == nil {
					continue
				}

				pageInfo, err := page.Info()
				if err != nil {
					continue
				}

				targetID := string(pageInfo.TargetID)

				// 跳过已处理的页面
				if processedPages[targetID] {
					continue
				}

				// 跳过特殊页面
				if strings.HasPrefix(pageInfo.URL, "chrome://") ||
					strings.HasPrefix(pageInfo.URL, "chrome-extension://") ||
					strings.HasPrefix(pageInfo.URL, "devtools://") ||
					strings.HasPrefix(pageInfo.URL, "about:") {
					processedPages[targetID] = true
					continue
				}

				// 标记为已处理
				processedPages[targetID] = true

				logger.Info(ctx, "Injecting XHR interceptor into new page: %s (URL: %s)", targetID, pageInfo.URL)

				// 为新页面设置EvalOnNewDocument（影响该页面内的iframe和导航）
				_, evalErr := page.EvalOnNewDocument(xhrInterceptorScriptForManager)
				if evalErr != nil {
					logger.Warn(ctx, "Failed to set EvalOnNewDocument for page %s: %v", targetID, evalErr)
				}

				// 立即在页面注入（以防页面已经加载）
				go func(p *rod.Page, tid string) {
					_, injectErr := p.Eval(`() => { ` + xhrInterceptorScriptForManager + ` return true; }`)
					if injectErr != nil {
						logger.Warn(ctx, "Failed to inject XHR interceptor into page %s: %v", tid, injectErr)
					} else {
						logger.Info(ctx, "✓ XHR interceptor injected into page: %s", tid)
					}
				}(page, targetID)
			}
		case <-ctx.Done():
			logger.Info(ctx, "Context cancelled, stopping XHR watcher for instance: %s", instanceID)
			return
		}
	}
}

// StopInstance 停止指定浏览器实例
func (m *Manager) StopInstance(ctx context.Context, instanceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	runtime, exists := m.instances[instanceID]
	if !exists || runtime == nil {
		return fmt.Errorf("instance %s is not running", instanceID)
	}

	logger.Info(ctx, "Stopping browser instance: %s", runtime.instance.Name)

	isRemote := runtime.instance.Type == "remote"

	// 关闭浏览器
	if runtime.browser != nil {
		if !isRemote {
			// 关闭所有页面
			if pages, err := runtime.browser.Pages(); err == nil {
				for _, page := range pages {
					_ = page.Close()
				}
			}
			time.Sleep(1 * time.Second)
		}

		if err := runtime.browser.Close(); err != nil {
			logger.Warn(ctx, "Error closing browser: %v", err)
		}
	}

	// 终止本地浏览器进程
	if !isRemote && runtime.launcher != nil {
		time.Sleep(1 * time.Second)
		runtime.launcher.Kill()
		logger.Info(ctx, "Browser process terminated")
	}

	// 清理本地实例的锁文件
	if !isRemote && runtime.instance.UserDataDir != "" {
		// 等待浏览器完全退出
		time.Sleep(500 * time.Millisecond)

		if err := m.cleanupSingletonLock(ctx, runtime.instance.UserDataDir); err != nil {
			logger.Warn(ctx, "Failed to cleanup singleton lock after stop: %v", err)
		} else {
			logger.Info(ctx, "✓ Cleaned up singleton lock files for stopped instance")
		}
	}

	m.finalizeStoppedCurrentInstanceRuntimeLocked(ctx, instanceID)

	logger.Info(ctx, "✓ Browser instance stopped: %s", runtime.instance.Name)
	return nil
}

// SwitchInstance 切换当前活动实例
func (m *Manager) SwitchInstance(ctx context.Context, instanceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 从数据库获取实例信息，验证实例是否存在
	instance, err := m.db.GetBrowserInstance(instanceID)
	if err != nil {
		return fmt.Errorf("instance %s not found: %w", instanceID, err)
	}

	// 设置当前实例ID（无论实例是否运行）
	m.currentInstanceID = instanceID

	// 检查实例是否运行
	runtime, exists := m.instances[instanceID]
	if exists && runtime != nil {
		// 实例正在运行，更新旧字段以保持向后兼容
		m.browser = runtime.browser
		m.launcher = runtime.launcher
		m.isRunning = true
		m.startTime = runtime.startTime
		m.activePage = runtime.activePage
		logger.Info(ctx, "Switched to running instance: %s", instance.Name)
	} else {
		// 实例未运行，清空旧字段
		m.browser = nil
		m.launcher = nil
		m.isRunning = false
		m.startTime = time.Time{}
		m.activePage = nil
		logger.Info(ctx, "Switched to stopped instance: %s (not running)", instance.Name)
	}

	return nil
}

// GetCurrentInstance 获取当前活动实例
func (m *Manager) GetCurrentInstance() *models.BrowserInstance {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.currentInstanceID == "" {
		return nil
	}

	// 首先尝试从运行时获取（如果实例正在运行）
	runtime, exists := m.instances[m.currentInstanceID]
	if exists && runtime != nil {
		return runtime.instance
	}

	// 如果实例未运行，从数据库获取
	instance, err := m.db.GetBrowserInstance(m.currentInstanceID)
	if err != nil {
		return nil
	}

	return instance
}

// GetInstanceRuntime 获取指定实例的运行时信息
func (m *Manager) GetInstanceRuntime(instanceID string) (*BrowserInstanceRuntime, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	runtime, exists := m.instances[instanceID]
	if !exists || runtime == nil {
		return nil, fmt.Errorf("instance %s is not running", instanceID)
	}

	return runtime, nil
}

// ListRunningInstances 列出所有运行中的实例
func (m *Manager) ListRunningInstances() []*models.BrowserInstance {
	m.mu.Lock()
	defer m.mu.Unlock()

	var instances []*models.BrowserInstance
	for _, runtime := range m.instances {
		if runtime != nil && runtime.instance != nil {
			instances = append(instances, runtime.instance)
		}
	}

	return instances
}
