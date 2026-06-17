package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/browserwing/browserwing/services/playbotagent"
	"github.com/gin-gonic/gin"
)

func (h *ProjectHandlers) StartGenerateTestCasesRun(c *gin.Context) {
	projectID, versionID, pageID, ok := parseProjectVersionPageIDs(c)
	if !ok {
		return
	}
	var req generateTestCasesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}
	if h.playbotRuns == nil {
		h.playbotRuns = newPlaybotRunHub(15*time.Minute, 200)
	}
	runID := h.playbotRuns.start(playbotRunOwnerID(c))
	h.playbotRuns.append(runID, playbotRunEvent{
		Phase:   "queued",
		Level:   "info",
		Message: "Playbot generation run has started.",
	})
	go h.executeGenerateTestCasesRun(runID, projectID, versionID, pageID, req)
	c.JSON(http.StatusOK, gin.H{
		"run_id": runID,
		"status": "running",
	})
}

func (h *ProjectHandlers) executeGenerateTestCasesRun(runID string, projectID, versionID, pageID uint, req generateTestCasesRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	sink := func(event playbotagent.Event) {
		h.playbotRuns.append(runID, playbotRunEventFromAgentEvent(event))
	}
	response := h.executeGenerateTestCases(ctx, projectID, versionID, pageID, req, generateTestCasesExecutionOptions{
		EnableAgentLLM: true,
		EventSink:      sink,
	})
	phase := "done"
	level := "info"
	message := "Playbot generation completed."
	if response.Status >= http.StatusBadRequest {
		phase = "failed"
		level = "error"
		message = "Playbot generation failed."
	}
	h.playbotRuns.append(runID, playbotRunEvent{
		Phase:   phase,
		Level:   level,
		Message: message,
		Data: map[string]any{
			"status":   response.Status,
			"response": response.Body,
		},
	})
}

func playbotRunEventFromAgentEvent(event playbotagent.Event) playbotRunEvent {
	phase := strings.TrimSpace(event.Phase)
	switch phase {
	case "done":
		phase = "agent_done"
	case "failed":
		phase = "agent_failed"
	}
	return playbotRunEvent{
		RequestID:      event.RequestID,
		Phase:          phase,
		Level:          strings.TrimSpace(event.Level),
		Message:        sanitizePlaybotRunDisplayString(event.Message, nil),
		VisibleMessage: sanitizePlaybotRunDisplayString(event.VisibleMessage, nil),
		Data:           sanitizePlaybotRunEventData(event.Data, nil),
		CreatedAt:      event.CreatedAt,
	}
}

func (h *ProjectHandlers) StreamPlaybotRun(c *gin.Context) {
	if h.playbotRuns == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "playbot_run_not_found"})
		return
	}
	runID := strings.TrimSpace(c.Param("run_id"))
	afterSeq, _ := strconv.ParseInt(strings.TrimSpace(c.Query("after_seq")), 10, 64)
	sub, ok := h.playbotRuns.subscribe(runID, playbotRunOwnerID(c), afterSeq)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "playbot_run_not_found"})
		return
	}
	if sub.cancel != nil {
		defer sub.cancel()
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming_not_supported"})
		return
	}
	writeEvent := func(event playbotRunEvent) bool {
		data, err := json.Marshal(event)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	for event := range sub.backlog {
		if !writeEvent(event) {
			return
		}
	}
	if sub.done {
		return
	}
	clientGone := c.Request.Context().Done()
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-clientGone:
			return
		case event, ok := <-sub.live:
			if !ok {
				return
			}
			if !writeEvent(event) {
				return
			}
			if event.Phase == "done" || event.Phase == "failed" {
				return
			}
		case <-keepalive.C:
			fmt.Fprintf(c.Writer, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (h *ProjectHandlers) GetPlaybotRunResult(c *gin.Context) {
	if h.playbotRuns == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "playbot_run_not_found"})
		return
	}
	result, ok := h.playbotRuns.result(strings.TrimSpace(c.Param("run_id")), playbotRunOwnerID(c))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "playbot_run_not_found"})
		return
	}
	if result == nil {
		c.JSON(http.StatusAccepted, gin.H{"status": "running"})
		return
	}
	c.JSON(http.StatusOK, result)
}

func playbotRunOwnerID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.GetString("user_id"))
}

type playbotRunDisplayRedaction struct {
	token       string
	replacement string
}

func sanitizeP475AgentDisplayPayload(result map[string]any, source p475RecordingSource, secret playbotagent.SecretChannel) (string, map[string]any) {
	redactions := playbotRunDisplayRedactions(source, secret)
	visibleSummary := sanitizePlaybotRunDisplayString(result["visible_summary"], redactions)
	return visibleSummary, sanitizePlaybotRunModelOutput(result["model_output"], redactions)
}

func sanitizePlaybotAgentEventForDisplay(event playbotagent.Event, redactions []playbotRunDisplayRedaction) playbotagent.Event {
	event.Message = sanitizePlaybotRunDisplayString(event.Message, redactions)
	event.VisibleMessage = sanitizePlaybotRunDisplayString(event.VisibleMessage, redactions)
	event.Data = sanitizePlaybotRunEventData(event.Data, redactions)
	return event
}

func playbotRunDisplayRedactions(source p475RecordingSource, secret playbotagent.SecretChannel) []playbotRunDisplayRedaction {
	redactions := make([]playbotRunDisplayRedaction, 0, len(source.ActionTrace)+1)
	seen := map[string]struct{}{}
	appendRedaction := func(token string, replacement string, minLen int) {
		token = strings.TrimSpace(token)
		if len(token) < minLen {
			return
		}
		if _, ok := seen[token]; ok {
			return
		}
		seen[token] = struct{}{}
		redactions = append(redactions, playbotRunDisplayRedaction{token: token, replacement: replacement})
	}
	appendRedaction(secret.Value, "<redacted>", 1)
	for _, action := range source.ActionTrace {
		appendRedaction(stringFromAny(action["value"]), "已录制输入值", 4)
	}
	return redactions
}

func sanitizePlaybotRunModelOutput(value any, redactions []playbotRunDisplayRedaction) map[string]any {
	source, ok := mapFromPlaybotRunValue(value)
	if !ok {
		return nil
	}
	out := map[string]any{}
	if text := sanitizePlaybotRunDisplayString(source["visible_message"], redactions); text != "" {
		out["visible_message"] = text
	}
	if steps := sanitizePlaybotRunCandidateSteps(source["candidate_steps"], redactions); len(steps) > 0 {
		out["candidate_steps"] = steps
	}
	if values := sanitizePlaybotRunStringList(source["assumptions"], redactions); len(values) > 0 {
		out["assumptions"] = values
	}
	if values := sanitizePlaybotRunStringList(source["risk_notes"], redactions); len(values) > 0 {
		out["risk_notes"] = values
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sanitizePlaybotRunEventData(input map[string]any, redactions []playbotRunDisplayRedaction) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		switch key {
		case "candidate_steps":
			if steps := sanitizePlaybotRunCandidateSteps(value, redactions); len(steps) > 0 {
				out[key] = steps
			}
		case "assumptions", "risk_notes":
			if values := sanitizePlaybotRunStringList(value, redactions); len(values) > 0 {
				out[key] = values
			}
		case "code", "model", "provider", "action_count":
			if scalar, ok := sanitizePlaybotRunScalar(value, redactions); ok {
				out[key] = scalar
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sanitizePlaybotRunCandidateSteps(value any, redactions []playbotRunDisplayRedaction) []map[string]any {
	rawSteps, ok := anySliceFromPlaybotRunValue(value)
	if !ok {
		return nil
	}
	steps := make([]map[string]any, 0, len(rawSteps))
	for _, rawStep := range rawSteps {
		source, ok := mapFromPlaybotRunValue(rawStep)
		if !ok {
			continue
		}
		step := map[string]any{}
		for _, key := range []string{"action", "target_summary", "reason"} {
			if text := sanitizePlaybotRunDisplayString(source[key], redactions); text != "" {
				step[key] = text
			}
		}
		if trimPlaybotRunDisplayString(source["value_summary"]) != "" {
			step["value_summary"] = "已录制输入值"
		}
		if len(step) > 0 {
			steps = append(steps, step)
		}
	}
	return steps
}

func sanitizePlaybotRunStringList(value any, redactions []playbotRunDisplayRedaction) []string {
	items, ok := anySliceFromPlaybotRunValue(value)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text := sanitizePlaybotRunDisplayString(item, redactions); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func sanitizePlaybotRunScalar(value any, redactions []playbotRunDisplayRedaction) (any, bool) {
	switch typed := value.(type) {
	case string:
		text := sanitizePlaybotRunDisplayString(typed, redactions)
		if text == "" {
			return nil, false
		}
		return text, true
	case int, int64, float64, json.Number, bool:
		return typed, true
	default:
		return nil, false
	}
}

func anySliceFromPlaybotRunValue(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out, true
	case []string:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out, true
	default:
		return nil, false
	}
}

func mapFromPlaybotRunValue(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out, true
	default:
		return nil, false
	}
}

func trimPlaybotRunDisplayString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func sanitizePlaybotRunDisplayString(value any, redactions []playbotRunDisplayRedaction) string {
	text := trimPlaybotRunDisplayString(value)
	if text == "" {
		return ""
	}
	for _, redaction := range redactions {
		if redaction.token == "" {
			continue
		}
		text = strings.ReplaceAll(text, redaction.token, redaction.replacement)
	}
	return redactPlaybotRunLocalPath(text)
}

func redactPlaybotRunLocalPath(text string) string {
	idx := playbotRunWindowsAbsolutePathIndex(text)
	if idx < 0 {
		return text
	}
	return strings.TrimSpace(text[:idx] + "<redacted-local-path>")
}

func playbotRunWindowsAbsolutePathIndex(text string) int {
	for idx := 0; idx+2 < len(text); idx++ {
		if !playbotRunIsASCIIAlpha(text[idx]) || text[idx+1] != ':' {
			continue
		}
		if idx > 0 && playbotRunIsASCIIAlphaNumeric(text[idx-1]) {
			continue
		}
		if text[idx+2] == '\\' || text[idx+2] == '/' {
			return idx
		}
	}
	return -1
}

func playbotRunIsASCIIAlphaNumeric(value byte) bool {
	return playbotRunIsASCIIAlpha(value) || (value >= '0' && value <= '9')
}

func playbotRunIsASCIIAlpha(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}
