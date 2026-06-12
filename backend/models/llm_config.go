package models

import (
	"encoding/json"
	"time"
)

// LLMConfigModel LLM配置数据库模型
type LLMConfigModel struct {
	ID               string    `gorm:"primaryKey;size:128" json:"id"`
	Name             string    `gorm:"size:255;not null;uniqueIndex" json:"name"` // LLM 名称标识
	Provider         string    `gorm:"size:64;not null" json:"provider"`          // openai, anthropic, custom 等
	APIKey           string    `gorm:"-" json:"api_key"`                          // API密钥，仅运行时使用，不落库
	APIKeyCiphertext string    `gorm:"column:api_key_ciphertext;type:text" json:"-"`
	APIKeyNonce      string    `gorm:"type:text" json:"-"`
	APIKeyKeyID      string    `gorm:"size:128" json:"-"`
	Model            string    `gorm:"size:255;not null" json:"model"` // 模型名称，如 gpt-4, claude-3
	BaseURL          string    `gorm:"type:text" json:"base_url"`      // 自定义 API 地址
	IsDefault        bool      `gorm:"index" json:"is_default"`        // 是否为默认配置
	IsActive         bool      `gorm:"index" json:"is_active"`         // 是否启用
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (LLMConfigModel) TableName() string {
	return "llm_configs"
}

// ToJSON 转换为JSON字节
func (l *LLMConfigModel) ToJSON() ([]byte, error) {
	return json.Marshal(l)
}

// FromJSON 从JSON字节解析
func (l *LLMConfigModel) FromJSON(data []byte) error {
	return json.Unmarshal(data, l)
}
