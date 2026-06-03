import type { TestPage } from './api/project';
import BrowserManager, { p45BrowserManagerContract } from './pages/BrowserManager';
import {
	buildP45AuthStateSummary,
	buildP45PageManagementView,
	buildP45SaveRecordingPayload,
	createP45RecordingController,
} from './pages/p45RecordingUiContract';
import TestPageManager, { p45TestPageManagerContract } from './pages/TestPageManager';

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
	rows: P45PageManagementRowView[];
}

interface P45StartRecordingCall {
	projectId: number;
	versionId: number;
	pageId: number;
  payload: {
    recording_kind: P45RecordingKind;
    auth_context: P45AuthContext;
	};
}

interface P45TestPageManagerPageContract {
	buildAuthStateSummary: typeof buildP45AuthStateSummary;
	buildPageManagementView: typeof buildP45PageManagementView;
	createRecordingController: typeof createP45RecordingController;
}

interface P45BrowserManagerPageContract {
	buildSaveRecordingPayload: typeof buildP45SaveRecordingPayload;
}

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
assertSameReference(
	pageManagerContract.buildPageManagementView,
	buildP45PageManagementView,
	'TestPageManager should consume the P4.5 page management view helper',
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
assertOmitsSecrets(savedAuthView, 'page management view');

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
			return { data: { recording_session_id: 'contract-session' } };
		},
	},
	navigate: (to: string) => {
		navigations.push(to);
	},
});

await controller.startRecording({
  pageId: page.id,
  recordingKind: 'business_flow',
  authContext: 'clean',
});

assertDeepEqual(
  startCalls[0],
  {
    projectId,
    versionId,
    pageId: page.id,
    payload: {
      recording_kind: 'business_flow',
      auth_context: 'clean',
    },
  },
  'start recording should call API with recording_kind and auth_context',
);
assertEqual(navigations.length, 1, 'start recording should navigate into the browser recording surface');

const savePayload = buildP45SaveRecordingPayload({
  name: '业务主流程',
  actionTrace: '{"steps":[]}',
  domSnapshot: '{"elements":[]}',
  recordingMeta: {
    schema_version: 1,
    recording_kind: 'business_flow',
    auth_context: 'clean',
    auth_state_id: null,
    target_url: 'https://example.invalid/orders',
  },
});
assertEqual(savePayload.recording_meta.auth_context, 'clean', 'save recording should include recording_meta');
assertOmitsSecrets(savePayload, 'save recording payload');

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

function assertOmitsSecrets(value: unknown, label: string) {
	const serialized = JSON.stringify(value);
  for (const secret of ['secret-cookie-value', 'secret-local-token', 'secret-session-token']) {
    if (serialized.includes(secret)) {
      throw new Error(`${label} leaked ${secret}`);
    }
  }
}
