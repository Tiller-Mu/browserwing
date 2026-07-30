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

-- A pre-P4.7.6 active session cannot prove runtime identity. Do not allow it
-- to bypass the new partial uniqueness/receipt protocol after deployment.
UPDATE recording_sessions
SET status = 'failed',
    failure_code = 'runtime_lease_lost',
    failure_detail_sanitized = 'legacy active recording lacks P4.7.6 runtime identity',
    failed_at = NOW(),
    lifecycle_revision = lifecycle_revision + 1,
    updated_at = NOW()
WHERE status IN ('starting', 'recording')
  AND (browser_instance_id = '' OR runtime_page_id = '' OR runtime_generation = '');

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
