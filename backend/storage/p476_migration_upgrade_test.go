package storage

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/browserwing/browserwing/models"
	"gorm.io/gorm"
)

func TestP476MigrationRepairsNullableRuntimeIdentityUpgrade(t *testing.T) {
	db := newP47StorageContractDB(t)
	if err := models.AutoMigrate(db); err != nil {
		t.Fatalf("prepare current recording schema: %v", err)
	}

	legacy := &models.RecordingSession{
		ProjectID:         1,
		VersionID:         1,
		PageID:            1,
		RecordingKind:     "business_flow",
		AuthContext:       "clean",
		Status:            "recording",
		BrowserInstanceID: "legacy-browser",
		RuntimePageID:     "legacy-page",
		RuntimeInstanceID: "legacy-runtime",
		RuntimeGeneration: "legacy-generation",
		LeaseGeneration:   "legacy-lease",
		TargetURL:         "https://example.test/recording",
		StartedAt:         time.Now().UTC(),
	}
	if err := db.Create(legacy).Error; err != nil {
		t.Fatalf("create legacy recording session: %v", err)
	}
	legacySibling := *legacy
	legacySibling.ID = 0
	legacySibling.BrowserInstanceID = "legacy-browser-sibling"
	if err := db.Create(&legacySibling).Error; err != nil {
		t.Fatalf("create second legacy recording session: %v", err)
	}

	if err := db.Exec(`
ALTER TABLE recording_sessions
    ALTER COLUMN browser_instance_id DROP NOT NULL,
    ALTER COLUMN runtime_page_id DROP NOT NULL,
    ALTER COLUMN runtime_instance_id DROP NOT NULL,
    ALTER COLUMN runtime_generation DROP NOT NULL,
    ALTER COLUMN lease_generation DROP NOT NULL,
    ALTER COLUMN lifecycle_revision DROP NOT NULL,
    ALTER COLUMN sync_revision DROP NOT NULL,
    ALTER COLUMN sync_payload_hash DROP NOT NULL,
    ALTER COLUMN draft_hash DROP NOT NULL,
    ALTER COLUMN draft_completeness_version DROP NOT NULL,
    ALTER COLUMN base_page_flow_revision DROP NOT NULL,
    ALTER COLUMN failure_code DROP NOT NULL,
    ALTER COLUMN failure_detail_sanitized DROP NOT NULL`).Error; err != nil {
		t.Fatalf("make legacy runtime identity nullable: %v", err)
	}
	if err := db.Exec(`
UPDATE recording_sessions
SET browser_instance_id = NULL,
    runtime_page_id = NULL,
    runtime_instance_id = NULL,
    runtime_generation = NULL,
    lease_generation = NULL,
    lifecycle_revision = NULL,
    sync_revision = NULL,
    sync_payload_hash = NULL,
    draft_hash = NULL,
    draft_completeness_version = NULL,
    base_page_flow_revision = NULL,
    failure_code = NULL,
    failure_detail_sanitized = NULL
WHERE id IN ?`, []uint{legacy.ID, legacySibling.ID}).Error; err != nil {
		t.Fatalf("clear legacy runtime identity values: %v", err)
	}

	p476RunDeployMigration(t, db)

	var migrated struct {
		Status            string
		BrowserInstanceID string
		RuntimePageID     string
		RuntimeGeneration string
		LeaseGeneration   string
		LifecycleRevision uint64
		FailureCode       string
		FailureDetail     string `gorm:"column:failure_detail_sanitized"`
	}
	if err := db.Table("recording_sessions").
		Select("status, browser_instance_id, runtime_page_id, runtime_generation, lease_generation, lifecycle_revision, failure_code, failure_detail_sanitized").
		Where("id = ?", legacy.ID).
		Take(&migrated).Error; err != nil {
		t.Fatalf("load migrated recording session: %v", err)
	}
	if migrated.Status != "failed" || migrated.FailureCode != "runtime_lease_lost" {
		t.Fatalf("legacy active session = status %q, failure %q, want failed/runtime_lease_lost", migrated.Status, migrated.FailureCode)
	}
	if migrated.BrowserInstanceID != "" || migrated.RuntimePageID != "" || migrated.RuntimeGeneration != "" || migrated.LeaseGeneration != "" {
		t.Fatalf("migrated runtime identity = browser %q page %q generation %q lease %q, want normalized empty values", migrated.BrowserInstanceID, migrated.RuntimePageID, migrated.RuntimeGeneration, migrated.LeaseGeneration)
	}
	if migrated.LifecycleRevision != 2 {
		t.Fatalf("migrated lifecycle revision = %d, want 2 after legacy session closure", migrated.LifecycleRevision)
	}
	var closedActiveSessions int64
	if err := db.Model(&models.RecordingSession{}).
		Where("id IN ? AND status = ? AND failure_code = ?", []uint{legacy.ID, legacySibling.ID}, "failed", "runtime_lease_lost").
		Count(&closedActiveSessions).Error; err != nil {
		t.Fatalf("count closed legacy active sessions: %v", err)
	}
	if closedActiveSessions != 2 {
		t.Fatalf("closed legacy active sessions = %d, want both NULL-browser active rows closed before unique-index normalization", closedActiveSessions)
	}

	if err := (&PostgresStore{db: db}).autoMigrate(); err != nil {
		t.Fatalf("PostgreSQL startup migration after P4.7.6 deploy migration: %v", err)
	}
	p476AssertRecordingSessionRuntimeColumnsNotNull(t, db)
}

func p476AssertRecordingSessionRuntimeColumnsNotNull(t *testing.T, db *gorm.DB) {
	t.Helper()
	var nullableColumns int64
	if err := db.Raw(`
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'recording_sessions'
  AND column_name IN (
      'browser_instance_id', 'runtime_page_id', 'runtime_instance_id',
      'runtime_generation', 'lease_generation', 'lifecycle_revision',
      'sync_revision', 'sync_payload_hash', 'draft_hash',
      'draft_completeness_version', 'base_page_flow_revision',
      'failure_code', 'failure_detail_sanitized'
  )
  AND is_nullable <> 'NO'`).Scan(&nullableColumns).Error; err != nil {
		t.Fatalf("inspect recording session nullability after startup migration: %v", err)
	}
	if nullableColumns != 0 {
		t.Fatalf("startup migration left %d P4.7.6 runtime columns nullable", nullableColumns)
	}
}

func p476RunDeployMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	migration, err := os.ReadFile("migrations/20260724_p476_project_auth_state_ciphertext.sql")
	if err != nil {
		t.Fatalf("read P4.7.6 deploy migration: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		for _, statement := range p476SQLStatements(string(migration)) {
			statement = strings.TrimSpace(statement)
			if statement == "" || strings.EqualFold(statement, "BEGIN") || strings.EqualFold(statement, "COMMIT") {
				continue
			}
			if err := tx.Exec(statement).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("run P4.7.6 deploy migration: %v", err)
	}
}

func p476SQLStatements(script string) []string {
	var statements []string
	start := 0
	inSingleQuote := false
	for index := 0; index < len(script); index++ {
		switch script[index] {
		case '\'':
			if inSingleQuote && index+1 < len(script) && script[index+1] == '\'' {
				index++
				continue
			}
			inSingleQuote = !inSingleQuote
		case ';':
			if !inSingleQuote {
				statements = append(statements, script[start:index])
				start = index + 1
			}
		}
	}
	if remainder := script[start:]; strings.TrimSpace(remainder) != "" {
		statements = append(statements, remainder)
	}
	return statements
}
