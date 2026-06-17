import { useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { AlertTriangle, ArrowLeft, Clipboard, Code2, FileJson, ListChecks, RefreshCw } from 'lucide-react';
import { projectApi, type PageRecordingDetailResponse } from '../api/project';
import Toast from '../components/Toast';
import { buildP45RecordingDetailView } from './p45RecordingUiContract';

type RecordingTab = 'actions' | 'snapshot' | 'meta';

const tabLabels: Record<RecordingTab, string> = {
  actions: '动作轨迹',
  snapshot: '页面快照',
  meta: '录制元数据',
};

export default function PageRecordingDetail() {
  const { projectId, versionId, pageId } = useParams();
  const navigate = useNavigate();
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [detail, setDetail] = useState<PageRecordingDetailResponse | null>(null);
  const [error, setError] = useState('');
  const [activeTab, setActiveTab] = useState<RecordingTab>('actions');
  const [toast, setToast] = useState<{ message: string; type: 'success' | 'error' | 'info' } | null>(null);

  const ids = useMemo(() => {
    if (!projectId || !versionId || !pageId) return null;
    return {
      projectId: Number(projectId),
      versionId: Number(versionId),
      pageId: Number(pageId),
    };
  }, [pageId, projectId, versionId]);

  useEffect(() => {
    loadRecording();
  }, [ids]);

  const showToast = (message: string, type: 'success' | 'error' | 'info' = 'info') => {
    setToast({ message, type });
  };

  const loadRecording = async (silent = false) => {
    if (!ids) return;
    try {
      if (silent) {
        setRefreshing(true);
      } else {
        setLoading(true);
      }
      setError('');
      const response = await projectApi.getLatestPageRecording(ids.projectId, ids.versionId, ids.pageId);
      setDetail(response.data);
    } catch (err: any) {
      const code = err.response?.data?.code;
      setError(code === 'page_recording_not_found' ? '当前页面还没有保存主流程录制。' : err.response?.data?.error || '读取录制结果失败');
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  };

  const copyText = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      showToast('已复制', 'success');
    } catch {
      showToast('复制失败', 'error');
    }
  };

  const selectedJSON = () => {
    if (!detail) return null;
    if (activeTab === 'actions') return detail.recording.action_trace_json;
    if (activeTab === 'snapshot') return detail.recording.dom_snapshot_json;
    return detail.recording.recording_meta_json;
  };

  const jsonText = JSON.stringify(selectedJSON(), null, 2);
  const recordingView = detail ? buildP45RecordingDetailView(detail.recording) : null;
  const actions = Array.isArray(detail?.recording.action_trace_json) ? detail?.recording.action_trace_json as Record<string, any>[] : [];
  const diagnostics = detail?.recording.diagnostics;
  const parseErrors = diagnostics?.parse_errors || [];
  const removedFields = diagnostics?.sensitive_fields_removed || [];
  const backPath = ids ? `/projects/${ids.projectId}/versions/${ids.versionId}/pages` : '/projects';

  if (loading) {
    return (
      <div className="flex items-center justify-center py-12">
        <div className="text-gray-500 dark:text-gray-400">加载中...</div>
      </div>
    );
  }

  if (!detail || !recordingView || !ids) {
    return (
      <div className="space-y-4">
        <button
          onClick={() => navigate(backPath)}
          className="flex items-center gap-1 text-sm text-gray-500 hover:text-indigo-600 transition-colors"
        >
          <ArrowLeft className="w-4 h-4" /> 返回页面管理
        </button>
        <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-8 text-gray-500 dark:text-gray-400">
          {error || '录制结果不存在或当前层级无权访问。'}
        </div>
        {toast && <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />}
      </div>
    );
  }

  return (
    <div className="space-y-6 animate-fade-in">
      <div className="space-y-4">
        <button
          onClick={() => navigate(backPath)}
          className="flex items-center gap-1 text-sm text-gray-500 hover:text-indigo-600 transition-colors"
        >
          <ArrowLeft className="w-4 h-4" /> 返回页面管理
        </button>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 className="text-2xl font-bold text-gray-900 dark:text-gray-100">录制结果</h1>
            <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">
              {detail.page.name} / {detail.recording.name || '未命名录制'}
            </p>
            {detail.page.path && (
              <p className="mt-1 font-mono text-xs text-gray-500 dark:text-gray-400 break-all">{detail.page.path}</p>
            )}
          </div>
          <button
            onClick={() => loadRecording(true)}
            disabled={refreshing}
            className="inline-flex items-center gap-2 px-4 py-2 bg-white dark:bg-gray-900 border border-gray-200 dark:border-gray-700 text-gray-700 dark:text-gray-200 rounded-lg hover:border-indigo-300 dark:hover:border-indigo-700 disabled:opacity-50"
          >
            <RefreshCw className={`w-4 h-4 ${refreshing ? 'animate-spin' : ''}`} /> 刷新
          </button>
        </div>
      </div>

      <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3">
        <MetricPanel label="动作数" value={recordingView.actionCount} />
        <MetricPanel label="快照元素" value={recordingView.snapshotElementCount} />
        <MetricPanel label="质量状态" value={recordingView.status === 'ready' ? '可用' : '需检查'} tone={recordingView.status === 'ready' ? 'ok' : 'warn'} />
        <MetricPanel label="更新时间" value={formatTime(detail.recording.updated_at)} />
      </div>

      {recordingView.status === 'warning' && (
        <div className="bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 rounded-lg p-4 text-sm text-amber-900 dark:text-amber-200">
          <div className="flex items-start gap-2">
            <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
            <div className="space-y-1">
              {recordingView.qualityMessages.map((message) => (
                <div key={message}>{message}</div>
              ))}
              {parseErrors.map((item) => (
                <div key={`${item.field}:${item.code}`}>{item.field} 解析失败：{item.code}</div>
              ))}
            </div>
          </div>
        </div>
      )}

      {removedFields.length > 0 && (
        <div className="bg-gray-50 dark:bg-gray-900/50 border border-gray-200 dark:border-gray-700 rounded-lg p-3 text-xs text-gray-600 dark:text-gray-400">
          已从展示结果中移除敏感字段：{removedFields.join(', ')}
        </div>
      )}

      <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 overflow-hidden">
        <div className="px-5 py-4 border-b border-gray-100 dark:border-gray-700 flex items-center gap-2">
          <ListChecks className="w-4 h-4 text-gray-500" />
          <h2 className="text-lg font-semibold text-gray-900 dark:text-gray-100">动作轨迹</h2>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full min-w-[900px] text-sm">
            <thead className="bg-gray-50 dark:bg-gray-900/50 text-gray-500 dark:text-gray-400">
              <tr>
                <th className="px-4 py-3 text-left font-medium">#</th>
                <th className="px-4 py-3 text-left font-medium">类型</th>
                <th className="px-4 py-3 text-left font-medium">目标</th>
                <th className="px-4 py-3 text-left font-medium">值 / URL</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-700">
              {actions.length === 0 ? (
                <tr>
                  <td colSpan={4} className="px-4 py-6 text-center text-gray-500 dark:text-gray-400">没有可展示的动作</td>
                </tr>
              ) : (
                actions.map((action, index) => (
                  <tr key={index} className="align-top hover:bg-gray-50/70 dark:hover:bg-gray-900/30">
                    <td className="px-4 py-3 text-gray-500 dark:text-gray-400">{index + 1}</td>
                    <td className="px-4 py-3 font-medium text-gray-900 dark:text-gray-100">{String(action.type || action.action || '-')}</td>
                    <td className="px-4 py-3 text-gray-700 dark:text-gray-300">{targetSummary(action)}</td>
                    <td className="px-4 py-3 font-mono text-xs text-gray-600 dark:text-gray-400 break-all">{valueSummary(action)}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>

      <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 overflow-hidden">
        <div className="px-5 py-3 border-b border-gray-100 dark:border-gray-700 flex flex-wrap items-center justify-between gap-3">
          <div className="inline-flex rounded-lg border border-gray-200 dark:border-gray-700 overflow-hidden">
            {(Object.keys(tabLabels) as RecordingTab[]).map((tab) => (
              <button
                key={tab}
                onClick={() => setActiveTab(tab)}
                className={`px-4 py-2 text-sm inline-flex items-center gap-2 ${activeTab === tab
                  ? 'bg-gray-900 text-white dark:bg-gray-700'
                  : 'bg-white dark:bg-gray-900 text-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-gray-800'
                  }`}
              >
                {tab === 'actions' ? <Code2 className="w-4 h-4" /> : <FileJson className="w-4 h-4" />}
                {tabLabels[tab]}
              </button>
            ))}
          </div>
          <button
            onClick={() => copyText(jsonText)}
            className="inline-flex items-center gap-2 px-3 py-2 text-sm bg-white dark:bg-gray-900 border border-gray-200 dark:border-gray-700 text-gray-700 dark:text-gray-200 rounded-lg hover:border-indigo-300 dark:hover:border-indigo-700"
          >
            <Clipboard className="w-4 h-4" /> 复制 JSON
          </button>
        </div>
        <pre className="m-0 p-5 max-h-[520px] overflow-auto text-xs leading-5 bg-gray-950 text-gray-100">
          {jsonText}
        </pre>
      </div>

      {toast && <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />}
    </div>
  );
}

function MetricPanel({ label, value, tone = 'default' }: { label: string; value: string | number; tone?: 'default' | 'ok' | 'warn' }) {
  const color = tone === 'ok'
    ? 'text-emerald-700 dark:text-emerald-300'
    : tone === 'warn'
      ? 'text-amber-700 dark:text-amber-300'
      : 'text-gray-900 dark:text-gray-100';
  return (
    <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-4">
      <div className="text-xs text-gray-500 dark:text-gray-400">{label}</div>
      <div className={`mt-2 text-xl font-semibold ${color}`}>{value}</div>
    </div>
  );
}

function targetSummary(action: Record<string, any>) {
  if (action.recorded_selector) return `recorded_selector: ${action.recorded_selector}`;
  if (action.selector) return `selector: ${action.selector}`;
  if (action.css) return `css: ${action.css}`;
  if (action.xpath) return `xpath: ${action.xpath}`;
  if (action.ref_id) return `ref_id: ${action.ref_id}`;
  if (action.text) return `text: ${action.text}`;
  const target = action.target && typeof action.target === 'object' ? action.target as Record<string, any> : null;
  if (!target) return '缺少 target';
  if (target.recorded_selector) return `recorded_selector: ${target.recorded_selector}`;
  if (target.selector) return `selector: ${target.selector}`;
  if (target.css) return `css: ${target.css}`;
  if (target.xpath) return `xpath: ${target.xpath}`;
  if (target.role && target.text) return `${target.role}: ${target.text}`;
  if (target.text) return `text: ${target.text}`;
  if (target.label) return `label: ${target.label}`;
  if (target.placeholder) return `placeholder: ${target.placeholder}`;
  if (target.ref_id) return `ref_id: ${target.ref_id}`;
  return 'target 为空';
}

function valueSummary(action: Record<string, any>) {
  const value = action.value ?? action.url ?? '';
  if (value === '') return '-';
  const text = typeof value === 'string' ? value : JSON.stringify(value);
  return text.length > 160 ? `${text.slice(0, 160)}...` : text;
}

function formatTime(value: string) {
  if (!value) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}
