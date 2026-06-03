import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const root = dirname(fileURLToPath(import.meta.url));
const pageManager = readFileSync(join(root, 'src/pages/TestPageManager.tsx'), 'utf8');
const browserManager = readFileSync(join(root, 'src/pages/BrowserManager.tsx'), 'utf8');

mustMatch(
  pageManager,
  /(<table|role=["']table["']|display:\s*['"]table|grid-cols-\[)/,
  'TestPageManager should render pages as a compact list/table scanning surface',
);
mustNotMatch(
  pageManager,
  /grid\s+grid-cols-1\s+xl:grid-cols-2/,
  'TestPageManager should not keep the old large card grid as the primary page scanning surface',
);

mustMatch(
  pageManager,
  /recording_kind[^]+login_flow|录制登录流程|Record Login/i,
  'Each page row should expose a login-flow recording action',
);
mustMatch(
  pageManager,
  /recording_kind[^]+business_flow|录制业务流程|Record Business/i,
  'Each page row should expose a business-flow recording action',
);
mustMatch(
  pageManager,
  /auth_state|authState|项目登录态|Project Auth/i,
  'Project page manager should show a project auth-state summary',
);
mustNotMatch(
  pageManager,
  /secret-cookie-value|secret-local-token|secret-session-token/,
  'Project auth-state summary must not render sensitive storage values',
);

mustMatch(
  pageManager,
  /recording_kind[^]+auth_context|auth_context[^]+recording_kind/,
  'Starting a page recording session should send recording_kind and auth_context',
);
mustMatch(
  browserManager,
  /recording_meta/,
  'Saving a page recording should include recording_meta',
);

function mustMatch(source, pattern, message) {
  assert.ok(pattern.test(source), message);
}

function mustNotMatch(source, pattern, message) {
  assert.ok(!pattern.test(source), message);
}
