package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/browserwing/browserwing/llm"
	"github.com/browserwing/browserwing/models"
	"github.com/gin-gonic/gin"
)

func writeLLMConfigError(c *gin.Context, err error) bool {
	status, body := llmConfigErrorResponse(err)
	if status == 0 {
		return false
	}
	c.JSON(status, body)
	return true
}

func llmConfigErrorResponse(err error) (int, gin.H) {
	if err == nil {
		return 0, nil
	}
	var cfgErr *llm.ConfigError
	if errors.As(err, &cfgErr) {
		return http.StatusBadRequest, gin.H{
			"error": cfgErr.Error(),
			"code":  cfgErr.Code,
		}
	}
	return 0, nil
}

func (h *Handler) currentUser(c *gin.Context) (*models.User, error) {
	userID, ok := c.Get("user_id")
	if !ok {
		return nil, nil
	}
	return h.db.GetUser(stringFromAny(userID))
}

func (h *Handler) currentUserIsAdmin(c *gin.Context) bool {
	if h == nil || h.config == nil || h.config.Auth == nil || !h.config.Auth.Enabled {
		return true
	}
	user, err := h.currentUser(c)
	return err == nil && user != nil && user.IsAdmin
}

func (h *Handler) requireAdmin(c *gin.Context) bool {
	if h.currentUserIsAdmin(c) {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "error.forbidden"})
	return false
}

type llmConfigTesterFunc func(context.Context, *models.LLMConfigModel) (map[string]any, error)
