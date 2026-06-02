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
  test_cases?: any[];
  created_at: string;
  updated_at: string;
}

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
