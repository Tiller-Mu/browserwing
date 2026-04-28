import { client } from './client';

export interface ProjectVersion {
  id: number;
  project_id: number;
  version_name: string;
  description: string;
  created_at: string;
  updated_at: string;
}

export interface Project {
  id: number;
  name: string;
  description: string;
  base_url: string;
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
  createVersion: (projectId: number, data: { version_name: string; description: string }) =>
    client.post<ProjectVersion>(`/projects/${projectId}/versions`, data),
    
  deleteVersion: (projectId: number, versionId: number) =>
    client.delete<{ message: string }>(`/projects/${projectId}/versions/${versionId}`),
    
  cloneVersion: (projectId: number, versionId: number, newVersionName: string) =>
    client.post<ProjectVersion>(`/projects/${projectId}/versions/${versionId}/clone`, { new_version_name: newVersionName })
};
