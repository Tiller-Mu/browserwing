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

  // 页面管理
  getPages: (projectId: number, versionId: number) => 
    client.get<TestPage[]>(`/projects/${projectId}/versions/${versionId}/pages`),
  createPage: (projectId: number, versionId: number, data: { name: string; path: string; description: string }) =>
    client.post<TestPage>(`/projects/${projectId}/versions/${versionId}/pages`, data),
  deletePage: (projectId: number, versionId: number, pageId: number) =>
    client.delete<{ message: string }>(`/projects/${projectId}/versions/${versionId}/pages/${pageId}`),
  savePageRecording: (projectId: number, versionId: number, pageId: number, data: { name: string; action_trace: string; dom_snapshot: string }) =>
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
