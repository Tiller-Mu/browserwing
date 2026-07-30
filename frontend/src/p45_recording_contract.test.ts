import { projectApi, type RunTestCaseRequest } from './api/project';

export type P45RecordingKind = 'login_flow' | 'business_flow';
export type P45AuthContext = 'clean' | 'project_saved';

export interface P45RecordingMeta {
  schema_version: 1;
  recording_kind: P45RecordingKind;
  auth_context: P45AuthContext;
  auth_state_id: number | null;
  target_url: string;
}

export const p45ProjectAuthStateApiContract = {
  getProjectAuthState: projectApi.getProjectAuthState,
  captureProjectAuthState: projectApi.captureProjectAuthState,
  deleteProjectAuthState: projectApi.deleteProjectAuthState,
  getPageRecordingContext: projectApi.getPageRecordingContext,
  startPageRecordingSession: projectApi.startPageRecordingSession,
};

export const p45LoginRecordingSessionRequest = {
  recording_kind: 'login_flow',
  auth_context: 'clean',
} satisfies { recording_kind: P45RecordingKind; auth_context: P45AuthContext };

export const p45BusinessRecordingSessionRequest = {
  recording_kind: 'business_flow',
  auth_context: 'project_saved',
} satisfies { recording_kind: P45RecordingKind; auth_context: P45AuthContext };

export const p45CleanBusinessRecordingSessionRequest = {
  recording_kind: 'business_flow',
  auth_context: 'clean',
} satisfies { recording_kind: P45RecordingKind; auth_context: P45AuthContext };

export const p45SaveRecordingPayload: Parameters<typeof projectApi.savePageRecording>[3] = {
  operation_id: '00000000-0000-4000-8000-000000000045',
  name: '业务主流程',
  action_trace: '{"steps":[]}',
  dom_snapshot: '{"elements":[]}',
  recording_meta: {
    schema_version: 1,
    recording_kind: 'business_flow',
    auth_context: 'project_saved',
    auth_state_id: 12,
    target_url: 'https://example.invalid/orders',
  } satisfies P45RecordingMeta,
};

export const p45RunProjectSavedRequest: RunTestCaseRequest = {
  auth_context: 'project_saved',
  stop_on_failure: true,
  capture_screenshot: true,
};

export const p45RunCleanRequest: RunTestCaseRequest = {
  auth_context: 'clean',
  stop_on_failure: true,
};
