import { projectApi, type StopPageRecordingSessionResponse, type TestPage } from './api/project';
import BrowserManager, { p45BrowserManagerContract, recordingOperationFailureIsTerminal, startRecordingOperationKey } from './pages/BrowserManager';
import { toastAutoDismissDuration } from './components/Toast';
import {
	buildP45AuthStateSummary,
	buildP45PageManagementView,
	buildP45RecordingDetailView,
	buildP45SaveRecordingPayload,
	createP45RecordingController,
	resolveP45InPageStoppedRecordingAction,
} from './pages/p45RecordingUiContract';
import TestPageManager, { formatRecordingOperationInProgressMessage, p45TestPageManagerContract } from './pages/TestPageManager';
import { createRecordingOperationLedger } from './pages/recordingLifecycleOperationLedger';

type P45RecordingKind = 'login_flow' | 'business_flow';
type P45AuthContext = 'clean' | 'project_saved';

interface P45ProjectAuthStateSummary {
	id: number;
	status: 'active';
  cookie_count: number;
  origin_count: number;
  origins: string[];
  captured_at: string;
	captured_url: string;
}

interface P45AuthStateSummaryView {
	id: number;
	cookieCount: number;
	originCount: number;
	origins: string[];
	capturedAt: string;
}

interface P45RecordingActionView {
	recordingKind: P45RecordingKind;
	authContext: P45AuthContext;
	disabled?: boolean;
	guidance?: {
		kind: 'capture_auth_state_or_choose_clean';
	};
}

interface P45PageManagementRowView {
	pageId: number;
	actions: P45RecordingActionView[];
}

interface P45PageManagementView {
	layout: 'list' | 'table';
	authState: P45AuthStateSummaryView | null;
	authStateActions: Array<{
		kind: 'delete_project_auth_state';
		disabled?: boolean;
	}>;
	rows: P45PageManagementRowView[];
}

interface P45StartRecordingCall {
	projectId: number;
	versionId: number;
	pageId: number;
  payload: {
    operation_id: string;
    recording_kind: P45RecordingKind;
    auth_context: P45AuthContext;
    target_url?: string;
		browser_instance_id?: string;
		runtime_page_id?: string;
	};
}

interface P45TestPageManagerPageContract {
	buildAuthStateSummary: typeof buildP45AuthStateSummary;
	buildPageManagementView: typeof buildP45PageManagementView;
	buildRecordingDetailView: typeof buildP45RecordingDetailView;
	createRecordingController: typeof createP45RecordingController;
}

interface P45BrowserManagerPageContract {
	buildSaveRecordingPayload: typeof buildP45SaveRecordingPayload;
	resolveInPageStoppedRecordingAction: typeof resolveP45InPageStoppedRecordingAction;
	syncProjectRecordingDraft: typeof projectApi.syncPageRecordingSession;
	cancelProjectRecordingSession: typeof projectApi.cancelPageRecordingSession;
	createRecordingCancelController: P45RecordingCancelControllerFactory;
}

type P45RecordingCancelControllerFactory = (options: {
	api: {
		cancelPageRecordingSession: typeof projectApi.cancelPageRecordingSession;
	};
	clearLocalRecordingState: () => void | Promise<void>;
	setProjectRecordingSession?: (session: StopPageRecordingSessionResponse) => void;
}) => {
	cancelRecording: (input: {
		isProjectRecordingContext: boolean;
		projectId?: number | string | null;
		versionId?: number | string | null;
		pageId?: number | string | null;
		recordingSessionId?: string | null;
	}) => Promise<StopPageRecordingSessionResponse | undefined>;
};

const projectId = 31;
const versionId = 42;
const page: TestPage = {
  id: 53,
  version_id: versionId,
  name: '订单页面',
  path: '/orders',
  description: '核心业务页面',
  created_at: '2026-06-03T08:00:00Z',
	updated_at: '2026-06-03T08:00:00Z',
};

assertComponent(TestPageManager, 'TestPageManager');
assertComponent(BrowserManager, 'BrowserManager');
const pageManagerContract: P45TestPageManagerPageContract = p45TestPageManagerContract;
assertSameReference(
	pageManagerContract.buildAuthStateSummary,
	buildP45AuthStateSummary,
	'TestPageManager should consume the P4.5 auth-state summary helper',
);

assertEqual(
	recordingOperationFailureIsTerminal({ response: { status: 409, data: { error: 'human detail', code: 'recording_operation_in_progress' } } }),
	false,
	'operation-in-progress must retain the ledger using response.data.code rather than detail',
);
assertEqual(
	formatRecordingOperationInProgressMessage({ response: { data: { error: 'recording start target is already reserved' } } }),
	'运行时录制操作仍在进行中（recording_operation_in_progress）。请等待当前启动完成后再试；若持续出现，请保留此提示并检查浏览器录制页。 后端原因：recording start target is already reserved',
	'in-progress guidance must retain the backend reason for runtime diagnosis',
);
assertEqual(
	toastAutoDismissDuration('error'),
	undefined,
	'error notifications must remain visible until the user dismisses them',
);
assertEqual(
	toastAutoDismissDuration('success'),
	3000,
	'success notifications may retain the normal transient duration',
);
assertEqual(
	recordingOperationFailureIsTerminal({ response: { status: 409, data: { code: 'page_script_superseded' } } }),
	true,
	'known terminal lifecycle code must settle the ledger',
);
assertEqual(
	recordingOperationFailureIsTerminal({ response: { status: 500, data: { code: 'runtime_lease_lost' } } }),
	false,
	'5xx lifecycle failures must preserve the operation ledger for retry',
);
assertEqual(
	recordingOperationFailureIsTerminal({ response: { status: 409, data: { code: 'future_unknown_code' } } }),
	false,
	'unknown lifecycle code must retain the ledger until explicitly classified',
);
assertEqual(
	startRecordingOperationKey({ projectId: '1', versionId: '2', pageId: '3', recordingKind: 'business_flow' }) ===
		startRecordingOperationKey({ projectId: '1', versionId: '2', pageId: '3', recordingKind: 'business_flow' }),
	true,
	'pending Start must use a stable page-level key so runtime identity changes are blocked locally',
);
assertSameReference(
	pageManagerContract.buildPageManagementView,
	buildP45PageManagementView,
	'TestPageManager should consume the P4.5 page management view helper',
);
assertSameReference(
	pageManagerContract.buildRecordingDetailView,
	buildP45RecordingDetailView,
	'TestPageManager should expose the recording detail view helper',
);
assertSameReference(
	pageManagerContract.createRecordingController,
	createP45RecordingController,
	'TestPageManager should consume the P4.5 recording controller helper',
);
const browserManagerContract: P45BrowserManagerPageContract = p45BrowserManagerContract;
assertSameReference(
	browserManagerContract.buildSaveRecordingPayload,
	buildP45SaveRecordingPayload,
	'BrowserManager should consume the P4.5 save-recording payload helper',
);
assertSameReference(
	browserManagerContract.resolveInPageStoppedRecordingAction,
	resolveP45InPageStoppedRecordingAction,
	'BrowserManager should use the zero-action project recording decision helper',
);
assertSameReference(
	browserManagerContract.syncProjectRecordingDraft,
	projectApi.syncPageRecordingSession,
	'BrowserManager should sync project recording drafts through RecordingSession during recording',
);
assertFunction(
	projectApi.cancelPageRecordingSession,
	'projectApi should expose cancelPageRecordingSession for project RecordingSession cancel',
);
assertFunction(
	browserManagerContract.cancelProjectRecordingSession,
	'BrowserManager should expose the project RecordingSession cancel API binding',
);
assertSameReference(
	browserManagerContract.cancelProjectRecordingSession,
	projectApi.cancelPageRecordingSession,
	'BrowserManager should cancel project recording sessions through the backend cancel API',
);

assertEqual(
	resolveP45InPageStoppedRecordingAction({
		isProjectRecordingContext: true,
		recordingKind: 'login_flow',
		actionCount: 0,
	}),
	'show_login_auth_dialog',
	'zero-action login recording should retain its stopped session for auth capture or cancellation',
);
assertEqual(
	resolveP45InPageStoppedRecordingAction({
		isProjectRecordingContext: true,
		recordingKind: 'business_flow',
		actionCount: 0,
	}),
	'cancel_zero_action_project_recording',
	'zero-action business recording should automatically cancel after its stop persists',
);
assertEqual(
	resolveP45InPageStoppedRecordingAction({
		isProjectRecordingContext: true,
		recordingKind: 'login_flow',
		actionCount: 1,
	}),
	'show_save_dialog',
	'non-empty recordings should continue through the normal save dialog',
);

const authState: P45ProjectAuthStateSummary = {
	id: 64,
	status: 'active',
  cookie_count: 2,
  origin_count: 1,
  origins: ['https://example.invalid'],
  captured_at: '2026-06-03T09:00:00Z',
  captured_url: 'https://example.invalid/login',
};

const secretBearingRawAuthState = {
  ...authState,
  cookies: [{ name: 'session', value: 'secret-cookie-value' }],
  localStorage: { token: 'secret-local-token' },
	sessionStorage: { token: 'secret-session-token' },
};

const summary: P45AuthStateSummaryView = buildP45AuthStateSummary(secretBearingRawAuthState);
assertEqual(summary.cookieCount, 2, 'auth summary should expose cookie count');
assertEqual(summary.originCount, 1, 'auth summary should expose origin count');
assertDeepEqual(summary.origins, ['https://example.invalid'], 'auth summary should expose origins');
assertEqual(summary.capturedAt, authState.captured_at, 'auth summary should expose capture time');
assertOmitsSecrets(summary, 'auth summary');

const savedAuthView: P45PageManagementView = buildP45PageManagementView({
	pages: [page],
	authState: summary,
});
assertOneOf(savedAuthView.layout, ['list', 'table'], 'page manager should use a scan-friendly list/table layout');
const savedAuthRow = savedAuthView.rows.find((row) => row.pageId === page.id);
assertPresent(savedAuthRow, 'page manager should render a row for each page');
assertHasAction(savedAuthRow.actions, 'login_flow', 'clean');
assertHasAction(savedAuthRow.actions, 'business_flow', 'project_saved');
assertHasAction(savedAuthRow.actions, 'business_flow', 'clean');
assertEqual(
  savedAuthView.authStateActions.some((action) => String(action.kind) === 'capture_project_auth_state'),
  false,
  'page management must not expose an unscoped direct auth-state Capture action',
);
assertOmitsSecrets(savedAuthView, 'page management view');
assertFunction(
	projectApi.getLatestPageRecording,
	'projectApi should expose latest PageScript recording detail API',
);
const recordingDetailView = buildP45RecordingDetailView({
	diagnostics: {
		action_count: 10,
		snapshot_element_count: 0,
		quality_codes: ['recording_snapshot_unusable'],
		parse_errors: [],
		sensitive_fields_removed: ['localStorage'],
	},
});
assertEqual(recordingDetailView.actionCount, 10, 'recording detail view should expose action count');
assertEqual(recordingDetailView.snapshotElementCount, 0, 'recording detail view should expose snapshot element count');
assertEqual(recordingDetailView.status, 'warning', 'empty snapshot should be displayed as a warning');
assertDeepEqual(recordingDetailView.sensitiveFieldsRemoved, ['localStorage'], 'recording detail view should expose removed sensitive fields');

const missingAuthView: P45PageManagementView = buildP45PageManagementView({
	pages: [page],
	authState: null,
});
const missingAuthRow = missingAuthView.rows.find((row) => row.pageId === page.id);
assertPresent(missingAuthRow, 'page manager should render rows without auth state');
const projectSavedAction = findAction(missingAuthRow.actions, 'business_flow', 'project_saved');
assertEqual(projectSavedAction.disabled, true, 'business_flow + project_saved should be blocked without auth state');
assertEqual(
  projectSavedAction.guidance?.kind,
  'capture_auth_state_or_choose_clean',
  'blocked business recording should guide users to update auth state or choose clean',
);

const startCalls: P45StartRecordingCall[] = [];
const navigations: string[] = [];
const controller = createP45RecordingController({
	projectId,
	versionId,
	operationLedger: createRecordingOperationLedger(),
	api: {
		startPageRecordingSession: async (
			callProjectId: number,
			callVersionId: number,
			callPageId: number,
			payload: P45StartRecordingCall['payload'],
		) => {
			startCalls.push({
				projectId: callProjectId,
				versionId: callVersionId,
        pageId: callPageId,
        payload,
			});
			return {
				data: {
					recording_session_id: 'contract-session',
					recording_meta: {
						target_url: 'https://example.invalid/orders',
						auth_state_id: 42,
					},
				},
			};
		},
	},
	navigate: (to: string) => {
		navigations.push(to);
	},
});

await controller.startRecording({
  pageId: page.id,
  recordingKind: 'business_flow',
  authContext: 'project_saved',
  authStateId: 99,
  targetUrl: 'https://example.invalid/orders',
});

const firstStartCall = startCalls[0];
assertEqual(firstStartCall.projectId, projectId, 'start recording should retain project scope');
assertEqual(firstStartCall.versionId, versionId, 'start recording should retain version scope');
assertEqual(firstStartCall.pageId, page.id, 'start recording should retain page scope');
assertEqual(firstStartCall.payload.recording_kind, 'business_flow', 'start recording should retain recording_kind');
assertEqual(firstStartCall.payload.auth_context, 'project_saved', 'start recording should retain auth_context');
assertEqual(firstStartCall.payload.target_url, 'https://example.invalid/orders', 'start recording should retain target_url');
assertEqual(firstStartCall.payload.browser_instance_id, 'default', 'start recording should bind the browser instance');
assertEqual(firstStartCall.payload.runtime_page_id, `project-page:${page.id}`, 'start recording should bind the runtime page identity');
if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(firstStartCall.payload.operation_id || '')) {
  throw new Error(`start recording operation_id is not a UUID: ${firstStartCall.payload.operation_id}`);
}
assertEqual(navigations.length, 1, 'start recording should navigate into the browser recording surface');
const recordingNavigation = new URL(navigations[0], 'https://browserwing.local');
assertEqual(
  recordingNavigation.searchParams.get('recordingSessionId'),
  'contract-session',
  'start recording navigation should retain recording_session_id for stop/save',
);
assertEqual(
  recordingNavigation.searchParams.get('authStateId'),
  '42',
  'start recording navigation should prefer the recovered session auth_state_id over the current page value',
);

const responseLossCalls: P45StartRecordingCall[] = [];
let responseLossAttempt = 0;
const responseLossLedger = createRecordingOperationLedger();
const responseLossAPI = {
  startPageRecordingSession: async (callProjectId: number, callVersionId: number, callPageId: number, payload: P45StartRecordingCall['payload']) => {
    responseLossCalls.push({ projectId: callProjectId, versionId: callVersionId, pageId: callPageId, payload });
    responseLossAttempt += 1;
    if (responseLossAttempt === 1) throw new Error('response lost after server commit');
    return { data: { recording_session_id: 'response-loss-session', recording_meta: { recording_kind: 'login_flow' as const, auth_context: 'clean' as const } } };
  },
};
const responseLossController = createP45RecordingController({
  projectId,
  versionId,
  operationLedger: responseLossLedger,
  api: responseLossAPI,
  navigate: () => undefined,
});
try {
  await responseLossController.startRecording({ pageId: page.id, recordingKind: 'login_flow', authContext: 'clean' });
  throw new Error('response-loss first request should reject');
} catch (error) {
  if (!(error instanceof Error) || error.message !== 'response lost after server commit') throw error;
}
await assertRejects(
  () => responseLossController.startRecording({ pageId: page.id, recordingKind: 'login_flow', authContext: 'clean', targetUrl: 'https://example.invalid/changed' }),
  'a pending Start whose input changed locally must not reuse its operation id',
);
assertEqual(responseLossCalls.length, 1, 'changed pending input must not create an HTTP request');
const responseLossRetryController = createP45RecordingController({
  projectId,
  versionId,
  operationLedger: responseLossLedger,
  api: responseLossAPI,
  navigate: () => undefined,
});
await responseLossRetryController.startRecording({ pageId: page.id, recordingKind: 'login_flow', authContext: 'clean' });
assertEqual(responseLossCalls.length, 2, 'response-loss retry should issue two HTTP requests');
assertEqual(responseLossCalls[1].payload.operation_id, responseLossCalls[0].payload.operation_id, 'response-loss retry must reuse Start operation_id');
assertDeepEqual(responseLossCalls[1].payload, responseLossCalls[0].payload, 'response-loss retry must replay the frozen Start payload');

await controller.startRecording({
  pageId: page.id,
  recordingKind: 'business_flow',
  authContext: 'clean',
  targetUrl: '/orders',
});
assertEqual(
  'target_url' in startCalls[1].payload,
  false,
  'relative page paths should let the backend resolve the project target_url',
);

const resumedNavigations: string[] = [];
const resumedController = createP45RecordingController({
  projectId,
  versionId,
  operationLedger: createRecordingOperationLedger(),
  api: {
    startPageRecordingSession: async () => {
      throw {
        response: {
          status: 409,
          data: {
            error: 'recording_session_active',
            recording_session_id: 'active-session',
            page_id: page.id,
            recording_meta: {
              recording_kind: 'login_flow',
              auth_context: 'clean',
              auth_state_id: null,
              target_url: 'https://example.invalid/app/login',
            },
          },
        },
      };
    },
  },
  navigate: (to: string) => {
    resumedNavigations.push(to);
  },
});

await resumedController.startRecording({
  pageId: page.id,
  recordingKind: 'login_flow',
  authContext: 'clean',
  targetUrl: 'https://example.invalid/app/login',
});
const resumedRecordingNavigation = new URL(resumedNavigations[0], 'https://browserwing.local');
assertEqual(
  resumedRecordingNavigation.searchParams.get('recordingSessionId'),
  'active-session',
  'an active recording response should resume the existing browser recording session',
);

const foreignSessionNavigations: string[] = [];
const foreignSessionController = createP45RecordingController({
  projectId,
  versionId,
  operationLedger: createRecordingOperationLedger(),
  api: {
    startPageRecordingSession: async () => {
      throw {
        response: {
          status: 409,
          data: {
            error: 'recording_session_active',
            recording_session_id: 'other-page-session',
            page_id: page.id + 1,
            recording_meta: {
              recording_kind: 'login_flow',
              auth_context: 'clean',
              auth_state_id: null,
              target_url: 'https://example.invalid/app/login',
            },
          },
        },
      };
    },
  },
  navigate: (to: string) => {
    foreignSessionNavigations.push(to);
  },
});

await assertRejects(
  () => foreignSessionController.startRecording({
    pageId: page.id,
    recordingKind: 'login_flow',
    authContext: 'clean',
    targetUrl: 'https://example.invalid/app/login',
  }),
  'an active session from another page must not be resumed',
);
assertEqual(
  foreignSessionNavigations.length,
  0,
  'a foreign active session must not navigate the current page into the browser recording surface',
);

const savePayload = buildP45SaveRecordingPayload({
  name: '登录主流程',
  actionTrace: '{"steps":[]}',
  domSnapshot: '{"elements":[]}',
  recordingMeta: {
    schema_version: 1,
    recording_kind: 'login_flow',
    auth_context: 'clean',
    auth_state_id: null,
    target_url: 'https://example.invalid/orders',
  },
  recordingSessionId: 'contract-session',
  retainAuthSnapshot: true,
});
assertEqual(savePayload.recording_meta.auth_context, 'clean', 'save recording should include recording_meta');
assertEqual(savePayload.recording_session_id, 'contract-session', 'save recording should bind to RecordingSession');
assertEqual(savePayload.retain_auth_snapshot, true, 'save-and-capture should retain its auth snapshot until capture succeeds');
assertOmitsSecrets(savePayload, 'save recording payload');

const projectCancelEvents: string[] = [];
let resolveProjectCancel: ((value: { data: StopPageRecordingSessionResponse }) => void) | undefined;
const projectCancelController = browserManagerContract.createRecordingCancelController({
	api: {
		cancelPageRecordingSession: async (
			callProjectId: number,
			callVersionId: number,
			callPageId: number,
			callSessionId: string,
		) => {
			projectCancelEvents.push(`cancel:${callProjectId}:${callVersionId}:${callPageId}:${callSessionId}`);
			return new Promise((resolve) => {
				resolveProjectCancel = resolve;
			});
		},
	},
	clearLocalRecordingState: () => {
		projectCancelEvents.push('clear-local');
	},
	setProjectRecordingSession: (session) => {
		projectCancelEvents.push(`session:${session.status}`);
	},
});
const projectCancelPromise = projectCancelController.cancelRecording({
	isProjectRecordingContext: true,
	projectId,
	versionId,
	pageId: page.id,
	recordingSessionId: 'contract-session',
});
await Promise.resolve();
assertDeepEqual(
	projectCancelEvents,
	[`cancel:${projectId}:${versionId}:${page.id}:contract-session`],
	'project recording cancel should call backend before clearing local state',
);
assertPresent(resolveProjectCancel, 'project cancel API was not awaited');
resolveProjectCancel({
	data: {
		id: 99,
		recording_session_id: 'contract-session',
		project_id: projectId,
		version_id: versionId,
		page_id: page.id,
		recording_kind: 'business_flow',
		auth_context: 'clean',
		target_url: 'https://example.invalid/orders',
		status: 'cancelled',
		action_count: 0,
	},
});
await projectCancelPromise;
assertDeepEqual(
	projectCancelEvents,
	[
		`cancel:${projectId}:${versionId}:${page.id}:contract-session`,
		'session:cancelled',
		'clear-local',
	],
	'project recording cancel should clear local dialog/actions/state only after backend success',
);

const discardOnlyController = browserManagerContract.createRecordingCancelController({
	api: {
		cancelPageRecordingSession: async () => ({
			data: {
				id: 100,
				recording_session_id: 'saved-login-session',
				project_id: projectId,
				version_id: versionId,
				page_id: page.id,
				recording_kind: 'login_flow',
				auth_context: 'clean',
				target_url: 'https://example.invalid/app/login',
				status: 'saved',
				action_count: 1,
				auth_snapshot_discarded: true,
			},
		}),
	},
	clearLocalRecordingState: () => undefined,
});
const discardedSnapshotSession = await discardOnlyController.cancelRecording({
	isProjectRecordingContext: true,
	projectId,
	versionId,
	pageId: page.id,
	recordingSessionId: 'saved-login-session',
});
assertEqual(
	discardedSnapshotSession?.auth_snapshot_discarded,
	true,
	'discard-only cancellation should return the saved session marker to the caller',
);

const failedProjectCancelEvents: string[] = [];
const failedProjectCancelController = browserManagerContract.createRecordingCancelController({
	api: {
		cancelPageRecordingSession: async () => {
			failedProjectCancelEvents.push('cancel-called');
			throw new Error('cancel failed');
		},
	},
	clearLocalRecordingState: () => {
		failedProjectCancelEvents.push('clear-local');
	},
});
await assertRejects(
	() =>
		failedProjectCancelController.cancelRecording({
			isProjectRecordingContext: true,
			projectId,
			versionId,
			pageId: page.id,
			recordingSessionId: 'contract-session',
		}),
	'project cancel API failure should surface to the caller',
);
assertDeepEqual(
	failedProjectCancelEvents,
	['cancel-called'],
	'project recording cancel must not clear local state before backend success',
);

const localCancelEvents: string[] = [];
const localCancelController = browserManagerContract.createRecordingCancelController({
	api: {
		cancelPageRecordingSession: async () => {
			localCancelEvents.push('unexpected-project-cancel');
			throw new Error('non-project recording must not call project cancel API');
		},
	},
	clearLocalRecordingState: () => {
		localCancelEvents.push('clear-local');
	},
});
await localCancelController.cancelRecording({
	isProjectRecordingContext: false,
	recordingSessionId: null,
});
assertDeepEqual(
	localCancelEvents,
	['clear-local'],
	'non-project legacy recording cancel should keep the old local cleanup path without project cancel API',
);

function findAction(
	actions: P45RecordingActionView[],
	recordingKind: P45RecordingKind,
	authContext: P45AuthContext,
) {
  const action = actions.find((candidate) => candidate.recordingKind === recordingKind && candidate.authContext === authContext);
  assertPresent(action, `missing ${recordingKind} + ${authContext} action`);
  return action;
}

function assertHasAction(
	actions: P45RecordingActionView[],
	recordingKind: P45RecordingKind,
	authContext: P45AuthContext,
) {
  findAction(actions, recordingKind, authContext);
}

function assertPresent<T>(value: T | null | undefined, message: string): asserts value is T {
	if (value == null) {
		throw new Error(message);
	}
}

function assertComponent(value: unknown, name: string) {
	if (typeof value !== 'function') {
		throw new Error(`${name} should remain a renderable React component`);
	}
}

function assertEqual<T>(actual: T, expected: T, message: string) {
	if (actual !== expected) {
		throw new Error(`${message}; got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
  }
}

function assertOneOf<T>(actual: T, expected: readonly T[], message: string) {
  if (!expected.includes(actual)) {
    throw new Error(`${message}; got ${JSON.stringify(actual)}, want one of ${JSON.stringify(expected)}`);
  }
}

function assertDeepEqual(actual: unknown, expected: unknown, message: string) {
  const actualJSON = JSON.stringify(actual);
  const expectedJSON = JSON.stringify(expected);
  if (actualJSON !== expectedJSON) {
    throw new Error(`${message}; got ${actualJSON}, want ${expectedJSON}`);
	}
}

function assertSameReference<T>(actual: T, expected: T, message: string) {
	if (actual !== expected) {
		throw new Error(message);
	}
}

function assertFunction(value: unknown, message: string): asserts value is (...args: never[]) => unknown {
	if (typeof value !== 'function') {
		throw new Error(message);
	}
}

async function assertRejects(action: () => Promise<unknown>, message: string) {
	try {
		await action();
	} catch {
		return;
	}
	throw new Error(message);
}

function assertOmitsSecrets(value: unknown, label: string) {
	const serialized = JSON.stringify(value);
  for (const secret of ['secret-cookie-value', 'secret-local-token', 'secret-session-token']) {
    if (serialized.includes(secret)) {
      throw new Error(`${label} leaked ${secret}`);
    }
  }
}
