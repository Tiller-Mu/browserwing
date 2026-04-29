import { useState, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { FileText, Plus, Video, Trash2, ArrowLeft, Bot, ExternalLink, ListChecks } from 'lucide-react';
import { projectApi, TestPage } from '../api/project';
import Toast from '../components/Toast';
import { Modal } from '../components/Modal';

export default function TestPageManager() {
  const { projectId, versionId } = useParams();
  const navigate = useNavigate();
  
  const [pages, setPages] = useState<TestPage[]>([]);
  const [loading, setLoading] = useState(true);
  const [toast, setToast] = useState<{ message: string; type: 'success' | 'error' | 'info' } | null>(null);
  
  // Modals
  const [showCreateModal, setShowCreateModal] = useState(false);
  const [newPage, setNewPage] = useState({ name: '', path: '', description: '' });

  useEffect(() => {
    if (projectId && versionId) {
      loadPages();
    }
  }, [projectId, versionId]);

  const loadPages = async () => {
    try {
      setLoading(true);
      const response = await projectApi.getPages(Number(projectId), Number(versionId));
      setPages(response.data || []);
    } catch (error: any) {
      console.error('Failed to load pages:', error);
      showToast('获取测试页面列表失败', 'error');
    } finally {
      setLoading(false);
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
    } catch (error: any) {
      showToast('页面创建失败', 'error');
    }
  };

  const handleDeletePage = async (pageId: number) => {
    if (!window.confirm('确定要删除该页面及其挂载的所有录制轨迹和测试用例吗？不可恢复！')) return;

    try {
      await projectApi.deletePage(Number(projectId), Number(versionId), pageId);
      showToast('删除成功', 'success');
      loadPages();
    } catch (error: any) {
      showToast('删除失败', 'error');
    }
  };

  const showToast = (message: string, type: 'success' | 'error' | 'info' = 'info') => {
    setToast({ message, type });
  };

  const navigateToRecord = (pageId: number) => {
    // 跳转到浏览器录制组件，并传递 pageId 作为上下文
    navigate(`/browser?projectId=${projectId}&versionId=${versionId}&pageId=${pageId}`);
  };

  return (
    <div className="space-y-6 lg:space-y-8 animate-fade-in">
      {/* Header */}
      <div className="space-y-4">
        <button 
          onClick={() => navigate('/projects')}
          className="flex items-center gap-1 text-sm text-gray-500 hover:text-indigo-600 transition-colors"
        >
          <ArrowLeft className="w-4 h-4" /> 返回项目管理
        </button>
        <div className="flex items-center justify-between">
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

      {/* 页面列表 */}
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
            const hasMainScript = page.scripts && page.scripts.length > 0;
            const casesCount = page.test_cases ? page.test_cases.length : 0;
            
            return (
              <div key={page.id} className="bg-white dark:bg-gray-800 rounded-xl shadow-sm border border-gray-200 dark:border-gray-700 overflow-hidden flex flex-col">
                <div className="p-5 border-b border-gray-100 dark:border-gray-700 flex items-start justify-between bg-gray-50/50 dark:bg-gray-900/20">
                  <div className="flex items-start gap-3">
                    <div className="p-2 bg-indigo-50 dark:bg-indigo-900/30 rounded-lg mt-0.5">
                      <FileText className="w-5 h-5 text-indigo-600 dark:text-indigo-400" />
                    </div>
                    <div>
                      <h3 className="text-lg font-bold text-gray-900 dark:text-gray-100 flex items-center gap-2">
                        {page.name}
                        {hasMainScript && (
                          <span className="px-2 py-0.5 text-xs font-medium bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400 rounded-full flex items-center gap-1">
                            <Video className="w-3 h-3" /> 主流程已就绪
                          </span>
                        )}
                      </h3>
                      {page.path && (
                        <p className="text-sm text-gray-500 dark:text-gray-400 mt-1 font-mono flex items-center gap-1">
                          <ExternalLink className="w-3.5 h-3.5" /> {page.path}
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
                  {!hasMainScript ? (
                    <div className="flex-1 flex flex-col items-center justify-center text-center py-6 border-2 border-dashed border-gray-200 dark:border-gray-700 rounded-lg">
                      <div className="w-12 h-12 bg-gray-50 dark:bg-gray-900 rounded-full flex items-center justify-center mb-3">
                        <Video className="w-6 h-6 text-gray-400" />
                      </div>
                      <h4 className="text-gray-900 dark:text-gray-100 font-medium mb-1">缺少主流程录制</h4>
                      <p className="text-sm text-gray-500 dark:text-gray-400 mb-4 max-w-xs">
                        大模型需要一条您亲自操作的正向主流程录制轨迹，作为基准来推导异常用例。
                      </p>
                      <button 
                        onClick={() => navigateToRecord(page.id)}
                        className="px-4 py-2 bg-indigo-50 text-indigo-700 dark:bg-indigo-900/30 dark:text-indigo-400 rounded-lg font-medium hover:bg-indigo-100 dark:hover:bg-indigo-900/50 transition-colors flex items-center gap-2"
                      >
                        <Video className="w-4 h-4" /> 立即去录制
                      </button>
                    </div>
                  ) : (
                    <div className="flex-1 flex flex-col">
                      <div className="mb-6 bg-indigo-50/50 dark:bg-indigo-900/10 rounded-lg p-4 border border-indigo-100 dark:border-indigo-900/30">
                        <div className="flex items-center justify-between mb-2">
                          <h4 className="font-medium text-sm text-indigo-900 dark:text-indigo-300 flex items-center gap-2">
                            <Video className="w-4 h-4 text-indigo-500" /> 主流程录制轨迹
                          </h4>
                          <div className="flex gap-2">
                             <button 
                               onClick={() => navigateToRecord(page.id)}
                               className="text-xs text-indigo-600 hover:text-indigo-700 dark:text-indigo-400 dark:hover:text-indigo-300 font-medium flex items-center gap-1 transition-colors"
                             >
                               <Video className="w-3.5 h-3.5" /> 重新录制
                             </button>
                          </div>
                        </div>
                        <div className="flex items-center justify-between text-sm">
                          <span className="text-gray-600 dark:text-gray-400">
                            已绑定脚本: <span className="font-semibold text-gray-900 dark:text-gray-100">{page.scripts?.[0]?.name || '未命名脚本'}</span>
                          </span>
                          <span className="text-gray-500 dark:text-gray-500 text-xs">
                            {page.scripts?.[0]?.updated_at ? new Date(page.scripts[0].updated_at).toLocaleString() : ''}
                          </span>
                        </div>
                      </div>

                      <div className="flex items-center justify-between mb-4">
                        <h4 className="font-medium text-gray-900 dark:text-gray-100 flex items-center gap-2">
                          <ListChecks className="w-4 h-4 text-indigo-500" /> 衍生测试用例 ({casesCount})
                        </h4>
                      </div>
                      
                      {casesCount === 0 ? (
                        <div className="flex-1 bg-gray-50 dark:bg-gray-900/50 rounded-lg p-6 flex flex-col items-center justify-center text-center">
                          <Bot className="w-10 h-10 text-gray-400 mb-3" />
                          <p className="text-sm text-gray-600 dark:text-gray-400 mb-4">
                            主流程已就绪，可以召唤 AI 进行用例裂变了！
                          </p>
                          <button className="px-4 py-2 bg-gray-900 dark:bg-gray-700 text-white rounded-lg shadow-sm hover:bg-gray-800 dark:hover:bg-gray-600 transition-colors flex items-center gap-2">
                            <Bot className="w-4 h-4" /> 智能生成测试用例
                          </button>
                        </div>
                      ) : (
                        <div className="space-y-3 flex-1 overflow-y-auto max-h-[300px] pr-2">
                          {page.test_cases?.map((tc: any) => (
                            <div key={tc.id} className="p-3 border border-gray-100 dark:border-gray-700 rounded-lg hover:border-indigo-200 dark:hover:border-indigo-800 transition-colors cursor-pointer group">
                              <div className="flex justify-between items-start mb-1">
                                <span className="font-medium text-sm text-gray-900 dark:text-gray-100 group-hover:text-indigo-600 dark:group-hover:text-indigo-400 transition-colors">
                                  {tc.title}
                                </span>
                                <span className={`text-[10px] px-1.5 py-0.5 rounded font-medium ${
                                  tc.status === 'passed' ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400' :
                                  tc.status === 'failed' ? 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400' :
                                  'bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400'
                                }`}>
                                  {tc.status || '未执行'}
                                </span>
                              </div>
                              <p className="text-xs text-gray-500 dark:text-gray-400 line-clamp-1">
                                {tc.description}
                              </p>
                            </div>
                          ))}
                        </div>
                      )}
                    </div>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      )}

      {/* 新建页面弹窗 */}
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
