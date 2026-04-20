import { useState, useEffect } from 'react'
import { useLanguage } from '../i18n'
import { copyToClipboard as clipboardCopy } from '../utils/clipboard'
import { 
  listUsers, 
  createUser, 
  deleteUser, 
  updatePassword,
  listApiKeys, 
  createApiKey, 
  deleteApiKey,
  User,
  ApiKey
} from '../api/client'
import { Modal } from '../components/Modal'
import ConfirmDialog from '../components/ConfirmDialog'
import Toast from '../components/Toast'

export default function Settings() {
  const { t } = useLanguage()
  const [activeTab, setActiveTab] = useState<'users' | 'apikeys'>('users')
  
  // 用户管理状态
  const [users, setUsers] = useState<User[]>([])
  const [showCreateUserModal, setShowCreateUserModal] = useState(false)
  const [showPasswordModal, setShowPasswordModal] = useState(false)
  const [selectedUserId, setSelectedUserId] = useState<string>('')
  const [newUsername, setNewUsername] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [oldPassword, setOldPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  
  // ApiKey管理状态
  const [apiKeys, setApiKeys] = useState<ApiKey[]>([])
  const [showCreateApiKeyModal, setShowCreateApiKeyModal] = useState(false)
  const [apiKeyName, setApiKeyName] = useState('')
  const [apiKeyDescription, setApiKeyDescription] = useState('')
  const [createdApiKey, setCreatedApiKey] = useState<string>('')
  const [justCopied, setJustCopied] = useState<string>('')
  
  // 通用状态
  const [loading, setLoading] = useState(false)
  const [deleteConfirm, setDeleteConfirm] = useState<{show: boolean, type: 'user' | 'apikey', id: string, name: string}>({
    show: false,
    type: 'user',
    id: '',
    name: ''
  })
  const [toast, setToast] = useState<{show: boolean, message: string, type: 'success' | 'error'}>({
    show: false,
    message: '',
    type: 'success'
  })

  useEffect(() => {
    if (activeTab === 'users') {
      loadUsers()
    } else {
      loadApiKeys()
    }
  }, [activeTab])

  const loadUsers = async () => {
    try {
      const data = await listUsers()
      setUsers(data)
    } catch (error: any) {
      showToast(error.response?.data?.error || t('error.loadUsersFailed'), 'error')
    }
  }

  const loadApiKeys = async () => {
    try {
      const data = await listApiKeys()
      setApiKeys(data || [])
    } catch (error: any) {
      showToast(error.response?.data?.error || t('error.loadApiKeysFailed'), 'error')
      setApiKeys([]) // 确保在错误时也设置为空数组
    }
  }

  const handleCreateUser = async () => {
    if (!newUsername || !newPassword) {
      showToast(t('error.fillAllFields'), 'error')
      return
    }
    
    if (newPassword !== confirmPassword) {
      showToast(t('error.passwordMismatch'), 'error')
      return
    }

    setLoading(true)
    try {
      await createUser(newUsername, newPassword)
      showToast(t('success.userCreated'), 'success')
      setShowCreateUserModal(false)
      setNewUsername('')
      setNewPassword('')
      setConfirmPassword('')
      loadUsers()
    } catch (error: any) {
      showToast(error.response?.data?.error || t('error.createUserFailed'), 'error')
    } finally {
      setLoading(false)
    }
  }

  const handleUpdatePassword = async () => {
    if (!oldPassword || !newPassword) {
      showToast(t('error.fillAllFields'), 'error')
      return
    }
    
    if (newPassword !== confirmPassword) {
      showToast(t('error.passwordMismatch'), 'error')
      return
    }

    setLoading(true)
    try {
      await updatePassword(selectedUserId, oldPassword, newPassword)
      showToast(t('success.passwordUpdated'), 'success')
      setShowPasswordModal(false)
      setOldPassword('')
      setNewPassword('')
      setConfirmPassword('')
      setSelectedUserId('')
    } catch (error: any) {
      showToast(error.response?.data?.error || t('error.updatePasswordFailed'), 'error')
    } finally {
      setLoading(false)
    }
  }

  const handleDeleteUser = async () => {
    setLoading(true)
    try {
      await deleteUser(deleteConfirm.id)
      showToast(t('success.userDeleted'), 'success')
      setDeleteConfirm({ show: false, type: 'user', id: '', name: '' })
      loadUsers()
    } catch (error: any) {
      showToast(error.response?.data?.error || t('error.deleteUserFailed'), 'error')
    } finally {
      setLoading(false)
    }
  }

  const handleCreateApiKey = async () => {
    if (!apiKeyName) {
      showToast(t('error.fillAllFields'), 'error')
      return
    }

    setLoading(true)
    try {
      const data = await createApiKey(apiKeyName, apiKeyDescription)
      setCreatedApiKey(data.key)
      showToast(t('success.apiKeyCreated'), 'success')
      loadApiKeys()
    } catch (error: any) {
      showToast(error.response?.data?.error || t('error.createApiKeyFailed'), 'error')
    } finally {
      setLoading(false)
    }
  }

  const handleDeleteApiKey = async () => {
    setLoading(true)
    try {
      await deleteApiKey(deleteConfirm.id)
      showToast(t('success.apiKeyDeleted'), 'success')
      setDeleteConfirm({ show: false, type: 'apikey', id: '', name: '' })
      loadApiKeys()
    } catch (error: any) {
      showToast(error.response?.data?.error || t('error.deleteApiKeyFailed'), 'error')
    } finally {
      setLoading(false)
    }
  }

  const showToast = (message: string, type: 'success' | 'error') => {
    setToast({ show: true, message, type })
  }

  const copyToClipboard = (text: string, id?: string) => {
    clipboardCopy(text)
    showToast(t('success.copiedToClipboard'), 'success')
    if (id) {
      setJustCopied(id)
      setTimeout(() => setJustCopied(''), 2000)
    }
  }

  return (
    <div>
      <h1 className="text-2xl font-bold mb-6 text-gray-900 dark:text-white">
        {t('settings.title')}
      </h1>

      {/* 标签页 */}
      <div className="border-b border-gray-200 dark:border-gray-700 mb-6">
        <nav className="-mb-px flex space-x-8">
          <button
            onClick={() => setActiveTab('users')}
            className={`${
              activeTab === 'users'
                ? 'border-gray-900 text-gray-900 dark:border-gray-100 dark:text-gray-100'
                : 'border-transparent text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 hover:border-gray-300 dark:hover:border-gray-600'
            } whitespace-nowrap py-4 px-1 border-b-2 font-medium text-sm transition-colors`}
          >
            {t('settings.users')}
          </button>
          <button
            onClick={() => setActiveTab('apikeys')}
            className={`${
              activeTab === 'apikeys'
                ? 'border-gray-900 text-gray-900 dark:border-gray-100 dark:text-gray-100'
                : 'border-transparent text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 hover:border-gray-300 dark:hover:border-gray-600'
            } whitespace-nowrap py-4 px-1 border-b-2 font-medium text-sm transition-colors`}
          >
            {t('settings.apiKeys')}
          </button>
        </nav>
      </div>

      {/* 用户管理 */}
      {activeTab === 'users' && (
        <div>
          <div className="flex justify-between items-center mb-4">
            <h2 className="text-xl font-semibold text-gray-900 dark:text-white">
              {t('settings.userManagement')}
            </h2>
            <button
              onClick={() => setShowCreateUserModal(true)}
              className="px-4 py-2 bg-gray-900 dark:bg-gray-100 text-white dark:text-gray-900 rounded-lg hover:bg-gray-800 dark:hover:bg-gray-200 transition-colors shadow-sm"
            >
              {t('settings.createUser')}
            </button>
          </div>

          <div className="bg-white dark:bg-gray-800 shadow overflow-hidden sm:rounded-md">
            <ul className="divide-y divide-gray-200 dark:divide-gray-700">
              {users.map((user) => (
                <li key={user.id} className="px-6 py-4">
                  <div className="flex items-center justify-between">
                    <div>
                      <h3 className="text-lg font-medium text-gray-900 dark:text-white">
                        {user.username}
                      </h3>
                      <p className="text-sm text-gray-500 dark:text-gray-400">
                        {t('settings.createdAt')}: {new Date(user.created_at).toLocaleString()}
                      </p>
                    </div>
                    <div className="flex space-x-2">
                      <button
                        onClick={() => {
                          setSelectedUserId(user.id)
                          setShowPasswordModal(true)
                        }}
                        className="px-3 py-1.5 bg-gray-100 dark:bg-gray-700 text-gray-900 dark:text-gray-100 rounded-lg hover:bg-gray-200 dark:hover:bg-gray-600 transition-colors text-sm"
                      >
                        {t('settings.changePassword')}
                      </button>
                      <button
                        onClick={() => setDeleteConfirm({ show: true, type: 'user', id: user.id, name: user.username })}
                        className="px-3 py-1.5 bg-gray-100 dark:bg-gray-700 text-gray-700 dark:text-gray-300 rounded-lg hover:bg-gray-200 dark:hover:bg-gray-600 hover:text-gray-900 dark:hover:text-gray-100 transition-colors text-sm"
                      >
                        {t('common.delete')}
                      </button>
                    </div>
                  </div>
                </li>
              ))}
            </ul>
          </div>
        </div>
      )}

      {/* ApiKey管理 */}
      {activeTab === 'apikeys' && (
        <div>
          <div className="flex justify-between items-center mb-4">
            <h2 className="text-xl font-semibold text-gray-900 dark:text-white">
              {t('settings.apiKeyManagement')}
            </h2>
            <button
              onClick={() => setShowCreateApiKeyModal(true)}
              className="px-4 py-2 bg-gray-900 dark:bg-gray-100 text-white dark:text-gray-900 rounded-lg hover:bg-gray-800 dark:hover:bg-gray-200 transition-colors shadow-sm"
            >
              {t('settings.createApiKey')}
            </button>
          </div>

          <div className="bg-white dark:bg-gray-800 shadow overflow-hidden sm:rounded-md">
            <ul className="divide-y divide-gray-200 dark:divide-gray-700">
              {apiKeys.map((apiKey) => (
                <li key={apiKey.id} className="px-6 py-4">
                  <div className="flex items-center justify-between">
                    <div className="flex-1">
                      <h3 className="text-lg font-medium text-gray-900 dark:text-white">
                        {apiKey.name}
                      </h3>
                      {apiKey.description && (
                        <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">
                          {apiKey.description}
                        </p>
                      )}
                      <div className="mt-2 flex items-center space-x-2">
                        <code className="px-2 py-1 bg-gray-100 dark:bg-gray-700 rounded text-sm font-mono">
                          {apiKey.key.length > 50 ? apiKey.key.substring(0, 50) + '...' : apiKey.key}
                        </code>
                        <button
                          onClick={() => copyToClipboard(apiKey.key, apiKey.id)}
                          className={`text-sm transition-all ${
                            justCopied === apiKey.id
                              ? 'text-green-600 dark:text-green-400 font-medium'
                              : 'text-gray-700 dark:text-gray-300 hover:text-gray-900 dark:hover:text-gray-100 underline'
                          }`}
                        >
                          {justCopied === apiKey.id ? '✓ ' + t('success.copied') : t('common.copy')}
                        </button>
                      </div>
                      <p className="text-sm text-gray-500 dark:text-gray-400 mt-2">
                        {t('settings.createdAt')}: {new Date(apiKey.created_at).toLocaleString()}
                      </p>
                    </div>
                    <button
                      onClick={() => setDeleteConfirm({ show: true, type: 'apikey', id: apiKey.id, name: apiKey.name })}
                      className="px-3 py-1.5 bg-gray-100 dark:bg-gray-700 text-gray-700 dark:text-gray-300 rounded-lg hover:bg-gray-200 dark:hover:bg-gray-600 hover:text-gray-900 dark:hover:text-gray-100 transition-colors text-sm"
                    >
                      {t('common.delete')}
                    </button>
                  </div>
                </li>
              ))}
            </ul>
          </div>
        </div>
      )}

      {/* 创建用户模态框 */}
      <Modal
        isOpen={showCreateUserModal}
        onClose={() => {
          setShowCreateUserModal(false)
          setNewUsername('')
          setNewPassword('')
          setConfirmPassword('')
        }}
        title={t('settings.createUser')}
      >
        <div className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              {t('auth.username')}
            </label>
            <input
              type="text"
              value={newUsername}
              onChange={(e) => setNewUsername(e.target.value)}
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg dark:bg-gray-700 dark:text-white focus:outline-none focus:ring-2 focus:ring-gray-900 dark:focus:ring-gray-100 focus:border-transparent transition-all"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              {t('auth.password')}
            </label>
            <input
              type="password"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg dark:bg-gray-700 dark:text-white focus:outline-none focus:ring-2 focus:ring-gray-900 dark:focus:ring-gray-100 focus:border-transparent transition-all"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              {t('auth.confirmPassword')}
            </label>
            <input
              type="password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg dark:bg-gray-700 dark:text-white focus:outline-none focus:ring-2 focus:ring-gray-900 dark:focus:ring-gray-100 focus:border-transparent transition-all"
            />
          </div>
          <div className="flex justify-end space-x-2 mt-4">
            <button
              onClick={() => setShowCreateUserModal(false)}
              className="px-4 py-2 border border-gray-300 dark:border-gray-600 rounded-lg text-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-gray-700 transition-colors"
            >
              {t('common.cancel')}
            </button>
            <button
              onClick={handleCreateUser}
              disabled={loading}
              className="px-4 py-2 bg-gray-900 dark:bg-gray-100 text-white dark:text-gray-900 rounded-lg hover:bg-gray-800 dark:hover:bg-gray-200 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              {loading ? t('common.creating') : t('common.create')}
            </button>
          </div>
        </div>
      </Modal>

      {/* 修改密码模态框 */}
      <Modal
        isOpen={showPasswordModal}
        onClose={() => {
          setShowPasswordModal(false)
          setOldPassword('')
          setNewPassword('')
          setConfirmPassword('')
          setSelectedUserId('')
        }}
        title={t('settings.changePassword')}
      >
        <div className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              {t('auth.oldPassword')}
            </label>
            <input
              type="password"
              value={oldPassword}
              onChange={(e) => setOldPassword(e.target.value)}
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg dark:bg-gray-700 dark:text-white focus:outline-none focus:ring-2 focus:ring-gray-900 dark:focus:ring-gray-100 focus:border-transparent transition-all"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              {t('auth.newPassword')}
            </label>
            <input
              type="password"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg dark:bg-gray-700 dark:text-white focus:outline-none focus:ring-2 focus:ring-gray-900 dark:focus:ring-gray-100 focus:border-transparent transition-all"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              {t('auth.confirmPassword')}
            </label>
            <input
              type="password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg dark:bg-gray-700 dark:text-white focus:outline-none focus:ring-2 focus:ring-gray-900 dark:focus:ring-gray-100 focus:border-transparent transition-all"
            />
          </div>
          <div className="flex justify-end space-x-2 mt-4">
            <button
              onClick={() => setShowPasswordModal(false)}
              className="px-4 py-2 border border-gray-300 dark:border-gray-600 rounded-lg text-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-gray-700 transition-colors"
            >
              {t('common.cancel')}
            </button>
            <button
              onClick={handleUpdatePassword}
              disabled={loading}
              className="px-4 py-2 bg-gray-900 dark:bg-gray-100 text-white dark:text-gray-900 rounded-lg hover:bg-gray-800 dark:hover:bg-gray-200 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              {loading ? t('common.updating') : t('common.update')}
            </button>
          </div>
        </div>
      </Modal>

      {/* 创建ApiKey模态框 */}
      <Modal
        isOpen={showCreateApiKeyModal}
        onClose={() => {
          setShowCreateApiKeyModal(false)
          setApiKeyName('')
          setApiKeyDescription('')
          setCreatedApiKey('')
        }}
        title={t('settings.createApiKey')}
      >
        {!createdApiKey ? (
          <div className="space-y-4">
            <div>
              <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
                {t('settings.apiKeyName')}
              </label>
              <input
                type="text"
                value={apiKeyName}
                onChange={(e) => setApiKeyName(e.target.value)}
                className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg dark:bg-gray-700 dark:text-white focus:outline-none focus:ring-2 focus:ring-gray-900 dark:focus:ring-gray-100 focus:border-transparent transition-all"
              />
            </div>
            <div>
              <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
                {t('settings.apiKeyDescription')}
              </label>
              <textarea
                value={apiKeyDescription}
                onChange={(e) => setApiKeyDescription(e.target.value)}
                rows={3}
                className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg dark:bg-gray-700 dark:text-white focus:outline-none focus:ring-2 focus:ring-gray-900 dark:focus:ring-gray-100 focus:border-transparent transition-all"
              />
            </div>
            <div className="flex justify-end space-x-2 mt-4">
              <button
                onClick={() => setShowCreateApiKeyModal(false)}
                className="px-4 py-2 border border-gray-300 dark:border-gray-600 rounded-lg text-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-gray-700 transition-colors"
              >
                {t('common.cancel')}
              </button>
              <button
                onClick={handleCreateApiKey}
                disabled={loading}
                className="px-4 py-2 bg-gray-900 dark:bg-gray-100 text-white dark:text-gray-900 rounded-lg hover:bg-gray-800 dark:hover:bg-gray-200 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                {loading ? t('common.creating') : t('common.create')}
              </button>
            </div>
          </div>
        ) : (
          <div className="space-y-4">
            <div className="bg-gray-50 dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded-lg p-4">
              <p className="text-sm text-gray-700 dark:text-gray-300 mb-3 font-medium">
                {t('settings.apiKeyCreatedWarning')}
              </p>
              <div className="flex items-center space-x-2">
                <code className="flex-1 px-3 py-2 bg-white dark:bg-gray-900 border border-gray-300 dark:border-gray-600 rounded-lg text-sm break-all font-mono select-all">
                  {createdApiKey}
                </code>
                <button
                  onClick={() => {
                    copyToClipboard(createdApiKey, 'new-key')
                    // 复制后延迟关闭弹框
                    setTimeout(() => {
                      setShowCreateApiKeyModal(false)
                      setApiKeyName('')
                      setApiKeyDescription('')
                      setCreatedApiKey('')
                      setJustCopied('')
                    }, 1500)
                  }}
                  className={`px-3 py-2 rounded-lg transition-all whitespace-nowrap ${
                    justCopied === 'new-key'
                      ? 'bg-green-600 dark:bg-green-500 text-white'
                      : 'bg-gray-900 dark:bg-gray-100 text-white dark:text-gray-900 hover:bg-gray-800 dark:hover:bg-gray-200'
                  }`}
                >
                  {justCopied === 'new-key' ? '✓ ' + t('success.copied') : t('common.copy')}
                </button>
              </div>
              {justCopied === 'new-key' && (
                <p className="text-xs text-green-600 dark:text-green-400 mt-2 animate-pulse">
                  {t('common.closingInMoment')}
                </p>
              )}
            </div>
          </div>
        )}
      </Modal>

      {/* 删除确认对话框 */}
      {deleteConfirm.show && (
        <ConfirmDialog
          onCancel={() => setDeleteConfirm({ show: false, type: 'user', id: '', name: '' })}
          onConfirm={deleteConfirm.type === 'user' ? handleDeleteUser : handleDeleteApiKey}
          title={t('common.confirmDelete')}
          message={t('common.confirmDeleteMessage', { name: deleteConfirm.name })}
        />
      )}

      {/* Toast通知 */}
      {toast.show && (
        <Toast
          message={t(toast.message)}
          type={toast.type}
          onClose={() => setToast({ ...toast, show: false })}
        />
      )}
    </div>
  )
}
