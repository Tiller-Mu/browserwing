import { useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { AlertCircle, ArrowLeft, CheckCircle2, Clock, PlayCircle, Save, Sparkles, Trash2, XCircle } from 'lucide-react';
import {
  projectApi,
  type TestCaseRefinementDetail,
  type TestCaseRefinementStatus,
  type TestCaseRefinementSummary,
  type TestCaseDetail as TestCaseDetailType,
  type TestCaseStatus,
  type TestExecutionDetail,
  type TestExecutionStatus,
  type TestExecutionSummary
} from '../api/project';
import Toast from '../components/Toast';

const statusLabels: Record<TestCaseStatus, string> = {
  active: '启用',
  draft: '草稿',
  archived: '归档'
};

const executionLabels: Record<TestExecutionStatus, string> = {
  passed: '通过',
  failed: '失败',
  error: '异常'
};

const executionClasses: Record<TestExecutionStatus, string> = {
  passed: 'text-emerald-700 bg-emerald-50 border-emerald-200 dark:text-emerald-300 dark:bg-emerald-900/20 dark:border-emerald-800',
  failed: 'text-amber-700 bg-amber-50 border-amber-200 dark:text-amber-300 dark:bg-amber-900/20 dark:border-amber-800',
  error: 'text-red-700 bg-red-50 border-red-200 dark:text-red-300 dark:bg-red-900/20 dark:border-red-800'
};

const refinementLabels: Record<TestCaseRefinementStatus, string> = {
  proposed: '待应用',
  applied: '已应用',
  discarded: '已放弃'
};

const refinementClasses: Record<TestCaseRefinementStatus, string> = {
  proposed: 'text-indigo-700 bg-indigo-50 border-indigo-200 dark:text-indigo-300 dark:bg-indigo-900/20 dark:border-indigo-800',
  applied: 'text-emerald-700 bg-emerald-50 border-emerald-200 dark:text-emerald-300 dark:bg-emerald-900/20 dark:border-emerald-800',
  discarded: 'text-gray-700 bg-gray-50 border-gray-200 dark:text-gray-300 dark:bg-gray-900/20 dark:border-gray-700'
};

export default function TestCaseDetail() {
  const { projectId, versionId, pageId, testCaseId } = useParams();
  const navigate = useNavigate();

  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [executing, setExecuting] = useState(false);
  const [testCase, setTestCase] = useState<TestCaseDetailType | null>(null);
  const [executions, setExecutions] = useState<TestExecutionSummary[]>([]);
  const [selectedExecution, setSelectedExecution] = useState<TestExecutionDetail | null>(null);
  const [executionLoading, setExecutionLoading] = useState(false);
  const [executionError, setExecutionError] = useState('');
  const [refinePrompt, setRefinePrompt] = useState('');
  const [useExecutionContext, setUseExecutionContext] = useState(false);
  const [refining, setRefining] = useState(false);
  const [refinementBusy, setRefinementBusy] = useState(false);
  const [refinements, setRefinements] = useState<TestCaseRefinementSummary[]>([]);
  const [selectedRefinement, setSelectedRefinement] = useState<TestCaseRefinementDetail | null>(null);
  const [refinementError, setRefinementError] = useState('');
  const [form, setForm] = useState({
    title: '',
    description: '',
    status: 'draft' as TestCaseStatus,
    blueprintText: '{}',
    script_content: ''
  });
  const [toast, setToast] = useState<{ message: string; type: 'success' | 'error' | 'info' } | null>(null);

  const ids = useMemo(() => {
    if (!projectId || !versionId || !pageId || !testCaseId) return null;
    return {
      projectId: Number(projectId),
      versionId: Number(versionId),
      pageId: Number(pageId),
      testCaseId: Number(testCaseId)
    };
  }, [pageId, projectId, testCaseId, versionId]);

  useEffect(() => {
    loadTestCase();
    loadExecutions();
    loadRefinements();
  }, [ids]);

  const showToast = (message: string, type: 'success' | 'error' | 'info' = 'info') => {
    setToast({ message, type });
  };

  const loadTestCase = async () => {
    if (!ids) return;

    try {
      setLoading(true);
      const response = await projectApi.getTestCase(ids.projectId, ids.versionId, ids.pageId, ids.testCaseId);
      applyServerTestCase(response.data.test_case);
    } catch (error: any) {
      showToast(error.response?.data?.error || '读取测试用例失败', 'error');
    } finally {
      setLoading(false);
    }
  };

  const loadExecutions = async (preferExecutionId?: number) => {
    if (!ids) return;

    try {
      setExecutionLoading(true);
      setExecutionError('');
      const response = await projectApi.listTestCaseExecutions(ids.projectId, ids.versionId, ids.pageId, ids.testCaseId, 20);
      const nextExecutions = response.data.executions || [];
      setExecutions(nextExecutions);
      const targetId = preferExecutionId || nextExecutions[0]?.id;
      if (targetId) {
        await loadExecutionDetail(targetId, false);
      } else {
        setSelectedExecution(null);
      }
    } catch (error: any) {
      setExecutionError(error.response?.data?.error || '读取执行记录失败');
    } finally {
      setExecutionLoading(false);
    }
  };

  const loadExecutionDetail = async (executionId: number, showLoading = true) => {
    if (!ids) return;

    try {
      if (showLoading) setExecutionLoading(true);
      setExecutionError('');
      const response = await projectApi.getTestCaseExecution(ids.projectId, ids.versionId, ids.pageId, ids.testCaseId, executionId);
      setSelectedExecution(response.data.execution);
    } catch (error: any) {
      setExecutionError(error.response?.data?.error || '读取执行报告失败');
    } finally {
      if (showLoading) setExecutionLoading(false);
    }
  };

  const loadRefinements = async (preferRefinementId?: number) => {
    if (!ids) return;

    try {
      setRefinementError('');
      const response = await projectApi.listTestCaseRefinements(ids.projectId, ids.versionId, ids.pageId, ids.testCaseId);
      const nextRefinements = response.data.refinements || [];
      setRefinements(nextRefinements);
      const target = preferRefinementId || nextRefinements.find((item) => item.status === 'proposed')?.id || nextRefinements[0]?.id;
      if (target) {
        await loadRefinementDetail(target);
      } else {
        setSelectedRefinement(null);
      }
    } catch (error: any) {
      setRefinementError(error.response?.data?.error || '读取修改历史失败');
    }
  };

  const loadRefinementDetail = async (refinementId: number) => {
    if (!ids) return;

    try {
      setRefinementError('');
      const response = await projectApi.getTestCaseRefinement(ids.projectId, ids.versionId, ids.pageId, ids.testCaseId, refinementId);
      setSelectedRefinement(response.data.refinement);
    } catch (error: any) {
      setRefinementError(error.response?.data?.error || '读取修改建议失败');
    }
  };

  const applyServerTestCase = (next: TestCaseDetailType) => {
    setTestCase(next);
    setForm({
      title: next.title,
      description: next.description || '',
      status: next.status,
      blueprintText: JSON.stringify(next.blueprint, null, 2),
      script_content: next.script_content || ''
    });
  };

  const hasUnsavedChanges = () => {
    if (!testCase) return false;
    return (
      form.title !== testCase.title ||
      form.description !== (testCase.description || '') ||
      form.status !== testCase.status ||
      form.script_content !== (testCase.script_content || '') ||
      form.blueprintText !== JSON.stringify(testCase.blueprint, null, 2)
    );
  };

  const blockWhenUnsaved = () => {
    if (!hasUnsavedChanges()) return false;
    showToast('请先保存当前编辑内容', 'error');
    return true;
  };

  const parseBlueprint = () => {
    let parsed: unknown;
    try {
      parsed = JSON.parse(form.blueprintText);
    } catch {
      throw new Error('Blueprint 必须是合法 JSON');
    }
    if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
      throw new Error('Blueprint 必须是 JSON object');
    }
    const blueprint = parsed as Record<string, any>;
    if (form.status === 'active' && (!Array.isArray(blueprint.steps) || blueprint.steps.length === 0)) {
      throw new Error('启用状态的 Blueprint 必须包含非空 steps');
    }
    return blueprint;
  };

  const handleSave = async () => {
    if (!ids || executing) return;
    const title = form.title.trim();
    if (!title) {
      showToast('标题不能为空', 'error');
      return;
    }

    let blueprint: Record<string, any>;
    try {
      blueprint = parseBlueprint();
    } catch (error: any) {
      showToast(error.message, 'error');
      return;
    }

    try {
      setSaving(true);
      const response = await projectApi.updateTestCase(ids.projectId, ids.versionId, ids.pageId, ids.testCaseId, {
        title,
        description: form.description,
        status: form.status,
        blueprint,
        script_content: form.script_content
      });
      applyServerTestCase(response.data.test_case);
      showToast('保存成功', 'success');
    } catch (error: any) {
      showToast(error.response?.data?.error || '保存失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async () => {
    if (!ids || executing || !window.confirm('确定要删除该测试用例吗？不可恢复！')) return;

    try {
      await projectApi.deleteTestCase(ids.projectId, ids.versionId, ids.pageId, ids.testCaseId);
      showToast('删除成功', 'success');
      navigate(`/projects/${ids.projectId}/versions/${ids.versionId}/pages`);
    } catch (error: any) {
      showToast(error.response?.data?.error || '删除失败', 'error');
    }
  };

  const handleRun = async () => {
    if (!ids || form.status !== 'active') return;

    try {
      setExecuting(true);
      setExecutionError('');
      const response = await projectApi.runTestCase(ids.projectId, ids.versionId, ids.pageId, ids.testCaseId, {
        stop_on_failure: true,
        capture_screenshot: true
      });
      const execution = response.data.execution;
      setSelectedExecution(execution);
      await loadExecutions(execution.id);
      showToast(`执行完成：${executionLabels[execution.status]}`, execution.status === 'passed' ? 'success' : 'info');
    } catch (error: any) {
      const message = error.response?.data?.error || '执行失败';
      setExecutionError(message);
      showToast(message, 'error');
    } finally {
      setExecuting(false);
    }
  };

  const handleRefine = async () => {
    if (!ids || !refinePrompt.trim()) {
      showToast('请输入修改要求', 'error');
      return;
    }
    if (blockWhenUnsaved()) return;

    try {
      setRefining(true);
      setRefinementError('');
      const response = await projectApi.refineTestCase(ids.projectId, ids.versionId, ids.pageId, ids.testCaseId, {
        prompt: refinePrompt.trim(),
        execution_id: useExecutionContext ? selectedExecution?.id : undefined
      });
      const refinement = response.data.refinement;
      setSelectedRefinement(refinement);
      await loadRefinements(refinement.id);
      showToast('修改建议已生成', 'success');
    } catch (error: any) {
      const message = error.response?.data?.error || '生成修改建议失败';
      setRefinementError(message);
      showToast(message, 'error');
    } finally {
      setRefining(false);
    }
  };

  const handleApplyRefinement = async () => {
    if (!ids || !selectedRefinement || selectedRefinement.status !== 'proposed') return;
    if (blockWhenUnsaved()) return;

    try {
      setRefinementBusy(true);
      setRefinementError('');
      const response = await projectApi.applyTestCaseRefinement(
        ids.projectId,
        ids.versionId,
        ids.pageId,
        ids.testCaseId,
        selectedRefinement.id
      );
      applyServerTestCase(response.data.test_case);
      await loadRefinements(selectedRefinement.id);
      showToast('修改建议已应用', 'success');
    } catch (error: any) {
      const message = error.response?.data?.error || '应用修改建议失败';
      setRefinementError(message);
      showToast(message, 'error');
    } finally {
      setRefinementBusy(false);
    }
  };

  const handleDiscardRefinement = async () => {
    if (!ids || !selectedRefinement || selectedRefinement.status !== 'proposed') return;

    try {
      setRefinementBusy(true);
      setRefinementError('');
      await projectApi.discardTestCaseRefinement(
        ids.projectId,
        ids.versionId,
        ids.pageId,
        ids.testCaseId,
        selectedRefinement.id
      );
      await loadRefinements(selectedRefinement.id);
      showToast('修改建议已放弃', 'success');
    } catch (error: any) {
      const message = error.response?.data?.error || '放弃修改建议失败';
      setRefinementError(message);
      showToast(message, 'error');
    } finally {
      setRefinementBusy(false);
    }
  };

  const formatDuration = (durationMs?: number) => {
    if (!durationMs && durationMs !== 0) return '-';
    if (durationMs < 1000) return `${durationMs} ms`;
    return `${(durationMs / 1000).toFixed(2)} s`;
  };

  const executionIcon = (status: TestExecutionStatus) => {
    if (status === 'passed') return <CheckCircle2 className="w-4 h-4" />;
    if (status === 'failed') return <AlertCircle className="w-4 h-4" />;
    return <XCircle className="w-4 h-4" />;
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center py-12">
        <div className="text-gray-500 dark:text-gray-400">加载中...</div>
      </div>
    );
  }

  if (!testCase || !ids) {
    return (
      <div className="space-y-4">
        <button
          onClick={() => navigate(-1)}
          className="flex items-center gap-1 text-sm text-gray-500 hover:text-indigo-600 transition-colors"
        >
          <ArrowLeft className="w-4 h-4" /> 返回
        </button>
        <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-8 text-gray-500 dark:text-gray-400">
          测试用例不存在或当前层级无权访问。
        </div>
        {toast && <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />}
      </div>
    );
  }

  return (
    <div className="space-y-6 animate-fade-in">
      <div className="space-y-4">
        <button
          onClick={() => navigate(`/projects/${ids.projectId}/versions/${ids.versionId}/pages`)}
          className="flex items-center gap-1 text-sm text-gray-500 hover:text-indigo-600 transition-colors"
        >
          <ArrowLeft className="w-4 h-4" /> 返回页面管理
        </button>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 className="text-2xl font-bold text-gray-900 dark:text-gray-100">测试用例详情</h1>
            <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">
              #{testCase.id} / 页面 #{testCase.page_id}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={handleRun}
              disabled={executing || saving || refining || refinementBusy || form.status !== 'active'}
              title={form.status === 'active' ? '执行测试用例' : `当前状态为${statusLabels[form.status]}，不能执行`}
              className="flex items-center gap-2 px-4 py-2 bg-emerald-600 text-white rounded-lg hover:bg-emerald-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            >
              <PlayCircle className="w-4 h-4" /> {executing ? '执行中...' : '执行'}
            </button>
            <button
              onClick={handleDelete}
              disabled={executing || refining || refinementBusy}
              className="flex items-center gap-2 px-4 py-2 text-red-700 bg-red-50 dark:bg-red-900/30 dark:text-red-300 rounded-lg hover:bg-red-100 dark:hover:bg-red-900/50 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            >
              <Trash2 className="w-4 h-4" /> 删除
            </button>
            <button
              onClick={handleSave}
              disabled={saving || executing || refining || refinementBusy}
              className="flex items-center gap-2 px-4 py-2 bg-gray-900 dark:bg-gray-700 text-white rounded-lg hover:bg-gray-800 dark:hover:bg-gray-600 transition-colors disabled:opacity-50"
            >
              <Save className="w-4 h-4" /> {saving ? '保存中...' : '保存'}
            </button>
          </div>
        </div>
      </div>

      <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-5 space-y-4">
        <div className="grid grid-cols-1 lg:grid-cols-[1fr_180px] gap-4">
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">标题</label>
            <input
              value={form.title}
              onChange={(e) => setForm({ ...form, title: e.target.value })}
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">状态</label>
            <select
              value={form.status}
              onChange={(e) => setForm({ ...form, status: e.target.value as TestCaseStatus })}
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            >
              {(Object.keys(statusLabels) as TestCaseStatus[]).map((status) => (
                <option key={status} value={status}>{statusLabels[status]}</option>
              ))}
            </select>
          </div>
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">业务描述</label>
          <textarea
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
            rows={3}
            className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
          />
        </div>
      </div>

      <div className="grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_320px] gap-6">
        <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-5 space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <h2 className="text-lg font-semibold text-gray-900 dark:text-gray-100">自然语言修改</h2>
            {selectedRefinement && (
              <span className={`inline-flex items-center gap-1 px-3 py-1 rounded-full border text-sm font-medium ${refinementClasses[selectedRefinement.status]}`}>
                {refinementLabels[selectedRefinement.status]}
              </span>
            )}
          </div>

          <div className="space-y-3">
            <textarea
              value={refinePrompt}
              onChange={(e) => setRefinePrompt(e.target.value)}
              rows={3}
              placeholder="例如：增加密码为空时的错误提示校验"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
            />
            <div className="flex flex-wrap items-center justify-between gap-3">
              <label className="flex items-center gap-2 text-sm text-gray-600 dark:text-gray-400">
                <input
                  type="checkbox"
                  checked={useExecutionContext}
                  disabled={!selectedExecution}
                  onChange={(e) => setUseExecutionContext(e.target.checked)}
                  className="rounded border-gray-300 text-gray-900 focus:ring-gray-900"
                />
                引用当前执行报告
              </label>
              <button
                onClick={handleRefine}
                disabled={refining || refinementBusy || executing || saving || !refinePrompt.trim()}
                className="flex items-center gap-2 px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <Sparkles className="w-4 h-4" /> {refining ? '生成中...' : '生成建议'}
              </button>
            </div>
          </div>

          {refinementError && (
            <div className="text-sm text-red-600 dark:text-red-300">{refinementError}</div>
          )}

          {!selectedRefinement ? (
            <div className="text-sm text-gray-500 dark:text-gray-400 py-4">暂无修改建议。</div>
          ) : (
            <div className="space-y-4">
              <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                <div className="border border-gray-200 dark:border-gray-700 rounded-lg p-3">
                  <div className="text-xs text-gray-500 dark:text-gray-400">摘要</div>
                  <div className="text-sm text-gray-900 dark:text-gray-100 mt-1">{selectedRefinement.summary || '-'}</div>
                </div>
                <div className="border border-gray-200 dark:border-gray-700 rounded-lg p-3">
                  <div className="text-xs text-gray-500 dark:text-gray-400">风险提示</div>
                  <div className="text-sm text-gray-900 dark:text-gray-100 mt-1">{selectedRefinement.risk_notes || '-'}</div>
                </div>
              </div>

              <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
                <div>
                  <div className="text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">修改前</div>
                  <pre className="min-h-[220px] max-h-[360px] overflow-auto rounded-lg border border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-900 p-3 text-xs text-gray-800 dark:text-gray-100">
                    {JSON.stringify(selectedRefinement.original_blueprint, null, 2)}
                  </pre>
                </div>
                <div>
                  <div className="text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">修改后</div>
                  <pre className="min-h-[220px] max-h-[360px] overflow-auto rounded-lg border border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-900 p-3 text-xs text-gray-800 dark:text-gray-100">
                    {JSON.stringify(selectedRefinement.refined_blueprint, null, 2)}
                  </pre>
                </div>
              </div>

              {selectedRefinement.status === 'proposed' && (
                <div className="flex justify-end gap-2">
                  <button
                    onClick={handleDiscardRefinement}
                    disabled={refinementBusy || refining || executing || saving}
                    className="px-4 py-2 text-gray-700 bg-gray-100 dark:bg-gray-700 dark:text-gray-100 rounded-lg hover:bg-gray-200 dark:hover:bg-gray-600 transition-colors disabled:opacity-50"
                  >
                    放弃
                  </button>
                  <button
                    onClick={handleApplyRefinement}
                    disabled={refinementBusy || refining || executing || saving}
                    className="px-4 py-2 bg-gray-900 dark:bg-gray-700 text-white rounded-lg hover:bg-gray-800 dark:hover:bg-gray-600 transition-colors disabled:opacity-50"
                  >
                    {refinementBusy ? '处理中...' : '应用'}
                  </button>
                </div>
              )}
            </div>
          )}
        </div>

        <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-5">
          <h2 className="text-lg font-semibold text-gray-900 dark:text-gray-100">修改历史</h2>
          <div className="mt-4 space-y-2">
            {refinements.length === 0 ? (
              <div className="text-sm text-gray-500 dark:text-gray-400">暂无历史。</div>
            ) : refinements.map((refinement) => (
              <button
                key={refinement.id}
                onClick={() => loadRefinementDetail(refinement.id)}
                className={`w-full text-left border rounded-lg p-3 transition-colors ${
                  selectedRefinement?.id === refinement.id
                    ? 'border-gray-900 dark:border-gray-300'
                    : 'border-gray-200 dark:border-gray-700 hover:border-gray-400 dark:hover:border-gray-500'
                }`}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="text-sm font-medium text-gray-900 dark:text-gray-100 truncate">{refinement.user_prompt}</span>
                  <span className={`shrink-0 inline-flex items-center gap-1 px-2 py-0.5 rounded-full border text-xs ${refinementClasses[refinement.status]}`}>
                    {refinementLabels[refinement.status]}
                  </span>
                </div>
                <div className="mt-2 text-xs text-gray-500 dark:text-gray-400">
                  {new Date(refinement.created_at).toLocaleString()}
                </div>
              </button>
            ))}
          </div>
        </div>
      </div>

      <div className="grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_320px] gap-6">
        <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-5 space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <h2 className="text-lg font-semibold text-gray-900 dark:text-gray-100">最近执行报告</h2>
            </div>
            {selectedExecution && (
              <span className={`inline-flex items-center gap-1 px-3 py-1 rounded-full border text-sm font-medium ${executionClasses[selectedExecution.status]}`}>
                {executionIcon(selectedExecution.status)}
                {executionLabels[selectedExecution.status]}
              </span>
            )}
          </div>

          {executionLoading && !selectedExecution ? (
            <div className="text-sm text-gray-500 dark:text-gray-400 py-8">读取执行报告中...</div>
          ) : executionError ? (
            <div className="text-sm text-red-600 dark:text-red-300 py-4">{executionError}</div>
          ) : !selectedExecution ? (
            <div className="text-sm text-gray-500 dark:text-gray-400 py-8">暂无执行记录。</div>
          ) : (
            <div className="space-y-4">
              <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
                <div className="border border-gray-200 dark:border-gray-700 rounded-lg p-3">
                  <div className="flex items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
                    <Clock className="w-3.5 h-3.5" /> 耗时
                  </div>
                  <div className="text-base font-semibold text-gray-900 dark:text-gray-100 mt-1">
                    {formatDuration(selectedExecution.duration_ms)}
                  </div>
                </div>
                <div className="border border-gray-200 dark:border-gray-700 rounded-lg p-3">
                  <div className="text-xs text-gray-500 dark:text-gray-400">失败步骤</div>
                  <div className="text-base font-semibold text-gray-900 dark:text-gray-100 mt-1">
                    {selectedExecution.report_data.summary?.failed_step_index ?? '-'}
                  </div>
                </div>
                <div className="border border-gray-200 dark:border-gray-700 rounded-lg p-3">
                  <div className="text-xs text-gray-500 dark:text-gray-400">最终 URL</div>
                  <div className="text-sm text-gray-900 dark:text-gray-100 mt-1 truncate" title={selectedExecution.report_data.final_url || ''}>
                    {selectedExecution.report_data.final_url || '-'}
                  </div>
                </div>
              </div>

              {selectedExecution.error_message && (
                <div className="rounded-lg border border-red-200 dark:border-red-800 bg-red-50 dark:bg-red-900/20 p-3 text-sm text-red-700 dark:text-red-300">
                  {selectedExecution.error_message}
                </div>
              )}

              <div className="space-y-2">
                {(selectedExecution.report_data.steps || []).map((step) => (
                  <div
                    key={`${selectedExecution.id}-${step.index}`}
                    className={`border rounded-lg p-3 ${
                      step.status === 'passed'
                        ? 'border-gray-200 dark:border-gray-700'
                        : step.status === 'failed'
                          ? 'border-amber-300 bg-amber-50/60 dark:border-amber-800 dark:bg-amber-900/10'
                          : 'border-red-300 bg-red-50/60 dark:border-red-800 dark:bg-red-900/10'
                    }`}
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="flex items-center gap-2 text-sm font-medium text-gray-900 dark:text-gray-100">
                        <span>#{step.index}</span>
                        <span>{step.action}</span>
                        {step.description && <span className="text-gray-500 dark:text-gray-400">{step.description}</span>}
                      </div>
                      <span className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full border text-xs ${executionClasses[step.status]}`}>
                        {executionIcon(step.status)}
                        {executionLabels[step.status]}
                      </span>
                    </div>
                    <div className="mt-2 text-xs text-gray-500 dark:text-gray-400 flex flex-wrap gap-x-4 gap-y-1">
                      <span>目标：{step.target_summary || '-'}</span>
                      <span>耗时：{formatDuration(step.duration_ms)}</span>
                    </div>
                    {step.error && (
                      <div className="mt-2 text-sm text-red-700 dark:text-red-300">{step.error}</div>
                    )}
                  </div>
                ))}
              </div>

              {(selectedExecution.report_data.artifacts?.screenshots || []).length > 0 && (
                <div className="space-y-2">
                  <div className="text-sm font-medium text-gray-700 dark:text-gray-300">截图</div>
                  <div className="flex flex-wrap gap-2">
                    {(selectedExecution.report_data.artifacts?.screenshots || []).map((path) => (
                      <a
                        key={path}
                        href={path.startsWith('/') ? path : `/${path}`}
                        target="_blank"
                        rel="noreferrer"
                        className="text-sm text-indigo-600 dark:text-indigo-300 hover:underline"
                      >
                        {path}
                      </a>
                    ))}
                  </div>
                </div>
              )}
            </div>
          )}
        </div>

        <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-5">
          <h2 className="text-lg font-semibold text-gray-900 dark:text-gray-100">执行历史</h2>
          <div className="mt-4 space-y-2">
            {executions.length === 0 ? (
              <div className="text-sm text-gray-500 dark:text-gray-400">暂无历史。</div>
            ) : executions.map((execution) => (
              <button
                key={execution.id}
                onClick={() => loadExecutionDetail(execution.id)}
                className={`w-full text-left border rounded-lg p-3 transition-colors ${
                  selectedExecution?.id === execution.id
                    ? 'border-gray-900 dark:border-gray-300'
                    : 'border-gray-200 dark:border-gray-700 hover:border-gray-400 dark:hover:border-gray-500'
                }`}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="text-sm font-medium text-gray-900 dark:text-gray-100">#{execution.id}</span>
                  <span className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full border text-xs ${executionClasses[execution.status]}`}>
                    {executionIcon(execution.status)}
                    {executionLabels[execution.status]}
                  </span>
                </div>
                <div className="mt-2 text-xs text-gray-500 dark:text-gray-400">
                  {new Date(execution.created_at).toLocaleString()} / {formatDuration(execution.duration_ms)}
                </div>
              </button>
            ))}
          </div>
        </div>
      </div>

      <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
        <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-5">
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">Blueprint JSON</label>
          <textarea
            value={form.blueprintText}
            onChange={(e) => setForm({ ...form, blueprintText: e.target.value })}
            spellCheck={false}
            rows={22}
            className="w-full font-mono text-sm px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-900 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
          />
        </div>
        <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-5">
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">ScriptContent</label>
          <textarea
            value={form.script_content}
            onChange={(e) => setForm({ ...form, script_content: e.target.value })}
            spellCheck={false}
            rows={22}
            className="w-full font-mono text-sm px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-900 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-gray-900"
          />
        </div>
      </div>

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
