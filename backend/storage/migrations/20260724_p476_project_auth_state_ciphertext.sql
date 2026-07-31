-- P4.7.6 PostgreSQL deploy migration. Run explicitly before enabling the
-- lifecycle service. Application startup deliberately does not delete
-- plaintext auth state, because silent destructive cleanup would hide an
-- operational error. Existing ProjectAuthState rows must be recaptured.

BEGIN;

ALTER TABLE test_pages
    ADD COLUMN IF NOT EXISTS page_flow_revision BIGINT NOT NULL DEFAULT 0;

ALTER TABLE page_scripts
    ADD COLUMN IF NOT EXISTS source_recording_session_id BIGINT,
    ADD COLUMN IF NOT EXISTS page_script_content_hash VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS normalizer_version VARCHAR(64) NOT NULL DEFAULT '';

ALTER TABLE project_auth_states
    ADD COLUMN IF NOT EXISTS source_recording_session_id BIGINT,
    ADD COLUMN IF NOT EXISTS source_snapshot_receipt_id VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS state_ciphertext TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS state_nonce VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS encryption_version INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS encryption_key_id VARCHAR(128) NOT NULL DEFAULT '';

ALTER TABLE recording_sessions
    ADD COLUMN IF NOT EXISTS browser_instance_id VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS runtime_page_id VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS runtime_instance_id VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS runtime_generation VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS lease_generation VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS lifecycle_revision BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS sync_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS sync_payload_hash VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS draft_hash VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS draft_completeness_version INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS base_page_flow_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS failure_code VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS failure_detail_sanitized TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS failed_at TIMESTAMPTZ;

-- A pre-P4.7.6 AutoMigrate can have created these columns without the
-- database defaults and NOT NULL constraints. It may also already have the
-- active-instance partial unique index, which permits several NULL browser
-- identities but would reject normalizing all of them to ''. Move incomplete
-- active rows out of that index before any NULL normalization.
UPDATE recording_sessions
SET status = 'failed',
    failure_code = 'runtime_lease_lost',
    failure_detail_sanitized = 'legacy active recording lacks P4.7.6 runtime identity',
    failed_at = NOW(),
    lifecycle_revision = COALESCE(lifecycle_revision, 1) + 1,
    updated_at = NOW()
WHERE status IN ('starting', 'recording')
  AND (
      browser_instance_id IS NULL
      OR browser_instance_id = ''
      OR runtime_page_id IS NULL
      OR runtime_page_id = ''
      OR runtime_generation IS NULL
      OR runtime_generation = ''
      OR lease_generation IS NULL
      OR lease_generation = ''
  );

-- Normalize the remaining nullable fields before tightening the schema below.
UPDATE recording_sessions
SET browser_instance_id = COALESCE(browser_instance_id, ''),
    runtime_page_id = COALESCE(runtime_page_id, ''),
    runtime_instance_id = COALESCE(runtime_instance_id, ''),
    runtime_generation = COALESCE(runtime_generation, ''),
    lease_generation = COALESCE(lease_generation, ''),
    lifecycle_revision = COALESCE(lifecycle_revision, 1),
    sync_revision = COALESCE(sync_revision, 0),
    sync_payload_hash = COALESCE(sync_payload_hash, ''),
    draft_hash = COALESCE(draft_hash, ''),
    draft_completeness_version = COALESCE(draft_completeness_version, 1),
    base_page_flow_revision = COALESCE(base_page_flow_revision, 0),
    failure_code = COALESCE(failure_code, ''),
    failure_detail_sanitized = COALESCE(failure_detail_sanitized, '')
WHERE browser_instance_id IS NULL
   OR runtime_page_id IS NULL
   OR runtime_instance_id IS NULL
   OR runtime_generation IS NULL
   OR lease_generation IS NULL
   OR lifecycle_revision IS NULL
   OR sync_revision IS NULL
   OR sync_payload_hash IS NULL
   OR draft_hash IS NULL
   OR draft_completeness_version IS NULL
   OR base_page_flow_revision IS NULL
   OR failure_code IS NULL
   OR failure_detail_sanitized IS NULL;

ALTER TABLE recording_sessions
    ALTER COLUMN browser_instance_id SET DEFAULT '',
    ALTER COLUMN browser_instance_id SET NOT NULL,
    ALTER COLUMN runtime_page_id SET DEFAULT '',
    ALTER COLUMN runtime_page_id SET NOT NULL,
    ALTER COLUMN runtime_instance_id SET DEFAULT '',
    ALTER COLUMN runtime_instance_id SET NOT NULL,
    ALTER COLUMN runtime_generation SET DEFAULT '',
    ALTER COLUMN runtime_generation SET NOT NULL,
    ALTER COLUMN lease_generation SET DEFAULT '',
    ALTER COLUMN lease_generation SET NOT NULL,
    ALTER COLUMN lifecycle_revision SET DEFAULT 1,
    ALTER COLUMN lifecycle_revision SET NOT NULL,
    ALTER COLUMN sync_revision SET DEFAULT 0,
    ALTER COLUMN sync_revision SET NOT NULL,
    ALTER COLUMN sync_payload_hash SET DEFAULT '',
    ALTER COLUMN sync_payload_hash SET NOT NULL,
    ALTER COLUMN draft_hash SET DEFAULT '',
    ALTER COLUMN draft_hash SET NOT NULL,
    ALTER COLUMN draft_completeness_version SET DEFAULT 1,
    ALTER COLUMN draft_completeness_version SET NOT NULL,
    ALTER COLUMN base_page_flow_revision SET DEFAULT 0,
    ALTER COLUMN base_page_flow_revision SET NOT NULL,
    ALTER COLUMN failure_code SET DEFAULT '',
    ALTER COLUMN failure_code SET NOT NULL,
    ALTER COLUMN failure_detail_sanitized SET DEFAULT '',
    ALTER COLUMN failure_detail_sanitized SET NOT NULL;

ALTER TABLE recording_sessions
    DROP CONSTRAINT IF EXISTS recording_sessions_active_runtime_identity_check;
ALTER TABLE recording_sessions
    ADD CONSTRAINT recording_sessions_active_runtime_identity_check
    CHECK (
        status NOT IN ('starting', 'recording')
        OR (
            browser_instance_id <> ''
            AND runtime_page_id <> ''
            AND runtime_generation <> ''
            AND lease_generation <> ''
        )
    );

CREATE TABLE IF NOT EXISTS recording_operations (
    id BIGSERIAL PRIMARY KEY,
    operation_id VARCHAR(64) NOT NULL,
    action VARCHAR(32) NOT NULL,
    scope TEXT NOT NULL,
    request_payload_hash VARCHAR(128) NOT NULL,
    request_canonicalizer_version VARCHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL,
    runtime_effect_key VARCHAR(512),
    runtime_driver_token VARCHAR(128),
    runtime_driver_claim_generation BIGINT NOT NULL DEFAULT 0,
    runtime_driver_claimed_at TIMESTAMPTZ,
    runtime_driver_lease_expires_at TIMESTAMPTZ,
    recording_session_id BIGINT,
    project_id BIGINT NOT NULL DEFAULT 0,
    version_id BIGINT NOT NULL DEFAULT 0,
    page_id BIGINT NOT NULL DEFAULT 0,
    browser_instance_id VARCHAR(255) NOT NULL DEFAULT '',
    runtime_page_id VARCHAR(255) NOT NULL DEFAULT '',
    runtime_instance_id VARCHAR(255) NOT NULL DEFAULT '',
    runtime_generation VARCHAR(255) NOT NULL DEFAULT '',
    lease_generation VARCHAR(255) NOT NULL DEFAULT '',
    receipt_id VARCHAR(255) NOT NULL DEFAULT '',
	 runtime_receipt_claim_generation BIGINT NOT NULL DEFAULT 0,
    sanitized_response_json TEXT NOT NULL DEFAULT '',
    http_status INTEGER NOT NULL DEFAULT 0,
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    sanitized_error_detail TEXT NOT NULL DEFAULT '',
    failed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE recording_operations
    ADD COLUMN IF NOT EXISTS runtime_driver_token VARCHAR(128),
    ADD COLUMN IF NOT EXISTS runtime_driver_claim_generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS runtime_driver_claimed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS runtime_driver_lease_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS runtime_receipt_claim_generation BIGINT NOT NULL DEFAULT 0;

CREATE UNIQUE INDEX IF NOT EXISTS page_scripts_source_recording_session_uniq
    ON page_scripts(source_recording_session_id)
    WHERE source_recording_session_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS project_auth_states_snapshot_receipt_uniq
    ON project_auth_states(source_snapshot_receipt_id)
    WHERE source_snapshot_receipt_id <> '';

-- Clear legacy plaintext/incomplete rows before enforcing the active-scope
-- uniqueness invariant. A pre-P4.7.6 database can legitimately contain more
-- than one active plaintext state, so creating the index first would abort the
-- migration before this explicit retirement can run.
UPDATE project_auth_states
SET state_json = '',
    status = 'invalid',
    invalid_reason = 'P4.7.6 removed legacy plaintext auth state; recapture required',
    updated_at = NOW()
WHERE COALESCE(state_json, '') <> ''
   OR COALESCE(state_ciphertext, '') = ''
   OR COALESCE(state_nonce, '') = '';

-- If a manually repaired deployment already contains duplicate encrypted
-- active states, retain the newest one deterministically and retire the rest
-- before the partial unique index is created.
WITH ranked_active_states AS (
    SELECT id,
           ROW_NUMBER() OVER (PARTITION BY project_id, version_id ORDER BY updated_at DESC, id DESC) AS row_number
    FROM project_auth_states
    WHERE status = 'active'
)
UPDATE project_auth_states AS state
SET status = 'invalid',
    invalid_reason = 'P4.7.6 resolved duplicate active auth state during migration; recapture if needed',
    updated_at = NOW()
FROM ranked_active_states AS ranked
WHERE state.id = ranked.id
  AND ranked.row_number > 1;

-- Capture replaces the active state under the project-version scope lock.
-- Keep the same invariant in PostgreSQL so a missed application lock cannot
-- publish two active states for one version.
CREATE UNIQUE INDEX IF NOT EXISTS project_auth_states_active_scope_uniq
    ON project_auth_states(project_id, version_id)
    WHERE status = 'active';
CREATE UNIQUE INDEX IF NOT EXISTS recording_sessions_active_instance_uniq
    ON recording_sessions(browser_instance_id)
    WHERE status IN ('starting', 'recording');
CREATE UNIQUE INDEX IF NOT EXISTS recording_operations_operation_id_uniq
    ON recording_operations(operation_id);
-- Existing development databases may already have the original NOT NULL
-- column/index.  Pure database operations must not reserve a shared empty
-- runtime effect, so make the key nullable before rebuilding the index.
ALTER TABLE recording_operations
    ALTER COLUMN runtime_effect_key DROP NOT NULL,
    ALTER COLUMN runtime_effect_key DROP DEFAULT;
UPDATE recording_operations
SET runtime_effect_key = NULL
WHERE runtime_effect_key = '';
DROP INDEX IF EXISTS recording_operations_pending_effect_uniq;
CREATE UNIQUE INDEX recording_operations_pending_effect_uniq
    ON recording_operations(runtime_effect_key)
    WHERE status = 'pending' AND runtime_effect_key IS NOT NULL;

ALTER TABLE recording_artifacts
    ADD COLUMN IF NOT EXISTS source_receipt_id VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS artifact_fingerprint VARCHAR(128) NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS recording_artifacts_receipt_fingerprint_uniq
    ON recording_artifacts(source_receipt_id, artifact_fingerprint)
    WHERE source_receipt_id <> '';

COMMIT;
