package models

import (
	"time"

	"gorm.io/gorm"
)

// Project 代表一个顶级的测试项目（例如：“统一考试平台”）
type Project struct {
	ID          uint               `gorm:"primaryKey" json:"id"`
	Name        string             `gorm:"size:255;not null;unique" json:"name"`
	Description string             `gorm:"type:text" json:"description"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	Versions    []ProjectVersion   `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE;" json:"versions,omitempty"`
	AuthStates  []ProjectAuthState `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE;" json:"-"`
}

// ProjectVersion 代表项目下的某个版本（例如：“v1.0”），版本支持整体克隆
type ProjectVersion struct {
	ID          uint               `gorm:"primaryKey" json:"id"`
	ProjectID   uint               `gorm:"index;not null" json:"project_id"`
	VersionName string             `gorm:"size:100;not null" json:"version_name"`
	Description string             `gorm:"type:text" json:"description"`
	BaseURL     string             `gorm:"size:255" json:"base_url"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	Pages       []TestPage         `gorm:"foreignKey:VersionID;constraint:OnDelete:CASCADE;" json:"pages,omitempty"`
	AuthStates  []ProjectAuthState `gorm:"foreignKey:VersionID;constraint:OnDelete:CASCADE;" json:"-"`
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
	ID                uint      `gorm:"primaryKey" json:"id"`
	PageID            uint      `gorm:"index;not null" json:"page_id"`
	Name              string    `gorm:"size:255" json:"name"`
	ActionTrace       string    `gorm:"type:text" json:"action_trace"` // JSON 序列化后的用户录制操作序列
	DOMSnapshot       string    `gorm:"type:text" json:"dom_snapshot"` // 页面结构 JSON 快照
	RecordingMetaJSON string    `gorm:"type:text" json:"recording_meta_json"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
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

// ProjectAuthState 保存某个 ProjectVersion 的默认登录态。
// StateJSON 是敏感字段，只能供恢复执行使用，普通响应必须返回摘要。
type ProjectAuthState struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	ProjectID           uint       `gorm:"index:idx_project_auth_state_scope,not null" json:"project_id"`
	VersionID           uint       `gorm:"index:idx_project_auth_state_scope,not null" json:"version_id"`
	Name                string     `gorm:"size:255" json:"name"`
	Status              string     `gorm:"size:50;default:'active';index" json:"status"`
	SchemaVersion       int        `gorm:"default:1" json:"schema_version"`
	StateJSON           string     `gorm:"type:text" json:"-"`
	StateDigest         string     `gorm:"size:128" json:"state_digest"`
	OriginAllowlistJSON string     `gorm:"type:text" json:"origin_allowlist_json"`
	CookieCount         int        `json:"cookie_count"`
	OriginCount         int        `json:"origin_count"`
	CapturedURL         string     `gorm:"type:text" json:"captured_url"`
	CapturedPageID      uint       `gorm:"index" json:"captured_page_id"`
	CapturedAt          time.Time  `json:"captured_at"`
	LastValidatedAt     *time.Time `json:"last_validated_at"`
	InvalidReason       string     `gorm:"type:text" json:"invalid_reason"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// RecordingSession 是项目页面录制过程的数据库事实源。
type RecordingSession struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	ProjectID         uint      `gorm:"index:idx_recording_session_scope,not null" json:"project_id"`
	VersionID         uint      `gorm:"index:idx_recording_session_scope,not null" json:"version_id"`
	PageID            uint      `gorm:"index:idx_recording_session_scope,not null" json:"page_id"`
	RecordingKind     string    `gorm:"size:50;not null" json:"recording_kind"`
	AuthContext       string    `gorm:"size:50;not null" json:"auth_context"`
	SourceAuthStateID *uint     `gorm:"index" json:"source_auth_state_id,omitempty"`
	TargetURL         string    `gorm:"type:text" json:"target_url"`
	Status            string    `gorm:"size:50;index;not null" json:"status"`
	ActionsJSON       string    `gorm:"type:text" json:"actions_json"`
	ActionCount       int       `json:"action_count"`
	DOMSnapshot       string    `gorm:"type:text" json:"dom_snapshot"`
	RecordingMetaJSON string    `gorm:"type:text" json:"recording_meta_json"`
	ErrorMessage      string    `gorm:"type:text" json:"error_message"`
	StartedAt         time.Time `json:"started_at"`
	LastSyncedAt      time.Time `json:"last_synced_at"`
	StoppedAt         time.Time `json:"stopped_at"`
	SavedAt           time.Time `json:"saved_at"`
	CreatedBy         string    `gorm:"size:128;index" json:"created_by"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// RecordingArtifact 保存录制大文件的受控元数据，不直接暴露本地绝对路径。
type RecordingArtifact struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	ProjectID          uint      `gorm:"index:idx_recording_artifact_scope,not null" json:"project_id"`
	VersionID          uint      `gorm:"index:idx_recording_artifact_scope,not null" json:"version_id"`
	PageID             uint      `gorm:"index:idx_recording_artifact_scope,not null" json:"page_id"`
	RecordingSessionID uint      `gorm:"index;not null" json:"recording_session_id"`
	ArtifactType       string    `gorm:"size:50;not null" json:"artifact_type"`
	StorageBackend     string    `gorm:"size:50;not null" json:"storage_backend"`
	StoragePath        string    `gorm:"type:text;not null" json:"storage_path"`
	FileName           string    `gorm:"size:255" json:"file_name"`
	MimeType           string    `gorm:"size:255" json:"mime_type"`
	SizeBytes          int64     `json:"size_bytes"`
	Sensitive          bool      `gorm:"default:false" json:"sensitive"`
	CreatedAt          time.Time `json:"created_at"`
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
		&ProjectAuthState{},
		&RecordingSession{},
		&RecordingArtifact{},
	)
}
