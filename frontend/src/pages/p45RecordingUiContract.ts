import type {
  P45AuthContext,
  P45RecordingKind,
  P45RecordingMeta,
  PageRecordingDetail,
  ProjectAuthStateSummary,
  SavePageRecordingRequest,
  TestPage,
} from '../api/project';
import {
  type RecordingOperationLedger,
  recordingOperationFailureIsTerminal,
  startRecordingOperationKey,
} from './recordingLifecycleOperationLedger';

export interface P45AuthStateSummaryView {
  id: number;
  cookieCount: number;
  originCount: number;
  origins: string[];
  capturedAt: string;
  capturedUrl?: string;
}

export interface P45RecordingActionView {
  recordingKind: P45RecordingKind;
  authContext: P45AuthContext;
  disabled?: boolean;
  guidance?: {
    kind: 'capture_auth_state_or_choose_clean';
  };
}

export interface P45PageManagementRowView {
  pageId: number;
  page: TestPage;
  actions: P45RecordingActionView[];
}

export interface P45AuthStateActionView {
  kind: 'delete_project_auth_state';
  disabled?: boolean;
}

export interface P45PageManagementView {
  layout: 'list' | 'table';
  authState: P45AuthStateSummaryView | null;
  authStateActions: P45AuthStateActionView[];
  rows: P45PageManagementRowView[];
}

export interface P45RecordingDetailView {
  actionCount: number;
  snapshotElementCount: number;
  status: 'ready' | 'warning';
  qualityMessages: string[];
  parseErrorCount: number;
  sensitiveFieldsRemoved: string[];
}

export type P45InPageStoppedRecordingAction =
  | 'show_save_dialog'
  | 'show_login_auth_dialog'
  | 'cancel_zero_action_project_recording'
  | 'dismiss_zero_action_legacy_recording';

export function resolveP45InPageStoppedRecordingAction(input: {
  isProjectRecordingContext: boolean;
  recordingKind: P45RecordingKind;
  actionCount: number;
}): P45InPageStoppedRecordingAction {
  if (input.actionCount > 0) return 'show_save_dialog';
  if (!input.isProjectRecordingContext) return 'dismiss_zero_action_legacy_recording';
  return input.recordingKind === 'login_flow'
    ? 'show_login_auth_dialog'
    : 'cancel_zero_action_project_recording';
}

const recordingQualityMessages: Record<string, string> = {
  recording_action_missing_target: '存在缺少目标定位的录制动作',
  recording_action_missing_value: '存在缺少输入值的录制动作',
  recording_navigation_missing_url: '存在缺少 URL 的导航动作',
  recording_snapshot_unusable: '页面快照为空或不可用',
  recording_meta_invalid: '录制元数据无效',
  recording_auth_context_conflict: '录制登录态上下文冲突',
};

export function buildP45AuthStateSummary(raw: null | undefined): null;
export function buildP45AuthStateSummary(raw: Partial<ProjectAuthStateSummary>): P45AuthStateSummaryView;
export function buildP45AuthStateSummary(raw: Partial<ProjectAuthStateSummary> | null | undefined): P45AuthStateSummaryView | null;
export function buildP45AuthStateSummary(raw: Partial<ProjectAuthStateSummary> | null | undefined): P45AuthStateSummaryView | null {
  if (!raw) return null;
  return {
    id: Number(raw.id || 0),
    cookieCount: Number(raw.cookie_count || 0),
    originCount: Number(raw.origin_count || 0),
    origins: Array.isArray(raw.origins) ? [...raw.origins] : [],
    capturedAt: String(raw.captured_at || ''),
    capturedUrl: typeof raw.captured_url === 'string' ? raw.captured_url : undefined,
  };
}

export function buildP45PageManagementView(input: {
  pages: TestPage[];
  authState: P45AuthStateSummaryView | null;
}): P45PageManagementView {
  return {
    layout: 'table',
    authState: input.authState,
    authStateActions: [
      { kind: 'delete_project_auth_state', disabled: !input.authState },
    ],
    rows: input.pages.map((page) => ({
      pageId: page.id,
      page,
      actions: [
        { recordingKind: 'login_flow', authContext: 'clean' },
        {
          recordingKind: 'business_flow',
          authContext: 'project_saved',
          disabled: !input.authState,
          guidance: input.authState ? undefined : { kind: 'capture_auth_state_or_choose_clean' },
        },
        { recordingKind: 'business_flow', authContext: 'clean' },
      ],
    })),
  };
}

export function buildP45RecordingDetailView(recording: Pick<PageRecordingDetail, 'diagnostics'>): P45RecordingDetailView {
  const diagnostics = recording.diagnostics || {
    action_count: 0,
    snapshot_element_count: 0,
    quality_codes: [],
    parse_errors: [],
    sensitive_fields_removed: [],
  };
  const qualityCodes = Array.isArray(diagnostics.quality_codes) ? diagnostics.quality_codes : [];
  const parseErrors = Array.isArray(diagnostics.parse_errors) ? diagnostics.parse_errors : [];
  return {
    actionCount: Number(diagnostics.action_count || 0),
    snapshotElementCount: Number(diagnostics.snapshot_element_count || 0),
    status: qualityCodes.length > 0 || parseErrors.length > 0 ? 'warning' : 'ready',
    qualityMessages: qualityCodes.map((code) => recordingQualityMessages[code] || code),
    parseErrorCount: parseErrors.length,
    sensitiveFieldsRemoved: Array.isArray(diagnostics.sensitive_fields_removed) ? diagnostics.sensitive_fields_removed : [],
  };
}

export function createP45AuthStateController(options: {
  projectId: number;
  versionId: number;
  api: {
    deleteProjectAuthState: (projectId: number, versionId: number) => Promise<unknown>;
  };
}) {
  return {
    async remove() {
      await options.api.deleteProjectAuthState(options.projectId, options.versionId);
      return null;
    },
  };
}

export function createP45RecordingController(options: {
  projectId: number;
  versionId: number;
  operationLedger: RecordingOperationLedger;
  browserInstanceId?: string;
  runtimePageIDFor?: (pageId: number) => string;
  api: {
    startPageRecordingSession: (
      projectId: number,
      versionId: number,
      pageId: number,
		payload: { operation_id: string; recording_kind: P45RecordingKind; auth_context: P45AuthContext; target_url?: string; browser_instance_id: string; runtime_page_id: string },
    ) => Promise<{
      data?: {
        error?: string;
        recording_session_id?: string;
		page_id?: number;
        recording_meta?: {
          recording_kind?: P45RecordingKind;
          auth_context?: P45AuthContext;
          target_url?: string;
          auth_state_id?: number | null;
        };
      };
    }>;
  };
  navigate: (to: string) => void;
}) {
  return {
    async startRecording(input: {
      pageId: number;
      recordingKind: P45RecordingKind;
      authContext: P45AuthContext;
      authStateId?: number | null;
      targetUrl?: string;
    }) {
      const targetUrl = input.targetUrl?.trim();
	  const acceptedTargetURL = targetUrl && /^https?:\/\//i.test(targetUrl) ? targetUrl : undefined;
      const browserInstanceId = options.browserInstanceId || 'default';
      const runtimePageId = options.runtimePageIDFor?.(input.pageId) || `project-page:${input.pageId}`;
      const operationKey = startRecordingOperationKey({
        projectId: String(options.projectId),
        versionId: String(options.versionId),
        pageId: String(input.pageId),
        recordingKind: input.recordingKind,
      });
      const startPayload = options.operationLedger.prepare(operationKey, {
        recording_kind: input.recordingKind,
        auth_context: input.authContext,
		target_url: acceptedTargetURL,
        browser_instance_id: browserInstanceId,
        runtime_page_id: runtimePageId,
      }).payload;
      let response: Awaited<ReturnType<typeof options.api.startPageRecordingSession>>;
      try {
        response = await options.api.startPageRecordingSession(options.projectId, options.versionId, input.pageId, startPayload);
      } catch (error: unknown) {
        const activeSessionData = (error as {
          response?: {
            data?: {
              error?: string;
              recording_session_id?: string;
				page_id?: number;
              recording_meta?: {
                recording_kind?: P45RecordingKind;
                auth_context?: P45AuthContext;
                target_url?: string;
                auth_state_id?: number | null;
              };
            };
          };
        }).response?.data;
        if (
          activeSessionData?.error !== 'recording_session_active' ||
          !activeSessionData.recording_session_id ||
			activeSessionData.page_id !== input.pageId ||
          activeSessionData.recording_meta?.recording_kind !== input.recordingKind ||
          activeSessionData.recording_meta?.auth_context !== input.authContext
        ) {
          if (recordingOperationFailureIsTerminal(error)) options.operationLedger.settle(operationKey);
          throw error;
        }
        response = { data: activeSessionData };
      }
      options.operationLedger.settle(operationKey);
      const data = response.data;
      const recordingKind = data?.recording_meta?.recording_kind || input.recordingKind;
      const authContext = data?.recording_meta?.auth_context || input.authContext;
      const params = new URLSearchParams({
        projectId: String(options.projectId),
        versionId: String(options.versionId),
        pageId: String(input.pageId),
        recordingKind,
        authContext,
      });
      const authStateId = data?.recording_meta?.auth_state_id ?? input.authStateId;
      const resolvedTargetUrl = data?.recording_meta?.target_url || input.targetUrl;
      const recordingSessionId = data?.recording_session_id;
      if (authStateId != null) params.set('authStateId', String(authStateId));
      if (resolvedTargetUrl) params.set('targetUrl', resolvedTargetUrl);
      if (recordingSessionId) params.set('recordingSessionId', recordingSessionId);
      options.navigate(`/browser?${params.toString()}`);
    },
  };
}

export function buildP45SaveRecordingPayload(input: {
	name: string;
  actionTrace: string;
  domSnapshot: string;
  recordingMeta: P45RecordingMeta;
  recordingSessionId?: string | null;
  retainAuthSnapshot?: boolean;
}): Omit<SavePageRecordingRequest, 'operation_id'> {
  return {
    name: input.name,
    action_trace: input.actionTrace,
    dom_snapshot: input.domSnapshot,
    recording_session_id: input.recordingSessionId || undefined,
    retain_auth_snapshot: input.retainAuthSnapshot || undefined,
    recording_meta: {
      schema_version: 1,
      recording_kind: input.recordingMeta.recording_kind,
      auth_context: input.recordingMeta.auth_context,
      auth_state_id: input.recordingMeta.auth_state_id,
      target_url: input.recordingMeta.target_url,
      started_at: input.recordingMeta.started_at,
      ended_at: input.recordingMeta.ended_at,
    },
  };
}
