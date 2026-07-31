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
	ID               uint         `gorm:"primaryKey" json:"id"`
	VersionID        uint         `gorm:"index;not null" json:"version_id"`
	Name             string       `gorm:"size:255;not null" json:"name"`
	Path             string       `gorm:"size:255" json:"path"` // 页面的路由或相对路径
	Description      string       `gorm:"type:text" json:"description"`
	PageFlowRevision uint64       `gorm:"not null;default:0" json:"page_flow_revision"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
	Scripts          []PageScript `gorm:"foreignKey:PageID;constraint:OnDelete:CASCADE;" json:"scripts,omitempty"`
	TestCases        []TestCase   `gorm:"foreignKey:PageID;constraint:OnDelete:CASCADE;" json:"test_cases,omitempty"`
}

// PageScript 记录针对某个页面的操作轨迹和 DOM 快照（底层技术脚本，保留以供调优）
type PageScript struct {
	ID                       uint      `gorm:"primaryKey" json:"id"`
	PageID                   uint      `gorm:"index;not null" json:"page_id"`
	SourceRecordingSessionID *uint     `gorm:"uniqueIndex:page_scripts_source_recording_session_uniq" json:"source_recording_session_id,omitempty"`
	PageScriptContentHash    string    `gorm:"size:128;index" json:"page_script_content_hash"`
	NormalizerVersion        string    `gorm:"size:64" json:"normalizer_version"`
	Name                     string    `gorm:"size:255" json:"name"`
	ActionTrace              string    `gorm:"type:text" json:"action_trace"` // JSON 序列化后的用户录制操作序列
	DOMSnapshot              string    `gorm:"type:text" json:"dom_snapshot"` // 页面结构 JSON 快照
	RecordingMetaJSON        string    `gorm:"type:text" json:"recording_meta_json"`
	CreatedAt                time.Time `json:"created_at"`
	UpdatedAt                time.Time `json:"updated_at"`
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
	ID                       uint   `gorm:"primaryKey" json:"id"`
	ProjectID                uint   `gorm:"index:idx_project_auth_state_scope,not null;uniqueIndex:project_auth_states_active_scope_uniq,priority:1,where:status = 'active'" json:"project_id"`
	VersionID                uint   `gorm:"index:idx_project_auth_state_scope,not null;uniqueIndex:project_auth_states_active_scope_uniq,priority:2,where:status = 'active'" json:"version_id"`
	SourceRecordingSessionID *uint  `gorm:"index" json:"source_recording_session_id,omitempty"`
	SourceSnapshotReceiptID  string `gorm:"size:128;uniqueIndex:project_auth_states_snapshot_receipt_uniq,where:source_snapshot_receipt_id <> ''" json:"source_snapshot_receipt_id,omitempty"`
	Name                     string `gorm:"size:255" json:"name"`
	Status                   string `gorm:"size:50;default:'active';index" json:"status"`
	SchemaVersion            int    `gorm:"default:1" json:"schema_version"`
	// StateJSON is retained only so a deployment migration can erase the old
	// column explicitly. New writes must use StateCiphertext and callers only
	// receive the decrypted transient value populated by the API service.
	StateJSON           string     `gorm:"type:text" json:"-"`
	StateCiphertext     string     `gorm:"type:text" json:"-"`
	StateNonce          string     `gorm:"size:128" json:"-"`
	EncryptionVersion   int        `gorm:"not null;default:1" json:"encryption_version"`
	EncryptionKeyID     string     `gorm:"size:128" json:"encryption_key_id"`
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
	ID                       uint       `gorm:"primaryKey" json:"id"`
	ProjectID                uint       `gorm:"index:idx_recording_session_scope,not null" json:"project_id"`
	VersionID                uint       `gorm:"index:idx_recording_session_scope,not null" json:"version_id"`
	PageID                   uint       `gorm:"index:idx_recording_session_scope,not null" json:"page_id"`
	RecordingKind            string     `gorm:"size:50;not null" json:"recording_kind"`
	AuthContext              string     `gorm:"size:50;not null" json:"auth_context"`
	SourceAuthStateID        *uint      `gorm:"index" json:"source_auth_state_id,omitempty"`
	TargetURL                string     `gorm:"type:text" json:"target_url"`
	Status                   string     `gorm:"size:50;index;not null" json:"status"`
	BrowserInstanceID        string     `gorm:"size:255;not null;default:'';uniqueIndex:recording_sessions_active_instance_uniq,where:status = 'starting' OR status = 'recording'" json:"browser_instance_id"`
	RuntimePageID            string     `gorm:"size:255;not null;default:''" json:"runtime_page_id"`
	RuntimeInstanceID        string     `gorm:"size:255;not null;default:''" json:"runtime_instance_id"`
	RuntimeGeneration        string     `gorm:"size:255;not null;default:''" json:"runtime_generation"`
	LeaseGeneration          string     `gorm:"size:255;not null;default:''" json:"lease_generation"`
	LifecycleRevision        uint64     `gorm:"not null;default:1" json:"lifecycle_revision"`
	SyncRevision             uint64     `gorm:"not null;default:0" json:"sync_revision"`
	SyncPayloadHash          string     `gorm:"size:128;not null;default:''" json:"sync_payload_hash"`
	DraftHash                string     `gorm:"size:128;not null;default:''" json:"draft_hash"`
	DraftCompletenessVersion int        `gorm:"not null;default:1" json:"draft_completeness_version"`
	BasePageFlowRevision     uint64     `gorm:"not null;default:0" json:"base_page_flow_revision"`
	ActionsJSON              string     `gorm:"type:text" json:"actions_json"`
	ActionCount              int        `json:"action_count"`
	DOMSnapshot              string     `gorm:"type:text" json:"dom_snapshot"`
	RecordingMetaJSON        string     `gorm:"type:text" json:"recording_meta_json"`
	FailureCode              string     `gorm:"size:128;not null;default:''" json:"failure_code"`
	FailureDetailSanitized   string     `gorm:"type:text;not null;default:''" json:"failure_detail_sanitized"`
	FailedAt                 *time.Time `json:"failed_at"`
	ErrorMessage             string     `gorm:"type:text" json:"error_message"`
	StartedAt                time.Time  `json:"started_at"`
	LastSyncedAt             time.Time  `json:"last_synced_at"`
	StoppedAt                time.Time  `json:"stopped_at"`
	SavedAt                  time.Time  `json:"saved_at"`
	CreatedBy                string     `gorm:"size:128;index" json:"created_by"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

// RecordingOperation 是录制生命周期的全局幂等回执。pending 表示运行时
// side effect 已预留但还未形成数据库终态；completed/failed 都可重放。
type RecordingOperation struct {
	ID                          uint   `gorm:"primaryKey" json:"id"`
	OperationID                 string `gorm:"size:64;uniqueIndex:recording_operations_operation_id_uniq;not null" json:"operation_id"`
	Action                      string `gorm:"size:32;not null" json:"action"`
	Scope                       string `gorm:"type:text;not null" json:"scope"`
	RequestPayloadHash          string `gorm:"size:128;not null" json:"request_payload_hash"`
	RequestCanonicalizerVersion string `gorm:"size:64;not null" json:"request_canonicalizer_version"`
	Status                      string `gorm:"size:32;index;not null" json:"status"`
	// RuntimeEffectKey is only set for actions which reserve a runtime side
	// effect.  NULL deliberately keeps pure DB actions out of the partial
	// pending-effect uniqueness index.
	RuntimeEffectKey *string `gorm:"size:512;uniqueIndex:recording_operations_pending_effect_uniq,where:status = 'pending' AND runtime_effect_key IS NOT NULL" json:"runtime_effect_key,omitempty"`
	// RuntimeDriverToken is a fencing token for Start. Only the holder of the
	// current token/generation may adopt a runtime lease into a RecordingSession.
	RuntimeDriverToken           *string    `gorm:"size:128" json:"runtime_driver_token,omitempty"`
	RuntimeDriverClaimGeneration uint64     `gorm:"not null;default:0" json:"runtime_driver_claim_generation"`
	RuntimeDriverClaimedAt       *time.Time `json:"runtime_driver_claimed_at,omitempty"`
	RuntimeDriverLeaseExpiresAt  *time.Time `json:"runtime_driver_lease_expires_at,omitempty"`
	RecordingSessionID           *uint      `gorm:"index" json:"recording_session_id,omitempty"`
	ProjectID                    uint       `gorm:"index" json:"project_id"`
	VersionID                    uint       `gorm:"index" json:"version_id"`
	PageID                       uint       `gorm:"index" json:"page_id"`
	BrowserInstanceID            string     `gorm:"size:255" json:"browser_instance_id"`
	RuntimePageID                string     `gorm:"size:255" json:"runtime_page_id"`
	RuntimeInstanceID            string     `gorm:"size:255" json:"runtime_instance_id"`
	RuntimeGeneration            string     `gorm:"size:255" json:"runtime_generation"`
	LeaseGeneration              string     `gorm:"size:255" json:"lease_generation"`
	ReceiptID                    string     `gorm:"size:255" json:"receipt_id"`
	// RuntimeReceiptClaimGeneration fences a final/auth runtime receipt claim.
	// ACK or Release must match this generation as well as the operation and
	// full recording scope, so a timed-out predecessor cannot consume a later
	// claimant's receipt.
	RuntimeReceiptClaimGeneration uint64     `gorm:"not null;default:0" json:"runtime_receipt_claim_generation"`
	SanitizedResponseJSON         string     `gorm:"type:text" json:"-"`
	HTTPStatus                    int        `json:"http_status"`
	ErrorCode                     string     `gorm:"size:128" json:"error_code"`
	SanitizedErrorDetail          string     `gorm:"type:text" json:"-"`
	FailedAt                      *time.Time `json:"failed_at"`
	CreatedAt                     time.Time  `json:"created_at"`
	UpdatedAt                     time.Time  `json:"updated_at"`
}

// RecordingArtifact 保存录制大文件的受控元数据，不直接暴露本地绝对路径。
type RecordingArtifact struct {
	ID                 uint   `gorm:"primaryKey" json:"id"`
	ProjectID          uint   `gorm:"index:idx_recording_artifact_scope,not null" json:"project_id"`
	VersionID          uint   `gorm:"index:idx_recording_artifact_scope,not null" json:"version_id"`
	PageID             uint   `gorm:"index:idx_recording_artifact_scope,not null" json:"page_id"`
	RecordingSessionID uint   `gorm:"index;not null" json:"recording_session_id"`
	ArtifactType       string `gorm:"size:50;not null" json:"artifact_type"`
	StorageBackend     string `gorm:"size:50;not null" json:"storage_backend"`
	StoragePath        string `gorm:"type:text;not null" json:"storage_path"`
	FileName           string `gorm:"size:255" json:"file_name"`
	MimeType           string `gorm:"size:255" json:"mime_type"`
	SizeBytes          int64  `json:"size_bytes"`
	Sensitive          bool   `gorm:"default:false" json:"sensitive"`
	// SourceReceiptID and ArtifactFingerprint make the runtime-only receipt
	// provenance durable without letting a stale receipt overwrite the current
	// recording draft. The pair is unique only when it is available.
	SourceReceiptID     string    `gorm:"size:255;uniqueIndex:recording_artifacts_receipt_fingerprint_uniq,priority:1,where:source_receipt_id <> ''" json:"source_receipt_id,omitempty"`
	ArtifactFingerprint string    `gorm:"size:128;uniqueIndex:recording_artifacts_receipt_fingerprint_uniq,priority:2,where:source_receipt_id <> ''" json:"artifact_fingerprint,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
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
		&RecordingOperation{},
		&RecordingArtifact{},
	)
}
