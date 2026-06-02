package models

import (
	"time"

	"gorm.io/gorm"
)

// Project 代表一个顶级的测试项目（例如：“统一考试平台”）
type Project struct {
	ID          uint             `gorm:"primaryKey" json:"id"`
	Name        string           `gorm:"size:255;not null;unique" json:"name"`
	Description string           `gorm:"type:text" json:"description"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	Versions    []ProjectVersion `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE;" json:"versions,omitempty"`
}

// ProjectVersion 代表项目下的某个版本（例如：“v1.0”），版本支持整体克隆
type ProjectVersion struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	ProjectID   uint       `gorm:"index;not null" json:"project_id"`
	VersionName string     `gorm:"size:100;not null" json:"version_name"`
	Description string     `gorm:"type:text" json:"description"`
	BaseURL     string     `gorm:"size:255" json:"base_url"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Pages       []TestPage `gorm:"foreignKey:VersionID;constraint:OnDelete:CASCADE;" json:"pages,omitempty"`
}

// TestPage 代表某个具体的测试页面或功能点模块（例如：“登录页”、“成绩查询页”）
type TestPage struct {
	ID          uint         `gorm:"primaryKey" json:"id"`
	VersionID   uint         `gorm:"index;not null" json:"version_id"`
	Name        string       `gorm:"size:255;not null" json:"name"`
	Path        string       `gorm:"size:255" json:"path"` // 页面的路由或相对路径
	Description string       `gorm:"type:text" json:"description"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	Scripts     []PageScript `gorm:"foreignKey:PageID;constraint:OnDelete:CASCADE;" json:"scripts,omitempty"`
	TestCases   []TestCase   `gorm:"foreignKey:PageID;constraint:OnDelete:CASCADE;" json:"test_cases,omitempty"`
}

// PageScript 记录针对某个页面的操作轨迹和 DOM 快照（底层技术脚本，保留以供调优）
type PageScript struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	PageID      uint      `gorm:"index;not null" json:"page_id"`
	Name        string    `gorm:"size:255" json:"name"`
	ActionTrace string    `gorm:"type:text" json:"action_trace"` // JSON 序列化后的用户录制操作序列
	DOMSnapshot string    `gorm:"type:text" json:"dom_snapshot"` // 页面结构 JSON 快照
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TestCase 是真正的测试用例，通常由 PageScript + 大模型智能裂变而来
type TestCase struct {
	ID             uint            `gorm:"primaryKey" json:"id"`
	PageID         uint            `gorm:"index;not null" json:"page_id"`
	Title          string          `gorm:"size:255;not null" json:"title"`
	Description    string          `gorm:"type:text" json:"description"`
	Blueprint      string          `gorm:"type:text" json:"blueprint"`      // 固化的 JSON 格式执行大纲
	ScriptContent  string          `gorm:"type:text" json:"script_content"` // 如果有需要，可以保存生成的 Python Playwright 物理代码
	Status         string          `gorm:"size:50;default:'active'" json:"status"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	Refinements    []LLMRefinement `gorm:"foreignKey:TestCaseID;constraint:OnDelete:CASCADE;" json:"refinements,omitempty"`
	TestExecutions []TestExecution `gorm:"foreignKey:TestCaseID;constraint:OnDelete:CASCADE;" json:"executions,omitempty"`
}

// LLMRefinement 记录大模型对测试用例的二次干预与调优记录
type LLMRefinement struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	TestCaseID        uint       `gorm:"index;not null" json:"test_case_id"`
	UserPrompt        string     `gorm:"type:text" json:"user_prompt"`        // 测试人员输入的指导方向（如："增加密码为空的校验"）
	OriginalBlueprint string     `gorm:"type:text" json:"original_blueprint"` // 生成建议时的 Blueprint 快照
	RefinedBlueprint  string     `gorm:"type:text" json:"refined_blueprint"`  // 大模型优化后的 JSON 大纲
	Summary           string     `gorm:"type:text" json:"summary"`
	RiskNotes         string     `gorm:"type:text" json:"risk_notes"`
	Status            string     `gorm:"size:50;default:'proposed';index" json:"status"`
	AppliedAt         *time.Time `json:"applied_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// TestExecution 记录单次测试执行的报告结果
type TestExecution struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	TestCaseID   uint      `gorm:"index;not null" json:"test_case_id"`
	Status       string    `gorm:"size:50" json:"status"` // passed, failed, error
	ErrorMessage string    `gorm:"type:text" json:"error_message"`
	DurationMs   int       `json:"duration_ms"`
	ReportData   string    `gorm:"type:text" json:"report_data"` // 存储额外的截图路径、视频路径或全量日志
	CreatedAt    time.Time `json:"created_at"`
}

// AutoMigrate 注册所有的结构体至 GORM
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&Project{},
		&ProjectVersion{},
		&TestPage{},
		&PageScript{},
		&TestCase{},
		&LLMRefinement{},
		&TestExecution{},
	)
}
