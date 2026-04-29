import { useState, useEffect } from 'react'
import { Folder, Plus, Trash2, GitBranch, Copy, ChevronDown, ChevronRight, Settings, ExternalLink } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { projectApi, Project, ProjectVersion } from '../api/project'
import Toast from '../components/Toast'
import { Modal } from '../components/Modal'
import { useLanguage } from '../i18n'

export default function ProjectManager() {
  const { t } = useLanguage()
  const navigate = useNavigate()
  
  const [projects, setProjects] = useState<Project[]>([])
  const [loading, setLoading] = useState(true)
  const [toast, setToast] = useState<{ message: string; type: 'success' | 'error' | 'info' } | null>(null)
  
  // Expanded rows
  const [expandedProjects, setExpandedProjects] = useState<Set<number>>(new Set())

  // Modals
  const [showCreateModal, setShowCreateModal] = useState(false)
  const [showCloneModal, setShowCloneModal] = useState<{ show: boolean; projectId: number; sourceVersion: ProjectVersion | null }>({
    show: false,
    projectId: 0,
    sourceVersion: null
  })
  const [showCreateVersionModal, setShowCreateVersionModal] = useState<{ show: boolean; projectId: number }>({
    show: false,
    projectId: 0
  })
  const [showEditVersionModal, setShowEditVersionModal] = useState<{ show: boolean; projectId: number; version: ProjectVersion | null }>({
    show: false,
    projectId: 0,
    version: null
  })

  // Forms
  const [newProject, setNewProject] = useState({ name: '', description: '', base_url: '' })
  const [newVersionName, setNewVersionName] = useState('')
  const [newEmptyVersion, setNewEmptyVersion] = useState({ version_name: '', description: '', base_url: '' })
  const [editVersionForm, setEditVersionForm] = useState({ version_name: '', description: '', base_url: '' })

  useEffect(() => {
    loadProjects()
  }, [])

  const loadProjects = async () => {
    try {
      setLoading(true)
      const response = await projectApi.getProjects()
      setProjects(response.data || [])
    } catch (error: any) {
      console.error('Failed to load projects:', error)
      showToast('获取项目列表失败', 'error')
    } finally {
      setLoading(false)
    }
  }

  const handleCreateProject = async () => {
    if (!newProject.name.trim()) return
    
    try {
      await projectApi.createProject(newProject)
      showToast('项目创建成功', 'success')
      setShowCreateModal(false)
      setNewProject({ name: '', description: '', base_url: '' })
      loadProjects()
    } catch (error: any) {
      showToast('项目创建失败', 'error')
    }
  }

  const handleDeleteProject = async (id: number) => {
    if (!window.confirm('确定要删除该项目及其所有版本和测试用例吗？此操作不可恢复。')) return

    try {
      await projectApi.deleteProject(id)
      showToast('删除成功', 'success')
      loadProjects()
    } catch (error: any) {
      showToast('删除失败', 'error')
    }
  }

  const handleCloneVersion = async () => {
    if (!showCloneModal.sourceVersion || !newVersionName.trim()) return

    try {
      await projectApi.cloneVersion(showCloneModal.projectId, showCloneModal.sourceVersion.id, newVersionName)
      showToast('版本克隆成功', 'success')
      setShowCloneModal({ show: false, projectId: 0, sourceVersion: null })
      setNewVersionName('')
      loadProjects()
    } catch (error: any) {
      showToast('版本克隆失败', 'error')
    }
  }

  const handleCreateEmptyVersion = async () => {
    if (!newEmptyVersion.version_name.trim()) return

    try {
      await projectApi.createVersion(showCreateVersionModal.projectId, newEmptyVersion)
      showToast('空版本分支创建成功', 'success')
      setShowCreateVersionModal({ show: false, projectId: 0 })
      setNewEmptyVersion({ version_name: '', description: '', base_url: '' })
      loadProjects()
    } catch (error: any) {
      console.error('Create version failed:', error)
      showToast('分支创建失败', 'error')
    }
  }

  const handleEditVersion = async () => {
    if (!showEditVersionModal.version || !editVersionForm.version_name.trim()) return

    try {
      await projectApi.updateVersion(showEditVersionModal.projectId, showEditVersionModal.version.id, editVersionForm)
      showToast('版本更新成功', 'success')
      setShowEditVersionModal({ show: false, projectId: 0, version: null })
      setEditVersionForm({ version_name: '', description: '', base_url: '' })
      loadProjects()
    } catch (error: any) {
      console.error('Update version failed:', error)
      showToast('版本更新失败', 'error')
    }
  }

  const handleDeleteVersion = async (projectId: number, versionId: number) => {
    if (!window.confirm('确定要删除该版本吗？这会清空该版本下的所有页面、脚本和用例资产，且不可恢复！')) return

    try {
      await projectApi.deleteVersion(projectId, versionId)
      showToast('版本删除成功', 'success')
      loadProjects()
    } catch (error: any) {
      showToast('版本删除失败', 'error')
    }
  }

  const toggleExpand = (id: number) => {
    const newExpanded = new Set(expandedProjects)
    if (newExpanded.has(id)) {
      newExpanded.delete(id)
    } else {
      newExpanded.add(id)
    }
    setExpandedProjects(newExpanded)
  }

  const showToast = (message: string, type: 'success' | 'error' | 'info' = 'info') => {
    setToast({ message, type })
  }

  return (
    <div className="space-y-6 lg:space-y-8 animate-fade-in">
      {/* Header */}
      <div className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-bold text-gray-900 dark:text-gray-100">项目与版本管理</h1>
            <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">
              管理企业级测试资产：配置测试域名，并利用版本树管理不同迭代的测试用例集
            </p>
          </div>
          <button
            onClick={() => setShowCreateModal(true)}
            className="flex items-center gap-2 px-4 py-2 bg-gray-900 dark:bg-gray-700 text-white rounded-lg hover:bg-gray-800 dark:hover:bg-gray-600 transition-colors"
          >
            <Plus className="w-4 h-4" />
            新建测试项目
          </button>
        </div>
      </div>

      {/* 项目列表 */}
      {loading ? (
        <div className="flex items-center justify-center py-12">
          <div className="text-gray-500 dark:text-gray-400">{t('common.loading')}</div>
        </div>
      ) : projects.length === 0 ? (
        <div className="bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 p-12 text-center">
          <Folder className="w-12 h-12 text-gray-300 dark:text-gray-600 mx-auto mb-4" />
          <div className="text-gray-500 dark:text-gray-400">
            暂无测试项目，请点击上方按钮新建
          </div>
        </div>
      ) : (
        <div className="space-y-4">
          {projects.map((project) => (
            <div key={project.id} className="bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 overflow-hidden transition-all hover:shadow-md">
              {/* 项目行 */}
              <div className="p-5 flex items-center justify-between group">
                <div className="flex items-center gap-4 flex-1 cursor-pointer" onClick={() => toggleExpand(project.id)}>
                  <button className="p-1 rounded-md text-gray-400 hover:text-gray-900 hover:bg-gray-100 dark:hover:text-gray-100 dark:hover:bg-gray-700">
                    {expandedProjects.has(project.id) ? <ChevronDown className="w-5 h-5" /> : <ChevronRight className="w-5 h-5" />}
                  </button>
                  <div className="p-2 bg-indigo-50 dark:bg-indigo-900/30 rounded-lg">
                    <Folder className="w-6 h-6 text-indigo-600 dark:text-indigo-400" />
                  </div>
                  <div>
                    <h3 className="text-lg font-semibold text-gray-900 dark:text-gray-100">{project.name}</h3>
                    <div className="flex items-center gap-3 text-sm text-gray-500 dark:text-gray-400 mt-1">
                      {project.base_url && (
                        <span className="flex items-center gap-1 font-mono">
                          <Settings className="w-3.5 h-3.5" /> {project.base_url}
                        </span>
                      )}
                      <span>包含 {project.versions?.length || 0} 个版本</span>
                    </div>
                  </div>
                </div>

                <div className="flex items-center gap-2 opacity-0 group-hover:opacity-100 transition-opacity">
                  <button
                    onClick={() => handleDeleteProject(project.id)}
                    className="p-2 text-red-600 hover:bg-red-50 dark:hover:bg-red-900/30 rounded-lg transition-colors"
                    title="删除项目"
                  >
                    <Trash2 className="w-4 h-4" />
                  </button>
                </div>
              </div>

              {/* 版本列表面板 */}
              {expandedProjects.has(project.id) && (
                <div className="bg-gray-50 dark:bg-gray-900/50 border-t border-gray-100 dark:border-gray-800 p-5">
                  <div className="mb-4 flex items-center justify-between">
                    <h4 className="text-sm font-medium text-gray-700 dark:text-gray-300 flex items-center gap-2">
                      <GitBranch className="w-4 h-4" />
                      版本分支记录
                    </h4>
                    <button
                      onClick={() => setShowCreateVersionModal({ show: true, projectId: project.id })}
                      className="text-xs flex items-center gap-1 px-2.5 py-1.5 bg-white dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded-md text-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-gray-700 transition-colors shadow-sm"
                    >
                      <Plus className="w-3.5 h-3.5" /> 新建空分支
                    </button>
                  </div>
                  
                  <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
                    {project.versions?.map((version) => (
                      <div key={version.id} className="bg-white dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded-lg p-4 flex flex-col justify-between">
                        <div>
                          <div className="flex items-center justify-between mb-2">
                            <span className="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200">
                              {version.version_name}
                            </span>
                            <div className="flex items-center gap-1">
                              <button
                                onClick={() => {
                                  setEditVersionForm({ version_name: version.version_name, description: version.description || '', base_url: version.base_url || '' });
                                  setShowEditVersionModal({ show: true, projectId: project.id, version });
                                }}
                                className="p-1 text-gray-400 hover:text-indigo-600 dark:hover:text-indigo-400 hover:bg-indigo-50 dark:hover:bg-indigo-900/30 rounded transition-colors"
                                title="编辑版本分支"
                              >
                                <Settings className="w-4 h-4" />
                              </button>
                              <button
                                onClick={() => setShowCloneModal({ show: true, projectId: project.id, sourceVersion: version })}
                                className="p-1 text-gray-400 hover:text-indigo-600 dark:hover:text-indigo-400 hover:bg-indigo-50 dark:hover:bg-indigo-900/30 rounded transition-colors"
                                title="基于此版本克隆出新分支"
                              >
                                <Copy className="w-4 h-4" />
                              </button>
                              <button
                                onClick={() => handleDeleteVersion(project.id, version.id)}
                                className="p-1 text-gray-400 hover:text-red-600 dark:hover:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/30 rounded transition-colors"
                                title="删除该版本分支"
                              >
                                <Trash2 className="w-4 h-4" />
                              </button>
                            </div>
                          </div>
                          {version.base_url && (
                            <p className="text-xs text-indigo-600 dark:text-indigo-400 mt-1 flex items-center gap-1 font-mono break-all">
                              <ExternalLink className="w-3 h-3" /> {version.base_url}
                            </p>
                          )}
                          <p className="text-xs text-gray-500 dark:text-gray-400 mt-1 line-clamp-2">
                            {version.description || "无描述信息"}
                          </p>
                        </div>
                        <div className="mt-4 pt-3 border-t border-gray-100 dark:border-gray-700 flex justify-end">
                           <button 
                             onClick={() => navigate(`/projects/${project.id}/versions/${version.id}/pages`)}
                             className="text-xs font-medium text-indigo-600 dark:text-indigo-400 hover:underline"
                           >
                             进入录制与用例管理 →
                           </button>
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </div>
          ))}
        </div>
      )}

      {/* 新建项目弹窗 */}
      <Modal
        isOpen={showCreateModal}
        onClose={() => setShowCreateModal(false)}
        title="新建测试项目"
      >
        <div className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              项目名称 <span className="text-red-500">*</span>
            </label>
            <input
              type="text"
              value={newProject.name}
              onChange={(e) => setNewProject({ ...newProject, name: e.target.value })}
              placeholder="例如：统一教务管理系统"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              测试基准URL (Base URL)
            </label>
            <input
              type="url"
              value={newProject.base_url}
              onChange={(e) => setNewProject({ ...newProject, base_url: e.target.value })}
              placeholder="例如：https://test.example.com"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              项目描述
            </label>
            <textarea
              value={newProject.description}
              onChange={(e) => setNewProject({ ...newProject, description: e.target.value })}
              rows={3}
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div className="flex justify-end gap-3 mt-6 pt-4 border-t dark:border-gray-700">
            <button
              onClick={() => setShowCreateModal(false)}
              className="px-4 py-2 text-gray-700 dark:text-gray-300 bg-gray-100 dark:bg-gray-700 rounded-lg hover:bg-gray-200 transition-colors"
            >
              取消
            </button>
            <button
              onClick={handleCreateProject}
              disabled={!newProject.name.trim()}
              className="px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 transition-colors disabled:opacity-50"
            >
              保存
            </button>
          </div>
        </div>
      </Modal>

      {/* 克隆版本弹窗 */}
      <Modal
        isOpen={showCloneModal.show}
        onClose={() => setShowCloneModal({ show: false, projectId: 0, sourceVersion: null })}
        title={`克隆版本：从 ${showCloneModal.sourceVersion?.version_name}`}
      >
        <div className="space-y-4">
          <div className="bg-blue-50 dark:bg-blue-900/20 p-3 rounded-lg text-sm text-blue-800 dark:text-blue-300 mb-4">
            克隆操作将完整复制源版本下的所有【页面信息】、【底层录制脚本】及【AI 测试用例】，让您在新版本中无缝继承测试资产。
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              新版本名称 <span className="text-red-500">*</span>
            </label>
            <input
              type="text"
              value={newVersionName}
              onChange={(e) => setNewVersionName(e.target.value)}
              placeholder="例如：v1.2.0"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div className="flex justify-end gap-3 mt-6 pt-4 border-t dark:border-gray-700">
            <button
              onClick={() => setShowCloneModal({ show: false, projectId: 0, sourceVersion: null })}
              className="px-4 py-2 text-gray-700 dark:text-gray-300 bg-gray-100 dark:bg-gray-700 rounded-lg hover:bg-gray-200 transition-colors"
            >
              取消
            </button>
            <button
              onClick={handleCloneVersion}
              disabled={!newVersionName.trim()}
              className="px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 transition-colors disabled:opacity-50"
            >
              确认克隆
            </button>
          </div>
        </div>
      </Modal>

      {/* 新建空版本弹窗 */}
      <Modal
        isOpen={showCreateVersionModal.show}
        onClose={() => setShowCreateVersionModal({ show: false, projectId: 0 })}
        title="新建空版本分支"
      >
        <div className="space-y-4">
          <div className="bg-gray-50 dark:bg-gray-800 p-3 rounded-lg text-sm text-gray-600 dark:text-gray-400 mb-4">
            新建的版本分支将是一个全新的空集合，不包含任何历史页面或用例记录。
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              版本名称 <span className="text-red-500">*</span>
            </label>
            <input
              type="text"
              value={newEmptyVersion.version_name}
              onChange={(e) => setNewEmptyVersion({ ...newEmptyVersion, version_name: e.target.value })}
              placeholder="例如：v2.0.0"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              测试基准URL (Base URL)
            </label>
            <input
              type="url"
              value={newEmptyVersion.base_url}
              onChange={(e) => setNewEmptyVersion({ ...newEmptyVersion, base_url: e.target.value })}
              placeholder="例如：https://test-v2.example.com"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              版本描述
            </label>
            <textarea
              value={newEmptyVersion.description}
              onChange={(e) => setNewEmptyVersion({ ...newEmptyVersion, description: e.target.value })}
              rows={3}
              placeholder="简要描述这个版本的主要测试目标"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div className="flex justify-end gap-3 mt-6 pt-4 border-t dark:border-gray-700">
            <button
              onClick={() => setShowCreateVersionModal({ show: false, projectId: 0 })}
              className="px-4 py-2 text-gray-700 dark:text-gray-300 bg-gray-100 dark:bg-gray-700 rounded-lg hover:bg-gray-200 transition-colors"
            >
              取消
            </button>
            <button
              onClick={handleCreateEmptyVersion}
              disabled={!newEmptyVersion.version_name.trim()}
              className="px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 transition-colors disabled:opacity-50"
            >
              确认新建
            </button>
          </div>
        </div>
      </Modal>

      {/* 编辑版本弹窗 */}
      <Modal
        isOpen={showEditVersionModal.show}
        onClose={() => setShowEditVersionModal({ show: false, projectId: 0, version: null })}
        title="编辑版本分支"
      >
        <div className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              版本名称 <span className="text-red-500">*</span>
            </label>
            <input
              type="text"
              value={editVersionForm.version_name}
              onChange={(e) => setEditVersionForm({ ...editVersionForm, version_name: e.target.value })}
              placeholder="例如：v2.0.0"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              测试基准URL (Base URL)
            </label>
            <input
              type="url"
              value={editVersionForm.base_url}
              onChange={(e) => setEditVersionForm({ ...editVersionForm, base_url: e.target.value })}
              placeholder="例如：https://test-v2.example.com"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              版本描述
            </label>
            <textarea
              value={editVersionForm.description}
              onChange={(e) => setEditVersionForm({ ...editVersionForm, description: e.target.value })}
              rows={3}
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div className="flex justify-end gap-3 mt-6 pt-4 border-t dark:border-gray-700">
            <button
              onClick={() => setShowEditVersionModal({ show: false, projectId: 0, version: null })}
              className="px-4 py-2 text-gray-700 dark:text-gray-300 bg-gray-100 dark:bg-gray-700 rounded-lg hover:bg-gray-200 transition-colors"
            >
              取消
            </button>
            <button
              onClick={handleEditVersion}
              disabled={!editVersionForm.version_name.trim()}
              className="px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 transition-colors disabled:opacity-50"
            >
              保存更改
            </button>
          </div>
        </div>
      </Modal>

      {toast && (
        <Toast
          message={toast.message}
          type={toast.type}
          onClose={() => setToast(null)}
        />
      )}
    </div>
  )
}
