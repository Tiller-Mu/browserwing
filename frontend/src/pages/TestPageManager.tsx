import { useEffect, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { ArrowLeft, Bot, ExternalLink, Eye, FilePlus, FileText, Plus, ShieldCheck, Trash2, Video } from 'lucide-react';
import { api, type LLMConfig } from '../api/client';
import { projectApi, streamPlaybotRun, type GenerateTestCasesResponse, type P45AuthContext, type P45RecordingKind, type PlaybotRunEvent, type ProjectAuthStateSummary, type TestCase, type TestPage } from '../api/project';
import { Modal } from '../components/Modal';
import PlaybotRunTimeline from '../components/PlaybotRunTimeline';
import Toast from '../components/Toast';
import {
  buildP45AuthStateSummary,
  buildP45PageManagementView,
  buildP45RecordingDetailView,
  createP45AuthStateController,
  createP45RecordingController,
  type P45AuthStateSummaryView,
} from './p45RecordingUiContract';
import { createRecordingOperationLedger, recordingOperationInputChanged, recordingOperationIsInProgress } from './recordingLifecycleOperationLedger';

export function formatRecordingOperationInProgressMessage(error: unknown): string {
  const reason = String((error as { response?: { data?: { error?: unknown } } })?.response?.data?.error || '').trim();
  const guidance = '运行时录制操作仍在进行中（recording_operation_in_progress）。请等待当前启动完成后再试；若持续出现，请保留此提示并检查浏览器录制页。';
  return reason ? `${guidance} 后端原因：${reason}` : guidance;
}

type GenerateMode = 'append' | 'replace' | 'preview';

const generateModeLabels: Record<GenerateMode, string> = {
  append: '追加',
  replace: '覆盖',
  preview: '预览'
};

export default function TestPageManager() {
  const { projectId, versionId } = useParams();
  const navigate = useNavigate();

  const [pages, setPages] = useState<TestPage[]>([]);
  const [authState, setAuthState] = useState<P45AuthStateSummaryView | null>(null);
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
  const [generateEvents, setGenerateEvents] = useState<PlaybotRunEvent[]>([]);
  const generateStreamAbortRef = useRef<AbortController | null>(null);
  const recordingOperationLedgerRef = useRef(createRecordingOperationLedger());

  useEffect(() => {
    if (projectId && versionId) {
      loadPages();
      loadProjectAuthState();
      loadLLMConfigs();
    }
  }, [projectId, versionId]);

  useEffect(() => {
    return () => {
      generateStreamAbortRef.current?.abort();
    };
  }, []);

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

  const loadProjectAuthState = async () => {
    try {
      const response = await projectApi.getProjectAuthState(Number(projectId), Number(versionId));
      setAuthState(buildP45AuthStateSummary(response.data.auth_state as ProjectAuthStateSummary | null));
    } catch (error) {
      console.error('Failed to load project auth state:', error);
      setAuthState(null);
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

  const recordingController = projectId && versionId ? createP45RecordingController({
    projectId: Number(projectId),
    versionId: Number(versionId),
    operationLedger: recordingOperationLedgerRef.current,
    api: projectApi,
    navigate,
  }) : null;

  const authStateController = projectId && versionId ? createP45AuthStateController({
    projectId: Number(projectId),
    versionId: Number(versionId),
    api: projectApi,
  }) : null;

  const handleDeleteProjectAuthState = async () => {
    if (!authStateController || !authState) return;
    if (!window.confirm('确定要删除当前版本保存的项目登录态吗？')) return;

    try {
      await authStateController.remove();
      setAuthState(null);
      showToast('项目登录态已删除', 'success');
    } catch (error: any) {
      showToast(error.response?.data?.error || '删除项目登录态失败', 'error');
    }
  };

  const navigateToRecord = async (page: TestPage, recordingKind: P45RecordingKind, selectedAuthContext: P45AuthContext) => {
    if (!recordingController) return;
    try {
      await recordingController.startRecording({
        pageId: page.id,
        recordingKind,
        authContext: selectedAuthContext,
        authStateId: selectedAuthContext === 'project_saved' ? authState?.id ?? null : null,
        targetUrl: page.path,
      });
    } catch (error: any) {
      if (recordingOperationInputChanged(error)) {
        showToast(error.message || '上一次开始录制仍在处理中，请等待其完成后再试。', 'info');
        return;
      }
      if (recordingOperationIsInProgress(error)) {
        showToast(formatRecordingOperationInProgressMessage(error), 'error');
        return;
      }
      const detail = error.response?.data?.detail;
      const message = error.response?.data?.error || '启动页面录制失败';
      showToast(detail ? `${message}: ${detail}` : message, 'error');
    }
  };

  const navigateToTestCase = (pageId: number, testCaseId: number) => {
    navigate(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/test-cases/${testCaseId}`);
  };

  const navigateToRecordingDetail = (pageId: number) => {
    navigate(`/projects/${projectId}/versions/${versionId}/pages/${pageId}/recording`);
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
    generateStreamAbortRef.current?.abort();
    setGenerateTargetPage(page);
    setGenerateForm({ mode: 'append', llm_config_id: '', instruction: '' });
    setPreviewCases([]);
    setGenerateEvents([]);
    setShowGenerateModal(true);
  };

  const closeGenerateModal = () => {
    if (generating) return;
    generateStreamAbortRef.current?.abort();
    setShowGenerateModal(false);
  };

  const handleGenerateTestCases = async () => {
    if (!generateTargetPage || !projectId || !versionId) return;

    try {
      setGenerating(true);
      setGenerateEvents([]);
      const response = await projectApi.startGenerateTestCasesRun(
        Number(projectId),
        Number(versionId),
        generateTargetPage.id,
        {
          mode: generateForm.mode,
          llm_config_id: generateForm.llm_config_id || undefined,
          instruction: generateForm.instruction.trim() || undefined
        }
      );
      await connectPlaybotRunStream(response.data.run_id);
    } catch (error: any) {
      showToast(error.response?.data?.error || '生成测试用例失败', 'error');
      setGenerating(false);
    }
  };

  const appendGenerateEvent = (event: PlaybotRunEvent) => {
    setGenerateEvents((prev) => {
      if (prev.some((item) => item.seq === event.seq)) return prev;
      return [...prev, event].sort((a, b) => a.seq - b.seq);
    });
  };

  const handleGenerateResponse = (data: GenerateTestCasesResponse) => {
    if (data.saved) {
      showToast(`已生成并保存 ${data.generated_count} 条测试用例`, 'success');
      setShowGenerateModal(false);
      setPreviewCases([]);
      loadPages();
    } else {
      setPreviewCases(data.test_cases || []);
      showToast(`已生成 ${data.generated_count} 条预览用例，未保存`, 'info');
    }
  };

  const connectPlaybotRunStream = async (runId: string) => {
    const controller = new AbortController();
    generateStreamAbortRef.current = controller;
    let afterSeq = 0;
    let reconnects = 0;

    while (!controller.signal.aborted && reconnects < 3) {
      try {
        const response = await streamPlaybotRun(runId, afterSeq, controller.signal);
        if (!response.ok || !response.body) {
          throw new Error('stream failed');
        }
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';

        while (!controller.signal.aborted) {
          const { done, value } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          const lines = buffer.split('\n');
          buffer = lines.pop() || '';

          for (const line of lines) {
            if (!line.startsWith('data: ')) continue;
            const event = JSON.parse(line.slice(6)) as PlaybotRunEvent;
            afterSeq = Math.max(afterSeq, event.seq);
            appendGenerateEvent(event);

            if (event.phase === 'done') {
              const responseData = event.data?.response as GenerateTestCasesResponse | undefined;
              if (responseData) handleGenerateResponse(responseData);
              setGenerating(false);
              return;
            }
            if (event.phase === 'failed') {
              const responseData = event.data?.response as { error?: string; code?: string } | undefined;
              showToast(responseData?.error || responseData?.code || '生成测试用例失败', 'error');
              setGenerating(false);
              return;
            }
          }
        }
      } catch (error: any) {
        if (controller.signal.aborted) return;
        reconnects += 1;
        await new Promise((resolve) => window.setTimeout(resolve, 600));
        continue;
      }
      reconnects += 1;
    }
    if (!controller.signal.aborted) {
      showToast('生成过程连接中断，请稍后查看结果', 'error');
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

  const pageManagementView = buildP45PageManagementView({ pages, authState });

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
        <div className="bg-white dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded-lg shadow-sm overflow-hidden">
          <div className="px-5 py-3 border-b border-gray-100 dark:border-gray-700 bg-gray-50 dark:bg-gray-900/40 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div className="text-sm text-gray-600 dark:text-gray-400 flex flex-wrap items-center gap-2">
              <ShieldCheck className="w-4 h-4 text-gray-400" />
              <span>项目登录态：</span>
              {pageManagementView.authState ? (
                <span className="font-medium text-gray-900 dark:text-gray-100">
                  {pageManagementView.authState.cookieCount} Cookie / {pageManagementView.authState.originCount} Origin
                </span>
              ) : (
                <span className="font-medium text-amber-700 dark:text-amber-300">未保存</span>
              )}
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-xs text-gray-500 dark:text-gray-400">
                请完成并停止登录流程录制后，在录制页捕获登录态。
              </span>
              {pageManagementView.authState && (
                <button
                  onClick={handleDeleteProjectAuthState}
                  disabled={pageManagementView.authStateActions.find((action) => action.kind === 'delete_project_auth_state')?.disabled}
                  className="px-3 py-1.5 bg-white dark:bg-gray-900 border border-gray-200 dark:border-gray-700 text-gray-700 dark:text-gray-200 rounded-md hover:border-red-300 hover:text-red-700 dark:hover:border-red-800 dark:hover:text-red-300 disabled:opacity-50 disabled:cursor-not-allowed inline-flex items-center gap-1.5 text-sm"
                  title="删除当前版本保存的项目登录态"
                >
                  <Trash2 className="w-3.5 h-3.5" /> 删除登录态
                </button>
              )}
            </div>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[980px] text-sm">
              <thead className="bg-gray-50 dark:bg-gray-900/50 text-gray-500 dark:text-gray-400">
                <tr>
                  <th className="px-5 py-3 text-left font-medium">页面</th>
                  <th className="px-5 py-3 text-left font-medium">主流程</th>
                  <th className="px-5 py-3 text-left font-medium">测试用例</th>
                  <th className="px-5 py-3 text-left font-medium">录制</th>
                  <th className="px-5 py-3 text-right font-medium">操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-700">
                {pageManagementView.rows.map((row) => {
                  const page = row.page;
                  const hasMainScript = Boolean(page.scripts && page.scripts.length > 0);
                  const testCases = page.test_cases || [];
                  const casesCount = testCases.length;
                  const projectSavedAction = row.actions.find((action) => action.recordingKind === 'business_flow' && action.authContext === 'project_saved');

                  return (
                    <tr key={page.id} className="align-top hover:bg-gray-50/70 dark:hover:bg-gray-900/30">
                      <td className="px-5 py-4 max-w-[280px]">
                        <div className="font-semibold text-gray-900 dark:text-gray-100">{page.name}</div>
                        {page.path && (
                          <div className="mt-1 font-mono text-xs text-gray-500 dark:text-gray-400 break-all flex items-center gap-1">
                            <ExternalLink className="w-3.5 h-3.5 shrink-0" /> {page.path}
                          </div>
                        )}
                        {page.description && (
                          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400 line-clamp-2">{page.description}</div>
                        )}
                      </td>
                      <td className="px-5 py-4">
                        {hasMainScript ? (
                          <div>
                            <span className="inline-flex items-center gap-1 text-xs font-medium text-emerald-700 dark:text-emerald-400">
                              <Video className="w-3.5 h-3.5" /> 已就绪
                            </span>
                            <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
                              {page.scripts?.[0]?.name || '未命名脚本'}
                            </div>
                            <button
                              onClick={() => navigateToRecordingDetail(page.id)}
                              className="mt-2 inline-flex items-center gap-1 text-xs text-indigo-600 hover:text-indigo-700 dark:text-indigo-400"
                            >
                              <Eye className="w-3.5 h-3.5" /> 查看结果
                            </button>
                          </div>
                        ) : (
                          <span className="text-xs text-amber-700 dark:text-amber-300">未录制</span>
                        )}
                      </td>
                      <td className="px-5 py-4">
                        <div className="font-medium text-gray-900 dark:text-gray-100">{casesCount} 条</div>
                        {testCases.slice(0, 2).map((tc) => (
                          <button
                            key={tc.id}
                            onClick={() => navigateToTestCase(page.id, tc.id)}
                            className="block mt-1 max-w-[220px] truncate text-xs text-indigo-600 hover:text-indigo-700 dark:text-indigo-400"
                          >
                            {tc.title}
                          </button>
                        ))}
                      </td>
                      <td className="px-5 py-4">
                        <div className="flex flex-wrap gap-2">
                          <button
                            onClick={() => navigateToRecord(page, 'login_flow', 'clean')}
                            className="px-3 py-1.5 bg-white dark:bg-gray-900 border border-gray-200 dark:border-gray-700 text-gray-700 dark:text-gray-200 rounded-md hover:border-indigo-300 dark:hover:border-indigo-700 inline-flex items-center gap-1.5"
                          >
                            <Video className="w-3.5 h-3.5" /> 登录流程
                          </button>
                          <button
                            onClick={() => navigateToRecord(page, 'business_flow', 'project_saved')}
                            disabled={projectSavedAction?.disabled}
                            title={projectSavedAction?.disabled ? '请先完成并停止登录流程录制，再在录制页捕获登录态；或选择干净会话录制业务流程' : undefined}
                            className="px-3 py-1.5 bg-gray-900 dark:bg-gray-700 text-white rounded-md hover:bg-gray-800 dark:hover:bg-gray-600 disabled:opacity-50 disabled:cursor-not-allowed inline-flex items-center gap-1.5"
                          >
                            <Video className="w-3.5 h-3.5" /> 业务流程
                          </button>
                          <button
                            onClick={() => navigateToRecord(page, 'business_flow', 'clean')}
                            className="px-3 py-1.5 bg-white dark:bg-gray-900 border border-gray-200 dark:border-gray-700 text-gray-700 dark:text-gray-200 rounded-md hover:border-indigo-300 dark:hover:border-indigo-700 inline-flex items-center gap-1.5"
                          >
                            <Video className="w-3.5 h-3.5" /> 干净业务
                          </button>
                        </div>
                        {projectSavedAction?.disabled && (
                          <div className="mt-2 text-xs text-amber-700 dark:text-amber-300">
                            无登录态时，请先完成并停止登录流程录制，再在录制页捕获登录态；或选择干净业务录制。
                          </div>
                        )}
                      </td>
                      <td className="px-5 py-4">
                        <div className="flex justify-end gap-2">
                          {renderCreateCaseButton(page)}
                          {hasMainScript && renderGenerateButton(page)}
                          <button
                            onClick={() => handleDeletePage(page.id)}
                            className="p-2 text-gray-400 hover:text-red-600 hover:bg-red-50 dark:hover:bg-red-900/30 rounded-md transition-colors"
                            title="删除页面"
                          >
                            <Trash2 className="w-4 h-4" />
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
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
        onClose={closeGenerateModal}
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

          <PlaybotRunTimeline events={generateEvents} running={generating} />

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
              onClick={closeGenerateModal}
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

export const p45TestPageManagerContract = {
  buildAuthStateSummary: buildP45AuthStateSummary,
  buildPageManagementView: buildP45PageManagementView,
  buildRecordingDetailView: buildP45RecordingDetailView,
  createAuthStateController: createP45AuthStateController,
  createRecordingController: createP45RecordingController,
};
