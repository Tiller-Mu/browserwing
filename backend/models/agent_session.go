package models

import "time"

// AgentSession Agent 聊天会话
type AgentSession struct {
	ID          string    `gorm:"primaryKey;size:128" json:"id"`
	LLMConfigID string    `gorm:"size:128;index" json:"llm_config_id"` // 会话使用的LLM配置ID
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AgentMessage Agent 聊天消息
type AgentMessage struct {
	ID        string                   `gorm:"primaryKey;size:128" json:"id"`
	SessionID string                   `gorm:"size:128;index" json:"session_id"`
	Role      string                   `json:"role"` // user, assistant, system
	Content   string                   `json:"content"`
	Timestamp time.Time                `json:"timestamp"`
	ToolCalls []map[string]interface{} `gorm:"type:jsonb;serializer:json" json:"tool_calls,omitempty"` // 工具调用信息
}
