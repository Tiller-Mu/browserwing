import { client } from './client';

export interface ProjectVersion {
  id: number;
  project_id: number;
  version_name: string;
  description: string;
  base_url: string;
  created_at: string;
  updated_at: string;
}

export interface Project {
  id: number;
  name: string;
  description: string;
  base_url?: string;
  created_at: string;
  updated_at: string;
  versions: ProjectVersion[];
}

export type P45RecordingKind = 'login_flow' | 'business_flow';
export type P45AuthContext = 'clean' | 'project_saved';

export interface P45RecordingMeta {
  schema_version: 1;
  recording_kind: P45RecordingKind;
  auth_context: P45AuthContext;
  auth_state_id: number | null;
  target_url: string;
  started_at?: string;
  ended_at?: string;
}

export interface ProjectAuthStateSummary {
  id: number;
  project_id: number;
  version_id: number;
  name: string;
  status: 'active' | 'expired' | 'disabled';
  schema_version: number;
  state_digest: string;
  origins: string[];
  cookie_count: number;
  origin_count: number;
  captured_url: string;
  captured_page_id: number;
  captured_at: string;
  last_validated_at?: string | null;
  invalid_reason?: string;
  created_at: string;
  updated_at: string;
}

export interface ProjectAuthStateResponse {
  auth_state: ProjectAuthStateSummary | null;
}

export interface CaptureProjectAuthStateRequest {
  name?: string;
  captured_page_id?: number;
  captured_url?: string;
  origin_allowlist?: string[];
  replace?: boolean;
  browser_instance_id?: string;
}

export interface StartPageRecordingSessionRequest {
  recording_kind: P45RecordingKind;
  auth_context: P45AuthContext;
  browser_instance_id?: string;
}

export interface PageRecordingSessionResponse {
  recording_session_id: string;
  recording_meta?: P45RecordingMeta;
  auth_state?: ProjectAuthStateSummary;
}

export interface PageRecordingContextResponse {
  page: TestPage;
  target_url: string;
  auth_state: ProjectAuthStateSummary | null;
}

export interface SavePageRecordingRequest {
  name: string;
  action_trace: string;
  dom_snapshot: string;
  recording_meta: P45RecordingMeta;
}

export const projectApi = {
  // 项目管理
  getProjects: () => client.get<Project[]>('/projects'),
  
  createProject: (data: { name: string; description: string; base_url: string }) => 
    client.post<Project>('/projects', data),
    
  deleteProject: (id: number) => 
    client.delete<{ message: string }>(`/projects/${id}`),

  // 版本管理
  createVersion: (projectId: number, data: { version_name: string; description: string; base_url: string }) =>
    client.post<ProjectVersion>(`/projects/${projectId}/versions`, data),
    
  updateVersion: (projectId: number, versionId: number, data: { version_name?: string; description?: string; base_url?: string }) =>
    client.put<ProjectVersion>(`/projects/${projectId}/versions/${versionId}`, data),
    
  deleteVersion: (projectId: number, versionId: number) =>
    client.delete<{ message: string }>(`/projects/${projectId}/versions/${versionId}`),
    
  cloneVersion: (projectId: number, versionId: number, newVersionName: string) =>
    client.post<ProjectVersion>(`/projects/${projectId}/versions/${versionId}/clone`, { new_version_name: newVersionName }),

  getProjectAuthState: (projectId: number, versionId: number) =>
    client.get<ProjectAuthStateResponse>(`/projects/${projectId}/versions/${versionId}/auth-state`),
  captureProjectAuthState: (projectId: number, versionId: number, data: CaptureProjectAuthStateRequest) =>
    client.post<ProjectAuthStateResponse>(`/projects/${projectId}/versions/${versionId}/auth-state/capture`, data),
  deleteProjectAuthState: (projectId: number, versionId: number) =>
    client.delete<{ message: string }>(`/projects/${projectId}/versions/${versionId}/auth-state`),

  // 页面管理
  getPages: (projectId: number, versionId: number) => 
    client.get<TestPage[]>(`/projects/${projectId}/versions/${versionId}/pages`),
  createPage: (projectId: number, versionId: number, data: { name: string; path: string; description: string }) =>
    client.post<TestPage>(`/projects/${projectId}/versions/${versionId}/pages`, data),
  deletePage: (projectId: number, versionId: number, pageId: number) =>
    client.delete<{ message: string }>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}`),
  getPageRecordingContext: (projectId: number, versionId: number, pageId: number) =>
    client.get<PageRecordingContextResponse>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/recording-context`),
  startPageRecordingSession: (projectId: number, versionId: number, pageId: number, data: StartPageRecordingSessionRequest) =>
    client.post<PageRecordingSessionResponse>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/recording-session`, data),
  savePageRecording: (projectId: number, versionId: number, pageId: number, data: SavePageRecordingRequest) =>
    client.post<{ message: string; script: any }>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/recordings`, data),
  listTestCases: (projectId: number, versionId: number, pageId: number) =>
    client.get<ListTestCasesResponse>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases`),
  createTestCase: (projectId: number, versionId: number, pageId: number, data: CreateTestCaseRequest) =>
    client.post<{ test_case: TestCaseDetail }>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases`, data),
  getTestCase: (projectId: number, versionId: number, pageId: number, testCaseId: number) =>
    client.get<{ test_case: TestCaseDetail }>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases/${testCaseId}`),
  updateTestCase: (projectId: number, versionId: number, pageId: number, testCaseId: number, data: UpdateTestCaseRequest) =>
    client.put<{ test_case: TestCaseDetail }>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases/${testCaseId}`, data),
  deleteTestCase: (projectId: number, versionId: number, pageId: number, testCaseId: number) =>
    client.delete<{ message: string }>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases/${testCaseId}`),
  runTestCase: (projectId: number, versionId: number, pageId: number, testCaseId: number, data: RunTestCaseRequest) =>
    client.post<{ execution: TestExecutionDetail }>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases/${testCaseId}/run`, data),
  listTestCaseExecutions: (projectId: number, versionId: number, pageId: number, testCaseId: number, limit = 20) =>
    client.get<ListTestCaseExecutionsResponse>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases/${testCaseId}/executions`, { params: { limit } }),
  getTestCaseExecution: (projectId: number, versionId: number, pageId: number, testCaseId: number, executionId: number) =>
    client.get<{ execution: TestExecutionDetail }>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases/${testCaseId}/executions/${executionId}`),
  refineTestCase: (projectId: number, versionId: number, pageId: number, testCaseId: number, data: RefineTestCaseRequest) =>
    client.post<{ refinement: TestCaseRefinementDetail }>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases/${testCaseId}/refine`, data),
  listTestCaseRefinements: (projectId: number, versionId: number, pageId: number, testCaseId: number) =>
    client.get<ListTestCaseRefinementsResponse>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases/${testCaseId}/refinements`),
  getTestCaseRefinement: (projectId: number, versionId: number, pageId: number, testCaseId: number, refinementId: number) =>
    client.get<{ refinement: TestCaseRefinementDetail }>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases/${testCaseId}/refinements/${refinementId}`),
  applyTestCaseRefinement: (projectId: number, versionId: number, pageId: number, testCaseId: number, refinementId: number) =>
    client.post<{ test_case: TestCaseDetail; refinement: TestCaseRefinementStatusResponse }>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases/${testCaseId}/refinements/${refinementId}/apply`, {}),
  discardTestCaseRefinement: (projectId: number, versionId: number, pageId: number, testCaseId: number, refinementId: number) =>
    client.post<{ refinement: TestCaseRefinementStatusResponse }>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases/${testCaseId}/refinements/${refinementId}/discard`, {}),
  generateTestCases: (projectId: number, versionId: number, pageId: number, data: GenerateTestCasesRequest) =>
    client.post<GenerateTestCasesResponse>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases/generate`, data)
};

export interface TestPage {
  id: number;
  version_id: number;
  name: string;
  path: string;
  description: string;
  scripts?: any[];
  test_cases?: TestCaseSummary[];
  created_at: string;
  updated_at: string;
}

export type TestCaseStatus = 'active' | 'draft' | 'archived';

export interface TestCase {
  id?: number;
  page_id?: number;
  title: string;
  description: string;
  blueprint: string | Record<string, any>;
  script_content?: string;
  status?: string;
  created_at?: string;
  updated_at?: string;
}

export interface TestCaseSummary {
  id: number;
  page_id: number;
  title: string;
  description: string;
  status: TestCaseStatus;
  created_at: string;
  updated_at: string;
}

export interface TestCaseDetail extends TestCaseSummary {
  blueprint: Record<string, any>;
  script_content: string;
}

export interface ListTestCasesResponse {
  test_cases: TestCaseSummary[];
  count: number;
}

export interface CreateTestCaseRequest {
  title: string;
  description?: string;
  status?: TestCaseStatus;
  blueprint: Record<string, any>;
  script_content?: string;
}

export interface UpdateTestCaseRequest {
  title?: string;
  description?: string;
  status?: TestCaseStatus;
  blueprint?: Record<string, any>;
  script_content?: string;
}

export interface GenerateTestCasesRequest {
  mode: 'append' | 'replace' | 'preview';
  llm_config_id?: string;
  instruction?: string;
}

export interface GenerateTestCasesResponse {
  mode: 'append' | 'replace' | 'preview';
  saved: boolean;
  generated_count: number;
  test_cases: TestCase[];
}

export type TestExecutionStatus = 'passed' | 'failed' | 'error';

export interface RunTestCaseRequest {
  browser_instance_id?: string;
  headless?: boolean;
  stop_on_failure?: boolean;
  capture_screenshot?: boolean;
  auth_context?: P45AuthContext;
}

export interface TestExecutionSummary {
  id: number;
  test_case_id: number;
  status: TestExecutionStatus;
  error_message: string;
  duration_ms: number;
  created_at: string;
}

export interface ExecutionReportStep {
  index: number;
  action: string;
  description?: string;
  status: TestExecutionStatus;
  started_at?: string;
  ended_at?: string;
  duration_ms?: number;
  target_summary?: string;
  error?: string;
}

export interface ExecutionReportData {
  schema_version: number;
  source: 'blueprint';
  execution_url?: string;
  initial_navigation?: {
    mode: 'default' | 'explicit_step';
    url?: string;
    step_index: number | null;
  };
  duration_ms?: number;
  summary?: {
    total_steps: number;
    passed_steps: number;
    failed_steps: number;
    failed_step_index: number | null;
  };
  steps?: ExecutionReportStep[];
  artifacts?: {
    screenshots?: string[];
  };
  final_url?: string;
  auth_context?: P45AuthContext;
  auth_context_source?: 'request' | 'blueprint' | 'legacy_default';
  auth_state?: ProjectAuthStateSummary;
}

export interface TestExecutionDetail extends TestExecutionSummary {
  report_data: ExecutionReportData;
}

export interface ListTestCaseExecutionsResponse {
  executions: TestExecutionSummary[];
  count: number;
}

export type TestCaseRefinementStatus = 'proposed' | 'applied' | 'discarded';

export interface RefineTestCaseRequest {
  prompt: string;
  llm_config_id?: string;
  execution_id?: number;
}

export interface TestCaseRefinementSummary {
  id: number;
  test_case_id: number;
  user_prompt: string;
  summary: string;
  risk_notes: string;
  status: TestCaseRefinementStatus;
  applied_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface TestCaseRefinementDetail extends TestCaseRefinementSummary {
  original_blueprint: Record<string, any>;
  refined_blueprint: Record<string, any>;
}

export interface TestCaseRefinementStatusResponse {
  id: number;
  test_case_id: number;
  status: TestCaseRefinementStatus;
  applied_at: string | null;
}

export interface ListTestCaseRefinementsResponse {
  refinements: TestCaseRefinementSummary[];
  count: number;
}
