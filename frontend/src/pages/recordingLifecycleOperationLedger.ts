import { newRecordingOperationID } from '../api/project';

export interface RecordingOperationLedger {
  prepare: <T extends object>(key: string, requestPayload: T) => PreparedRecordingOperation<T>;
  operationID: (key: string, requestSnapshot?: unknown) => string;
  settle: (key: string) => void;
}

export interface PreparedRecordingOperation<T extends object> {
  operationID: string;
  payload: T & { operation_id: string };
}

interface RecordingOperationLedgerEntry {
  operationID: string;
  canonicalPayload: string;
  payload: Record<string, unknown>;
}

export class RecordingOperationInputChangedError extends Error {
  readonly code = 'recording_operation_input_changed';

  constructor() {
    super('上一次操作仍在处理中，请等待其完成后再修改并重试。');
    this.name = 'RecordingOperationInputChangedError';
  }
}

// This is deliberately UI-local retry state, not a lifecycle fact source. A
// mounted screen owns one ledger so response-loss retries retain their exact
// idempotency key across re-renders.
export function createRecordingOperationLedger(): RecordingOperationLedger {
  const pending = new Map<string, RecordingOperationLedgerEntry>();
  return {
    prepare<T extends object>(key: string, requestPayload: T): PreparedRecordingOperation<T> {
      const canonicalPayload = canonicalLedgerPayload(requestPayload);
      const existing = pending.get(key);
      if (existing) {
        if (existing.canonicalPayload !== canonicalPayload) throw new RecordingOperationInputChangedError();
        return {
          operationID: existing.operationID,
          payload: cloneLedgerPayload(existing.payload) as T & { operation_id: string },
        };
      }
      const operationID = newRecordingOperationID();
      const payload = cloneLedgerPayload({ ...requestPayload, operation_id: operationID });
      pending.set(key, { operationID, canonicalPayload, payload: payload as Record<string, unknown> });
      return { operationID, payload: cloneLedgerPayload(payload) as T & { operation_id: string } };
    },
    operationID(key: string, requestSnapshot?: unknown) {
      const existing = pending.get(key);
      if (existing) return existing.operationID;
      const operationID = newRecordingOperationID();
      const payload = requestSnapshot && typeof requestSnapshot === 'object' ? cloneLedgerPayload(requestSnapshot as Record<string, unknown>) : {};
      pending.set(key, { operationID, canonicalPayload: canonicalLedgerPayload(payload), payload });
      return operationID;
    },
    settle(key: string) {
      pending.delete(key);
    },
  };
}

function canonicalLedgerPayload(value: unknown): string {
  return JSON.stringify(canonicalLedgerValue(value));
}

function canonicalLedgerValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalLedgerValue);
  if (value && typeof value === 'object') {
    const source = value as Record<string, unknown>;
    return Object.keys(source).sort().reduce<Record<string, unknown>>((out, key) => {
      if (source[key] !== undefined) out[key] = canonicalLedgerValue(source[key]);
      return out;
    }, {});
  }
  return value === undefined ? null : value;
}

function cloneLedgerPayload<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

const terminalRecordingLifecycleCodes = new Set([
  'operation_id_invalid', 'operation_id_payload_conflict', 'recording_session_not_found',
  'recording_action_not_allowed', 'recording_lifecycle_conflict', 'sync_revision_stale',
  'sync_revision_payload_conflict', 'runtime_lease_lost', 'runtime_receipt_unavailable',
  'auth_snapshot_unavailable', 'recording_actions_invalid', 'recording_source_invalid',
  'recording_capture_not_allowed', 'recording_session_auth_capture_not_allowed',
  'recording_session_auth_capture_not_ready', 'page_script_replaced_conflict',
  'page_script_superseded', 'start_cancelled', 'auth_state_encryption_unavailable',
	'recording_auth_state_unavailable',
]);

export function recordingOperationFailureIsTerminal(error: unknown): boolean {
	const response = (error as { response?: { status?: unknown; data?: { code?: unknown } } })?.response;
	const status = Number(response?.status || 0);
	if (status < 400 || status >= 500) return false;
	const code = String(response?.data?.code || '');
	return terminalRecordingLifecycleCodes.has(code);
}

export function recordingOperationInputChanged(error: unknown): boolean {
  return String((error as { code?: unknown })?.code || '') === 'recording_operation_input_changed';
}

export function startRecordingOperationKey(input: {
  projectId: string;
  versionId: string;
  pageId: string;
  recordingKind: string;
}): string {
	return `start:${input.projectId}:${input.versionId}:${input.pageId}:${input.recordingKind}`;
}
