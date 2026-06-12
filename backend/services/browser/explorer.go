package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/pkg/logger"
	"github.com/browserwing/browserwing/storage"
	"github.com/go-rod/rod"
	"github.com/google/uuid"
)

// ExploreEvent represents a real-time event sent to the frontend during exploration
type ExploreEvent struct {
	Type    string      `json:"type"` // progress, thinking, tool_call, error, script_ready, done
	Content string      `json:"content,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// ExploreSession represents a single AI exploration session
type ExploreSession struct {
	ID              string            `json:"id"`
	TaskDesc        string            `json:"task_desc"`
	StartURL        string            `json:"start_url"`
	LLMConfigID     string            `json:"llm_config_id"`
	InstanceID      string            `json:"instance_id"`
	Status          string            `json:"status"` // running, completed, failed, stopped
	StartTime       time.Time         `json:"start_time"`
	EndTime         time.Time         `json:"end_time,omitempty"`
	RawOps          []models.OpRecord `json:"raw_ops,omitempty"`
	GeneratedScript *models.Script    `json:"generated_script,omitempty"`
	Error           string            `json:"error,omitempty"`
	StreamChan      chan ExploreEvent `json:"-"`
	cancelFunc      context.CancelFunc
}

// ExecutorRecorderInterface abstracts the executor's recording capabilities
type ExecutorRecorderInterface interface {
	StartRecordMode()
	StopRecordModeAsOpRecords() []models.OpRecord
	GetRecordedOpsAsOpRecords() []models.OpRecord
	NavigateForExplore(ctx context.Context, url string) error
}

// Explorer manages AI-driven browser exploration sessions
type Explorer struct {
	mu             sync.RWMutex
	sessions       map[string]*ExploreSession
	browserManager *Manager
	agentManager   AgentManagerInterface
	execRecorder   ExecutorRecorderInterface
	db             storage.Store
}

// NewExplorer creates a new Explorer instance
func NewExplorer(browserMgr *Manager, db storage.Store) *Explorer {
	return &Explorer{
		sessions:       make(map[string]*ExploreSession),
		browserManager: browserMgr,
		db:             db,
	}
}

// SetAgentManager sets the agent manager reference
func (exp *Explorer) SetAgentManager(agentMgr AgentManagerInterface) {
	exp.agentManager = agentMgr
}

// SetExecutorRecorder sets the executor recorder interface
func (exp *Explorer) SetExecutorRecorder(rec ExecutorRecorderInterface) {
	exp.execRecorder = rec
}

// StartExploration begins a new AI exploration session
func (exp *Explorer) StartExploration(ctx context.Context, taskDesc, startURL, llmConfigID, instanceID string) (*ExploreSession, error) {
	if exp.agentManager == nil {
		return nil, fmt.Errorf("agent manager is not available")
	}
	if taskDesc == "" {
		return nil, fmt.Errorf("task description is required")
	}

	sessionID := uuid.New().String()
	exploreCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)

	session := &ExploreSession{
		ID:          sessionID,
		TaskDesc:    taskDesc,
		StartURL:    startURL,
		LLMConfigID: llmConfigID,
		InstanceID:  instanceID,
		Status:      "running",
		StartTime:   time.Now(),
		StreamChan:  make(chan ExploreEvent, 200),
		cancelFunc:  cancel,
	}

	exp.mu.Lock()
	exp.sessions[sessionID] = session
	exp.mu.Unlock()

	go exp.runExploration(exploreCtx, session)

	return session, nil
}

// GetSession returns an exploration session by ID
func (exp *Explorer) GetSession(sessionID string) (*ExploreSession, bool) {
	exp.mu.RLock()
	defer exp.mu.RUnlock()
	s, ok := exp.sessions[sessionID]
	return s, ok
}

// StopExploration stops a running exploration session
func (exp *Explorer) StopExploration(sessionID string) error {
	exp.mu.RLock()
	session, ok := exp.sessions[sessionID]
	exp.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	if session.Status != "running" {
		return fmt.Errorf("session is not running: %s", session.Status)
	}

	session.cancelFunc()
	session.Status = "stopped"
	session.EndTime = time.Now()

	safeSend(session.StreamChan, ExploreEvent{Type: "done", Content: "Exploration stopped by user"})

	return nil
}

// runExploration is the core exploration goroutine
func (exp *Explorer) runExploration(ctx context.Context, session *ExploreSession) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error(ctx, "[Explorer] Panic in session %s: %v", session.ID, r)
			session.Status = "failed"
			session.Error = fmt.Sprintf("panic: %v", r)
			session.EndTime = time.Now()
			safeSend(session.StreamChan, ExploreEvent{Type: "error", Content: session.Error})
			safeSend(session.StreamChan, ExploreEvent{Type: "done"})
		}
	}()

	logger.Info(ctx, "[Explorer] Starting session %s: task=%s, url=%s", session.ID, session.TaskDesc, session.StartURL)
	safeSend(session.StreamChan, ExploreEvent{Type: "progress", Content: "Starting exploration..."})

	// Ensure browser instance is running before exploration.
	// We must use the multi-instance API (StartInstance) instead of the legacy Start()
	// to avoid conflicts between the legacy browser fields and the instance system.
	if err := exp.ensureBrowserRunning(ctx, session); err != nil {
		logger.Error(ctx, "[Explorer] Failed to ensure browser is running: %v", err)
		session.Status = "failed"
		session.Error = fmt.Sprintf("Failed to start browser: %v", err)
		session.EndTime = time.Now()
		safeSend(session.StreamChan, ExploreEvent{Type: "error", Content: session.Error})
		safeSend(session.StreamChan, ExploreEvent{Type: "done"})
		return
	}

	// Enable recording mode on the executor
	if exp.execRecorder != nil {
		exp.execRecorder.StartRecordMode()
		defer func() {
			ops := exp.execRecorder.StopRecordModeAsOpRecords()
			if session.RawOps == nil || len(ops) > len(session.RawOps) {
				session.RawOps = ops
			}
			logger.Info(ctx, "[Explorer] Recorded %d raw operations", len(session.RawOps))
		}()
	}

	// Navigate to start URL if provided
	if session.StartURL != "" {
		safeSend(session.StreamChan, ExploreEvent{Type: "progress", Content: fmt.Sprintf("Navigating to %s...", session.StartURL)})

		if exp.execRecorder != nil {
			if err := exp.execRecorder.NavigateForExplore(ctx, session.StartURL); err != nil {
				logger.Error(ctx, "[Explorer] Navigation failed: %v", err)
				session.Status = "failed"
				session.Error = fmt.Sprintf("Failed to navigate: %v", err)
				session.EndTime = time.Now()
				safeSend(session.StreamChan, ExploreEvent{Type: "error", Content: session.Error})
				safeSend(session.StreamChan, ExploreEvent{Type: "done"})
				return
			}
		}
		time.Sleep(2 * time.Second)
	}

	// Build the exploration prompt
	prompt := exp.buildExplorationPrompt(ctx, session)

	// Create a temporary agent session
	agentSessionID := "ai_explore_" + session.ID
	agentStreamChan := make(chan any, 200)

	safeSend(session.StreamChan, ExploreEvent{Type: "progress", Content: "AI agent is analyzing the task..."})

	// Forward agent events in background
	forwarded := make(chan struct{})
	go func() {
		defer close(forwarded)
		for chunk := range agentStreamChan {
			if ctx.Err() != nil {
				return
			}
			exp.forwardAgentEvent(session, chunk)
		}
	}()

	err := exp.agentManager.SendMessageInterface(ctx, agentSessionID, prompt, agentStreamChan, session.LLMConfigID)

	<-forwarded

	if err != nil {
		if ctx.Err() != nil {
			if session.Status != "stopped" {
				session.Status = "failed"
				session.Error = "Exploration timed out"
			}
		} else {
			session.Status = "failed"
			session.Error = fmt.Sprintf("Agent error: %v", err)
		}
		session.EndTime = time.Now()
		if session.Status == "failed" {
			safeSend(session.StreamChan, ExploreEvent{Type: "error", Content: session.Error})
		}
		safeSend(session.StreamChan, ExploreEvent{Type: "done"})
		exp.closeExplorationPage(ctx)
		return
	}

	// Collect recorded operations
	if exp.execRecorder != nil {
		ops := exp.execRecorder.GetRecordedOpsAsOpRecords()
		if len(ops) > len(session.RawOps) {
			session.RawOps = ops
		}
	}
	logger.Info(ctx, "[Explorer] Completed with %d raw ops, optimizing...", len(session.RawOps))

	safeSend(session.StreamChan, ExploreEvent{
		Type:    "progress",
		Content: fmt.Sprintf("Exploration completed. Optimizing %d operations into script...", len(session.RawOps)),
	})

	script := OptimizeOperationsToScript(session.RawOps, session.TaskDesc, session.StartURL)
	session.GeneratedScript = script
	session.Status = "completed"
	session.EndTime = time.Now()

	scriptJSON, _ := json.Marshal(script)
	safeSend(session.StreamChan, ExploreEvent{Type: "script_ready", Data: json.RawMessage(scriptJSON)})
	safeSend(session.StreamChan, ExploreEvent{Type: "done"})

	logger.Info(ctx, "[Explorer] Session %s completed: %d actions in final script", session.ID, len(script.Actions))

	// Close the browser page opened during exploration
	exp.closeExplorationPage(ctx)

	// Cleanup session after 30 minutes
	go func() {
		time.Sleep(30 * time.Minute)
		exp.mu.Lock()
		delete(exp.sessions, session.ID)
		exp.mu.Unlock()
	}()
}

// ensureBrowserRunning makes sure a browser instance is started through the
// multi-instance system. This avoids the conflict where the legacy Start()
// creates an unregistered browser that collides with startInstanceInternal().
//
// It handles several edge cases:
//  1. Browser already running → reuse it
//  2. Orphaned Chrome from a previous session → try reconnecting via DevToolsActivePort
//  3. Stale lock files blocking launch → clean up and retry
func (exp *Explorer) ensureBrowserRunning(ctx context.Context, session *ExploreSession) error {
	if exp.browserManager == nil {
		return fmt.Errorf("browser manager is not available")
	}

	// If the browser is already running (via the instance system), nothing to do
	if exp.browserManager.IsRunning() {
		logger.Info(ctx, "[Explorer] Browser is already running")
		safeSend(session.StreamChan, ExploreEvent{Type: "progress", Content: "Browser is ready"})
		return nil
	}

	safeSend(session.StreamChan, ExploreEvent{Type: "progress", Content: "Starting browser..."})

	// Determine which instance to start
	instanceID := session.InstanceID
	if instanceID == "" {
		instanceID = "default"
	}

	// Attempt 1: Try to start the instance normally
	logger.Info(ctx, "[Explorer] Starting browser instance: %s", instanceID)
	err := exp.browserManager.StartInstance(ctx, instanceID)
	if err == nil {
		logger.Info(ctx, "[Explorer] ✓ Browser instance started successfully")
		safeSend(session.StreamChan, ExploreEvent{Type: "progress", Content: "Browser started successfully"})
		return nil
	}

	logger.Warn(ctx, "[Explorer] First start attempt failed: %v", err)

	// Check if it failed due to debug url issue (orphaned Chrome / stale lock)
	if !strings.Contains(err.Error(), "Failed to get the debug url") &&
		!strings.Contains(err.Error(), "SingletonLock") &&
		!strings.Contains(err.Error(), "lockfile") {
		return fmt.Errorf("failed to start browser instance %s: %w", instanceID, err)
	}

	// Attempt 2: Try to reconnect to an existing orphaned Chrome via DevToolsActivePort
	safeSend(session.StreamChan, ExploreEvent{Type: "progress", Content: "Detecting orphaned Chrome, attempting reconnection..."})
	if reconnected := exp.tryReconnectOrphanedChrome(ctx, instanceID); reconnected {
		logger.Info(ctx, "[Explorer] ✓ Reconnected to existing Chrome")
		safeSend(session.StreamChan, ExploreEvent{Type: "progress", Content: "Reconnected to existing Chrome"})
		return nil
	}

	// Attempt 3: Kill orphaned Chrome processes, cleanup lock files, and retry
	logger.Info(ctx, "[Explorer] Killing orphaned Chrome processes and cleaning up...")
	safeSend(session.StreamChan, ExploreEvent{Type: "progress", Content: "Killing orphaned Chrome processes..."})

	if killErr := exp.browserManager.KillOrphanedChromeProcesses(ctx); killErr != nil {
		logger.Warn(ctx, "[Explorer] Failed to kill orphaned Chrome: %v", killErr)
	}

	// Wait for Chrome process to fully exit and release file handles
	logger.Info(ctx, "[Explorer] Waiting for Chrome process to exit...")
	time.Sleep(3 * time.Second)

	// Now clean up lock files (should succeed since process is dead)
	exp.cleanupStaleChromeLocks(ctx)
	time.Sleep(500 * time.Millisecond)

	// Verify lockfile is actually gone
	if cfg := exp.browserManager.GetConfig(); cfg != nil && cfg.Browser != nil && cfg.Browser.UserDataDir != "" {
		lockPath := filepath.Join(cfg.Browser.UserDataDir, "lockfile")
		if _, err := os.Stat(lockPath); err == nil {
			logger.Error(ctx, "[Explorer] ⚠ lockfile still exists after killing Chrome! The file may be held by another process.")
			// Try one more aggressive cleanup
			if removeErr := os.Remove(lockPath); removeErr != nil {
				logger.Error(ctx, "[Explorer] Still cannot remove lockfile: %v", removeErr)
			} else {
				logger.Info(ctx, "[Explorer] ✓ lockfile removed on final attempt")
			}
		} else {
			logger.Info(ctx, "[Explorer] ✓ lockfile successfully removed")
		}
	}

	// Retry browser start
	logger.Info(ctx, "[Explorer] Retrying browser start after killing orphaned processes...")
	safeSend(session.StreamChan, ExploreEvent{Type: "progress", Content: "Retrying browser start..."})

	err = exp.browserManager.StartInstance(ctx, instanceID)
	if err == nil {
		logger.Info(ctx, "[Explorer] ✓ Browser started after killing orphaned processes")
		safeSend(session.StreamChan, ExploreEvent{Type: "progress", Content: "Browser started successfully"})
		return nil
	}

	logger.Error(ctx, "[Explorer] Final browser start attempt failed: %v", err)
	return fmt.Errorf("failed to start browser after recovery (instance %s): %w", instanceID, err)
}

// tryReconnectOrphanedChrome attempts to connect to a Chrome instance
// left running from a previous server session by reading DevToolsActivePort.
func (exp *Explorer) tryReconnectOrphanedChrome(ctx context.Context, instanceID string) bool {
	cfg := exp.browserManager.GetConfig()
	if cfg == nil || cfg.Browser == nil || cfg.Browser.UserDataDir == "" {
		return false
	}

	portFile := filepath.Join(cfg.Browser.UserDataDir, "DevToolsActivePort")
	data, err := os.ReadFile(portFile)
	if err != nil {
		logger.Info(ctx, "[Explorer] No DevToolsActivePort file found")
		return false
	}

	lines := strings.SplitN(string(data), "\n", 2)
	if len(lines) < 2 || lines[0] == "" {
		return false
	}

	port := strings.TrimSpace(lines[0])
	wsPath := strings.TrimSpace(lines[1])
	wsURL := fmt.Sprintf("ws://127.0.0.1:%s%s", port, wsPath)

	logger.Info(ctx, "[Explorer] Attempting to reconnect to orphaned Chrome at %s", wsURL)

	// Try connecting with a short timeout
	reconnectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	browser := rod.New().ControlURL(wsURL).Context(reconnectCtx)
	if err := browser.Connect(); err != nil {
		logger.Info(ctx, "[Explorer] Reconnection failed: %v (Chrome is dead, need cleanup)", err)
		return false
	}

	// Connection succeeded! Register this browser in the instance system
	logger.Info(ctx, "[Explorer] ✓ Successfully reconnected to orphaned Chrome")
	exp.browserManager.AdoptBrowser(ctx, instanceID, browser)
	return true
}

// cleanupStaleChromeLocks removes stale lock files from the Chrome user data directory.
func (exp *Explorer) cleanupStaleChromeLocks(ctx context.Context) {
	cfg := exp.browserManager.GetConfig()
	if cfg == nil || cfg.Browser == nil || cfg.Browser.UserDataDir == "" {
		return
	}

	userDataDir := cfg.Browser.UserDataDir
	staleFiles := []string{
		"lockfile",
		"DevToolsActivePort",
		"SingletonLock",
		"SingletonCookie",
		"SingletonSocket",
	}

	for _, name := range staleFiles {
		path := filepath.Join(userDataDir, name)
		if _, err := os.Stat(path); err == nil {
			if err := os.Remove(path); err != nil {
				logger.Warn(ctx, "[Explorer] Failed to remove %s: %v", name, err)
			} else {
				logger.Info(ctx, "[Explorer] Removed stale file: %s", name)
			}
		}
	}
}

// getExplorerPromptContent loads the AI Explorer system prompt from DB, falling back to the default.
func (exp *Explorer) getExplorerPromptContent(ctx context.Context) string {
	if exp.db != nil {
		dbPrompt, err := exp.db.GetPrompt(models.SystemPromptAIExplorerID)
		if err == nil && dbPrompt != nil {
			return dbPrompt.Content
		}
		logger.Warn(ctx, "[Explorer] Failed to load AI Explorer prompt from DB, using default: %v", err)
	}
	// Fallback to hardcoded default
	return models.SystemPromptAIExplorer.Content
}

// buildExplorationPrompt constructs the prompt for AI exploration
func (exp *Explorer) buildExplorationPrompt(ctx context.Context, session *ExploreSession) string {
	var sb strings.Builder

	var currentURL string
	page := exp.browserManager.GetActivePage()
	if page != nil {
		if info, err := page.Info(); err == nil {
			currentURL = info.URL
		}
	}

	// Load the base prompt content from DB (user-customizable)
	sb.WriteString(exp.getExplorerPromptContent(ctx))
	sb.WriteString("\n\n")

	// Append dynamic context
	sb.WriteString("## Current State\n")
	if currentURL != "" {
		sb.WriteString(fmt.Sprintf("- Current page URL: %s\n", currentURL))
	}
	sb.WriteString("\n")
	sb.WriteString("## Your Objective\n")
	sb.WriteString(session.TaskDesc)
	sb.WriteString("\n")

	return sb.String()
}

// closeExplorationPage closes the active browser page after exploration finishes.
func (exp *Explorer) closeExplorationPage(ctx context.Context) {
	page := exp.browserManager.GetActivePage()
	if page == nil {
		return
	}
	logger.Info(ctx, "[Explorer] Closing exploration page...")
	if err := exp.browserManager.CloseActivePage(ctx, page); err != nil {
		logger.Warn(ctx, "[Explorer] Failed to close exploration page: %v", err)
	} else {
		logger.Info(ctx, "[Explorer] Exploration page closed")
	}
}

// forwardAgentEvent converts agent stream chunks to ExploreEvents
func (exp *Explorer) forwardAgentEvent(session *ExploreSession, chunk any) {
	data, err := json.Marshal(chunk)
	if err != nil {
		return
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return
	}

	eventType, _ := parsed["type"].(string)
	switch eventType {
	case "message":
		content, _ := parsed["content"].(string)
		if content != "" {
			safeSend(session.StreamChan, ExploreEvent{Type: "thinking", Content: content})
		}
	case "tool_call":
		safeSend(session.StreamChan, ExploreEvent{Type: "tool_call", Data: parsed["tool_call"]})
	case "error":
		errMsg, _ := parsed["error"].(string)
		safeSend(session.StreamChan, ExploreEvent{Type: "error", Content: errMsg})
	}
}

// safeSend sends to a channel without blocking
func safeSend(ch chan ExploreEvent, event ExploreEvent) {
	select {
	case ch <- event:
	default:
	}
}
