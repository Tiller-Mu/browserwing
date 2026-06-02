import { useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { ArrowLeft, Save, Trash2 } from 'lucide-react';
import { projectApi, type TestCaseDetail as TestCaseDetailType, type TestCaseStatus } from '../api/project';
import Toast from '../components/Toast';

const statusLabels: Record<TestCaseStatus, string> = {
  active: '启用',
  draft: '草稿',
  archived: '归档'
};

export default function TestCaseDetail() {
  const { projectId, versionId, pageId, testCaseId } = useParams();
  const navigate = useNavigate();

  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testCase, setTestCase] = useState<TestCaseDetailType | null>(null);
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
    if (!ids) return;
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
    if (!ids || !window.confirm('确定要删除该测试用例吗？不可恢复！')) return;

    try {
      await projectApi.deleteTestCase(ids.projectId, ids.versionId, ids.pageId, ids.testCaseId);
      showToast('删除成功', 'success');
      navigate(`/projects/${ids.projectId}/versions/${ids.versionId}/pages`);
    } catch (error: any) {
      showToast(error.response?.data?.error || '删除失败', 'error');
    }
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
              onClick={handleDelete}
              className="flex items-center gap-2 px-4 py-2 text-red-700 bg-red-50 dark:bg-red-900/30 dark:text-red-300 rounded-lg hover:bg-red-100 dark:hover:bg-red-900/50 transition-colors"
            >
              <Trash2 className="w-4 h-4" /> 删除
            </button>
            <button
              onClick={handleSave}
              disabled={saving}
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
