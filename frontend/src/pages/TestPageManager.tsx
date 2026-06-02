import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { ArrowLeft, Bot, ExternalLink, FilePlus, FileText, ListChecks, Plus, Trash2, Video } from 'lucide-react';
import { api, type LLMConfig } from '../api/client';
import { projectApi, type TestCase, type TestCaseStatus, type TestPage } from '../api/project';
import { Modal } from '../components/Modal';
import Toast from '../components/Toast';

type GenerateMode = 'append' | 'replace' | 'preview';

const generateModeLabels: Record<GenerateMode, string> = {
  append: '追加',
  replace: '覆盖',
  preview: '预览'
};

const testCaseStatusLabels: Record<TestCaseStatus, string> = {
  active: '启用',
  draft: '草稿',
  archived: '归档'
};

const testCaseStatusClass: Record<TestCaseStatus, string> = {
  active: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400',
  draft: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400',
  archived: 'bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400'
};

export default function TestPageManager() {
  const { projectId, versionId } = useParams();
  const navigate = useNavigate();

  const [pages, setPages] = useState<TestPage[]>([]);
  const [llmConfigs, setLlmConfigs] = useState<LLMConfig[]>([]);
  const [loading, setLoading] = useState(true);
  const [generating, setGenerating] = useState(false);
  const [toast, setToast] = useState<{ message: string; type: 'success' | 'error' | 'info' } | null>(null);

  const [showCreateModal, setShowCreateModal] = useState(false);
  const [showCreateCaseModal, setShowCreateCaseModal] = useState(false);
  const [showGenerateModal, setShowGenerateModal] = useState(false);
  const [newPage, setNewPage] = useState({ name: '', path: '', description: '' });
  const [createCasePage, setCreateCasePage] = useState<TestPage | null>(null);
  const [newCase, setNewCase] = useState({ title: '', description: '' });
  const [generateTargetPage, setGenerateTargetPage] = useState<TestPage | null>(null);
  const [generateForm, setGenerateForm] = useState({
    mode: 'append' as GenerateMode,
    llm_config_id: '',
    instruction: ''
  });
  const [previewCases, setPreviewCases] = useState<TestCase[]>([]);

  useEffect(() => {
    if (projectId && versionId) {
      loadPages();
      loadLLMConfigs();
    }
  }, [projectId, versionId]);

  const showToast = (message: string, type: 'success' | 'error' | 'info' = 'info') => {
    setToast({ message, type });
  };

  const loadPages = async () => {
    try {
      setLoading(true);
      const response = await projectApi.getPages(Number(projectId), Number(versionId));
      setPages(response.data || []);
    } catch (error) {
      console.error('Failed to load pages:', error);
      showToast('获取测试页面列表失败', 'error');
    } finally {
      setLoading(false);
    }
  };

  const loadLLMConfigs = async () => {
    try {
      const response = await api.listLLMConfigs();
      setLlmConfigs((response.data.configs || []).filter((config) => config.is_active));
    } catch (error) {
      console.error('Failed to load LLM configs:', error);
      setLlmConfigs([]);
    }
  };

  const handleCreatePage = async () => {
    if (!newPage.name.trim()) return;

    try {
      await projectApi.createPage(Number(projectId), Number(versionId), newPage);
      showToast('页面创建成功', 'success');
      setShowCreateModal(false);
      setNewPage({ name: '', path: '', description: '' });
      loadPages();
    } catch (error) {
      console.error('Failed to create page:', error);
      showToast('页面创建失败', 'error');
    }
  };

  const handleDeletePage = async (pageId: number) => {
    if (!window.confirm('确定要删除该页面及其挂载的所有录制轨迹和测试用例吗？不可恢复！')) return;

    try {
      await projectApi.deletePage(Number(projectId), Number(versionId), pageId);
      showToast('删除成功', 'success');
      loadPages();
    } catch (error) {
      console.error('Failed to delete page:', error);
      showToast('删除失败', 'error');
    }
  };

  const navigateToRecord = (pageId: number) => {
    navigate(`/browser?projectId=${projectId}&versionId=${versionId}&pageId=${pageId}`);
  };

  const navigateToTestCase = (pageId: number, testCaseId: number) => {
    navigate(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases/${testCaseId}`);
  };

  const openCreateTestCaseModal = (page: TestPage) => {
    setCreateCasePage(page);
    setNewCase({ title: '', description: '' });
    setShowCreateCaseModal(true);
  };

  const handleCreateTestCase = async () => {
    if (!createCasePage || !projectId || !versionId) return;

    const title = newCase.title.trim();
    if (!title) return;

    try {
      const response = await projectApi.createTestCase(Number(projectId), Number(versionId), createCasePage.id, {
        title,
        description: newCase.description,
        status: 'draft',
        blueprint: {
          title,
          description: newCase.description,
          steps: []
        },
        script_content: ''
      });
      setShowCreateCaseModal(false);
      navigateToTestCase(createCasePage.id, response.data.test_case.id);
    } catch (error: any) {
      showToast(error.response?.data?.error || '创建测试用例失败', 'error');
    }
  };

  const openGenerateModal = (page: TestPage) => {
    setGenerateTargetPage(page);
    setGenerateForm({ mode: 'append', llm_config_id: '', instruction: '' });
    setPreviewCases([]);
    setShowGenerateModal(true);
  };

  const handleGenerateTestCases = async () => {
    if (!generateTargetPage || !projectId || !versionId) return;

    try {
      setGenerating(true);
      const response = await projectApi.generateTestCases(
        Number(projectId),
        Number(versionId),
        generateTargetPage.id,
        {
          mode: generateForm.mode,
          llm_config_id: generateForm.llm_config_id || undefined,
          instruction: generateForm.instruction.trim() || undefined
        }
      );

      if (response.data.saved) {
        showToast(`已生成并保存 ${response.data.generated_count} 条测试用例`, 'success');
        setShowGenerateModal(false);
        setPreviewCases([]);
        loadPages();
      } else {
        setPreviewCases(response.data.test_cases || []);
        showToast(`已生成 ${response.data.generated_count} 条预览用例，未保存`, 'info');
      }
    } catch (error: any) {
      showToast(error.response?.data?.error || '生成测试用例失败', 'error');
    } finally {
      setGenerating(false);
    }
  };

  const renderGenerateButton = (page: TestPage, label = '智能生成') => (
    <button
      onClick={() => openGenerateModal(page)}
      className="px-3 py-2 bg-gray-900 dark:bg-gray-700 text-white rounded-lg shadow-sm hover:bg-gray-800 dark:hover:bg-gray-600 transition-colors flex items-center gap-2 text-sm"
    >
      <Bot className="w-4 h-4" /> {label}
    </button>
  );

  const renderCreateCaseButton = (page: TestPage) => (
    <button
      onClick={() => openCreateTestCaseModal(page)}
      className="px-3 py-2 bg-indigo-50 text-indigo-700 dark:bg-indigo-900/30 dark:text-indigo-300 rounded-lg hover:bg-indigo-100 dark:hover:bg-indigo-900/50 transition-colors flex items-center gap-2 text-sm"
    >
      <FilePlus className="w-4 h-4" /> 新建用例
    </button>
  );

  return (
    <div className="space-y-6 lg:space-y-8 animate-fade-in">
      <div className="space-y-4">
        <button
          onClick={() => navigate('/projects')}
          className="flex items-center gap-1 text-sm text-gray-500 hover:text-indigo-600 transition-colors"
        >
          <ArrowLeft className="w-4 h-4" /> 返回项目管理
        </button>
        <div className="flex items-center justify-between gap-4">
          <div>
            <h1 className="text-2xl font-bold text-gray-900 dark:text-gray-100">测试模块管理</h1>
            <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">
              在此管理版本下的所有核心业务页面。每个页面应该对应一条主线录制轨迹，大模型将基于此推导测试用例。
            </p>
          </div>
          <button
            onClick={() => setShowCreateModal(true)}
            className="flex items-center gap-2 px-4 py-2 bg-gray-900 dark:bg-gray-700 text-white rounded-lg hover:bg-gray-800 dark:hover:bg-gray-600 transition-colors"
          >
            <Plus className="w-4 h-4" />
            添加业务页面
          </button>
        </div>
      </div>

      {loading ? (
        <div className="flex items-center justify-center py-12">
          <div className="text-gray-500 dark:text-gray-400">加载中...</div>
        </div>
      ) : pages.length === 0 ? (
        <div className="bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 p-12 text-center">
          <FileText className="w-12 h-12 text-gray-300 dark:text-gray-600 mx-auto mb-4" />
          <div className="text-gray-500 dark:text-gray-400">
            该版本下暂无页面配置，请点击上方按钮添加。
          </div>
        </div>
      ) : (
        <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
          {pages.map((page) => {
            const hasMainScript = Boolean(page.scripts && page.scripts.length > 0);
            const testCases = page.test_cases || [];
            const casesCount = testCases.length;

            return (
              <div key={page.id} className="bg-white dark:bg-gray-800 rounded-xl shadow-sm border border-gray-200 dark:border-gray-700 overflow-hidden flex flex-col">
                <div className="p-5 border-b border-gray-100 dark:border-gray-700 flex items-start justify-between bg-gray-50/50 dark:bg-gray-900/20">
                  <div className="flex items-start gap-3 min-w-0">
                    <div className="p-2 bg-indigo-50 dark:bg-indigo-900/30 rounded-lg mt-0.5">
                      <FileText className="w-5 h-5 text-indigo-600 dark:text-indigo-400" />
                    </div>
                    <div className="min-w-0">
                      <h3 className="text-lg font-bold text-gray-900 dark:text-gray-100 flex items-center gap-2 flex-wrap">
                        {page.name}
                        {hasMainScript && (
                          <span className="px-2 py-0.5 text-xs font-medium bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400 rounded-full flex items-center gap-1">
                            <Video className="w-3 h-3" /> 主流程已就绪
                          </span>
                        )}
                      </h3>
                      {page.path && (
                        <p className="text-sm text-gray-500 dark:text-gray-400 mt-1 font-mono flex items-center gap-1 break-all">
                          <ExternalLink className="w-3.5 h-3.5 shrink-0" /> {page.path}
                        </p>
                      )}
                      {page.description && (
                        <p className="text-sm text-gray-600 dark:text-gray-400 mt-2 line-clamp-2">
                          {page.description}
                        </p>
                      )}
                    </div>
                  </div>
                  <button
                    onClick={() => handleDeletePage(page.id)}
                    className="p-1.5 text-gray-400 hover:text-red-600 hover:bg-red-50 dark:hover:bg-red-900/30 rounded transition-colors"
                    title="删除页面"
                  >
                    <Trash2 className="w-4 h-4" />
                  </button>
                </div>

                <div className="p-5 flex-1 flex flex-col bg-white dark:bg-gray-800">
                  <div className={`mb-5 rounded-lg p-4 border ${
                    hasMainScript
                      ? 'bg-indigo-50/50 dark:bg-indigo-900/10 border-indigo-100 dark:border-indigo-900/30'
                      : 'bg-gray-50 dark:bg-gray-900/50 border-gray-200 dark:border-gray-700'
                  }`}>
                    <div className="flex items-center justify-between mb-2 gap-3">
                      <h4 className={`font-medium text-sm flex items-center gap-2 ${
                        hasMainScript ? 'text-indigo-900 dark:text-indigo-300' : 'text-gray-900 dark:text-gray-100'
                      }`}>
                        <Video className="w-4 h-4 text-indigo-500" />
                        {hasMainScript ? '主流程录制轨迹' : '缺少主流程录制'}
                      </h4>
                      <button
                        onClick={() => navigateToRecord(page.id)}
                        className="text-xs text-indigo-600 hover:text-indigo-700 dark:text-indigo-400 dark:hover:text-indigo-300 font-medium flex items-center gap-1 transition-colors"
                      >
                        <Video className="w-3.5 h-3.5" /> {hasMainScript ? '重新录制' : '立即录制'}
                      </button>
                    </div>
                    {hasMainScript ? (
                      <div className="flex items-center justify-between gap-3 text-sm">
                        <span className="text-gray-600 dark:text-gray-400 min-w-0">
                          已绑定脚本: <span className="font-semibold text-gray-900 dark:text-gray-100">{page.scripts?.[0]?.name || '未命名脚本'}</span>
                        </span>
                        <span className="text-gray-500 dark:text-gray-500 text-xs shrink-0">
                          {page.scripts?.[0]?.updated_at ? new Date(page.scripts[0].updated_at).toLocaleString() : ''}
                        </span>
                      </div>
                    ) : (
                      <p className="text-sm text-gray-500 dark:text-gray-400">
                        手工维护用例不要求主流程；智能生成前需要先录制一条正向主流程。
                      </p>
                    )}
                  </div>

                  <div className="flex items-center justify-between gap-3 mb-4">
                    <h4 className="font-medium text-gray-900 dark:text-gray-100 flex items-center gap-2">
                      <ListChecks className="w-4 h-4 text-indigo-500" /> 测试用例 ({casesCount})
                    </h4>
                    <div className="flex items-center gap-2">
                      {renderCreateCaseButton(page)}
                      {hasMainScript && renderGenerateButton(page)}
                    </div>
                  </div>

                  {casesCount === 0 ? (
                    <div className="flex-1 bg-gray-50 dark:bg-gray-900/50 rounded-lg p-6 flex flex-col items-center justify-center text-center">
                      <FilePlus className="w-10 h-10 text-gray-400 mb-3" />
                      <p className="text-sm text-gray-600 dark:text-gray-400 mb-4">
                        暂无测试用例。
                      </p>
                      <div className="flex flex-wrap items-center justify-center gap-2">
                        {renderCreateCaseButton(page)}
                        {hasMainScript && renderGenerateButton(page, '智能生成测试用例')}
                      </div>
                    </div>
                  ) : (
                    <div className="space-y-3 flex-1 overflow-y-auto max-h-[300px] pr-2">
                      {testCases.map((tc) => (
                        <button
                          key={tc.id}
                          onClick={() => navigateToTestCase(page.id, tc.id)}
                          className="w-full text-left p-3 border border-gray-100 dark:border-gray-700 rounded-lg hover:border-indigo-200 dark:hover:border-indigo-800 transition-colors group"
                        >
                          <div className="flex justify-between items-start gap-3 mb-1">
                            <span className="font-medium text-sm text-gray-900 dark:text-gray-100 group-hover:text-indigo-600 dark:group-hover:text-indigo-400 transition-colors">
                              {tc.title}
                            </span>
                            <span className={`text-[10px] px-1.5 py-0.5 rounded font-medium shrink-0 ${testCaseStatusClass[tc.status] || testCaseStatusClass.draft}`}>
                              {testCaseStatusLabels[tc.status] || tc.status}
                            </span>
                          </div>
                          <p className="text-xs text-gray-500 dark:text-gray-400 line-clamp-1">
                            {tc.description}
                          </p>
                        </button>
                      ))}
                    </div>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      )}

      <Modal
        isOpen={showCreateModal}
        onClose={() => setShowCreateModal(false)}
        title="添加业务页面"
      >
        <div className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              页面名称 <span className="text-red-500">*</span>
            </label>
            <input
              type="text"
              value={newPage.name}
              onChange={(e) => setNewPage({ ...newPage, name: e.target.value })}
              placeholder="例如：系统登录页"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              页面路由路径 (选填)
            </label>
            <input
              type="text"
              value={newPage.path}
              onChange={(e) => setNewPage({ ...newPage, path: e.target.value })}
              placeholder="例如：/login 或 /dashboard"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              业务描述 (选填)
            </label>
            <textarea
              value={newPage.description}
              onChange={(e) => setNewPage({ ...newPage, description: e.target.value })}
              rows={3}
              placeholder="描述该页面的主要功能和录制预期，这有助于大模型更好地理解上下文"
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
              onClick={handleCreatePage}
              disabled={!newPage.name.trim()}
              className="px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 transition-colors disabled:opacity-50"
            >
              保存
            </button>
          </div>
        </div>
      </Modal>

      <Modal
        isOpen={showCreateCaseModal}
        onClose={() => setShowCreateCaseModal(false)}
        title={`新建测试用例${createCasePage ? `：${createCasePage.name}` : ''}`}
      >
        <div className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              用例标题 <span className="text-red-500">*</span>
            </label>
            <input
              value={newCase.title}
              onChange={(e) => setNewCase({ ...newCase, title: e.target.value })}
              placeholder="例如：密码为空时提示必填"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">业务描述</label>
            <textarea
              value={newCase.description}
              onChange={(e) => setNewCase({ ...newCase, description: e.target.value })}
              rows={3}
              placeholder="描述这个用例验证的业务场景"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div className="flex justify-end gap-3 mt-6 pt-4 border-t dark:border-gray-700">
            <button
              onClick={() => setShowCreateCaseModal(false)}
              className="px-4 py-2 text-gray-700 dark:text-gray-300 bg-gray-100 dark:bg-gray-700 rounded-lg hover:bg-gray-200 transition-colors"
            >
              取消
            </button>
            <button
              onClick={handleCreateTestCase}
              disabled={!newCase.title.trim()}
              className="px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 transition-colors disabled:opacity-50"
            >
              创建草稿
            </button>
          </div>
        </div>
      </Modal>

      <Modal
        isOpen={showGenerateModal}
        onClose={() => !generating && setShowGenerateModal(false)}
        title={`智能生成测试用例${generateTargetPage ? `：${generateTargetPage.name}` : ''}`}
      >
        <div className="space-y-5">
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">
              生成模式
            </label>
            <div className="grid grid-cols-3 gap-2">
              {(Object.keys(generateModeLabels) as GenerateMode[]).map((mode) => (
                <button
                  key={mode}
                  onClick={() => {
                    setGenerateForm({ ...generateForm, mode });
                    setPreviewCases([]);
                  }}
                  className={`px-3 py-2 rounded-lg border text-sm font-medium transition-colors ${
                    generateForm.mode === mode
                      ? 'border-gray-900 bg-gray-900 text-white dark:border-gray-100 dark:bg-gray-100 dark:text-gray-900'
                      : 'border-gray-200 text-gray-700 hover:bg-gray-50 dark:border-gray-700 dark:text-gray-300 dark:hover:bg-gray-700'
                  }`}
                >
                  {generateModeLabels[mode]}
                </button>
              ))}
            </div>
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              LLM 配置
            </label>
            <select
              value={generateForm.llm_config_id}
              onChange={(e) => setGenerateForm({ ...generateForm, llm_config_id: e.target.value })}
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            >
              <option value="">使用默认配置</option>
              {llmConfigs.map((config) => (
                <option key={config.id} value={config.id}>
                  {config.name} / {config.model}
                </option>
              ))}
            </select>
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              额外说明
            </label>
            <textarea
              value={generateForm.instruction}
              onChange={(e) => setGenerateForm({ ...generateForm, instruction: e.target.value })}
              rows={3}
              placeholder="例如：补充异常、边界和回归场景"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>

          {previewCases.length > 0 && (
            <div className="space-y-2 max-h-56 overflow-y-auto pr-1">
              {previewCases.map((testCase, index) => (
                <div key={`${testCase.title}-${index}`} className="p-3 border border-gray-200 dark:border-gray-700 rounded-lg">
                  <div className="font-medium text-sm text-gray-900 dark:text-gray-100">{testCase.title}</div>
                  <p className="text-xs text-gray-500 dark:text-gray-400 mt-1 line-clamp-2">{testCase.description}</p>
                </div>
              ))}
            </div>
          )}

          <div className="flex justify-end gap-3 pt-4 border-t dark:border-gray-700">
            <button
              onClick={() => setShowGenerateModal(false)}
              disabled={generating}
              className="px-4 py-2 text-gray-700 dark:text-gray-300 bg-gray-100 dark:bg-gray-700 rounded-lg hover:bg-gray-200 transition-colors disabled:opacity-50"
            >
              取消
            </button>
            <button
              onClick={handleGenerateTestCases}
              disabled={generating}
              className="px-4 py-2 bg-gray-900 dark:bg-gray-700 text-white rounded-lg hover:bg-gray-800 dark:hover:bg-gray-600 transition-colors disabled:opacity-50 flex items-center gap-2"
            >
              <Bot className="w-4 h-4" /> {generating ? '生成中...' : '开始生成'}
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
  );
}
