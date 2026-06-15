package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/browserwing/browserwing/llm"
	"github.com/gin-gonic/gin"
)

// StartExploration starts a new AI exploration session
func (h *Handler) StartExploration(c *gin.Context) {
	if h.explorer == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI Explorer is not available"})
		return
	}

	var req struct {
		TaskDesc    string `json:"task_desc" binding:"required"`
		StartURL    string `json:"start_url"`
		LLMConfigID string `json:"llm_config_id"`
		InstanceID  string `json:"instance_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, err := llm.ResolveRuntimeConfig(h.db, req.LLMConfigID); err != nil {
		if writeLLMConfigError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	session, err := h.explorer.StartExploration(c.Request.Context(), req.TaskDesc, req.StartURL, req.LLMConfigID, req.InstanceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"session_id": session.ID,
		"status":     session.Status,
	})
}

// StreamExploration streams exploration events via SSE
func (h *Handler) StreamExploration(c *gin.Context) {
	if h.explorer == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI Explorer is not available"})
		return
	}

	sessionID := c.Param("id")
	session, ok := h.explorer.GetSession(sessionID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming not supported"})
		return
	}

	clientGone := c.Request.Context().Done()

	for {
		select {
		case <-clientGone:
			return
		case event, ok := <-session.StreamChan:
			if !ok {
				data, _ := json.Marshal(map[string]string{"type": "done"})
				fmt.Fprintf(c.Writer, "data: %s\n\n", data)
				flusher.Flush()
				return
			}

			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			flusher.Flush()

			if event.Type == "done" {
				return
			}
		case <-time.After(30 * time.Second):
			fmt.Fprintf(c.Writer, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// StopExploration stops a running exploration session
func (h *Handler) StopExploration(c *gin.Context) {
	if h.explorer == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI Explorer is not available"})
		return
	}

	sessionID := c.Param("id")
	if err := h.explorer.StopExploration(sessionID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "stopped"})
}

// GetExplorationScript returns the generated script for a completed session
func (h *Handler) GetExplorationScript(c *gin.Context) {
	if h.explorer == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI Explorer is not available"})
		return
	}

	sessionID := c.Param("id")
	session, ok := h.explorer.GetSession(sessionID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}

	if session.GeneratedScript == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "script not ready yet", "status": session.Status})
		return
	}

	c.JSON(http.StatusOK, session.GeneratedScript)
}

// SaveExplorationScript saves the generated script to the database
func (h *Handler) SaveExplorationScript(c *gin.Context) {
	if h.explorer == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI Explorer is not available"})
		return
	}

	sessionID := c.Param("id")
	session, ok := h.explorer.GetSession(sessionID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}

	if session.GeneratedScript == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no script to save"})
		return
	}

	// Allow overriding name/description from request
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err == nil {
		if req.Name != "" {
			session.GeneratedScript.Name = req.Name
		}
		if req.Description != "" {
			session.GeneratedScript.Description = req.Description
		}
	}

	if err := h.db.SaveScript(session.GeneratedScript); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to save script: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":      session.GeneratedScript.ID,
		"name":    session.GeneratedScript.Name,
		"message": "Script saved successfully",
	})
}
