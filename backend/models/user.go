package models

import (
	"time"
)

// User 用户模型
type User struct {
	ID        string    `gorm:"primaryKey;size:128" json:"id"`
	Username  string    `gorm:"size:255;not null;uniqueIndex" json:"username"`
	Password  string    `json:"password,omitempty"` // 密码字段，返回给前端时应清空
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ApiKey API密钥模型
type ApiKey struct {
	ID          string    `gorm:"primaryKey;size:128" json:"id"`
	Name        string    `json:"name"`                                     // API密钥名称
	Key         string    `gorm:"size:255;not null;uniqueIndex" json:"key"` // 实际的密钥
	Description string    `json:"description"`                              // 描述
	UserID      string    `gorm:"size:128;index" json:"user_id"`            // 所属用户ID
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// LoginRequest 登录请求
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResponse 登录响应
type LoginResponse struct {
	Token string `json:"token"`
	User  *User  `json:"user"`
}

// CreateUserRequest 创建用户请求
type CreateUserRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// UpdatePasswordRequest 更新密码请求
type UpdatePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// CreateApiKeyRequest 创建API密钥请求
type CreateApiKeyRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}
