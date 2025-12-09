import { useEffect, useState } from 'react'
import { Key, Plus, Trash2, Copy, Eye, EyeOff } from 'lucide-react'
import { useAuthStore } from '../store/authStore'
import { accessKeyApi } from '../services/api'
import type { AccessKey, AccessKeyResponse } from '../types'

export default function Profile() {
  const { user } = useAuthStore()
  const [accessKeys, setAccessKeys] = useState<AccessKey[]>([])
  const [loading, setLoading] = useState(true)
  const [newKey, setNewKey] = useState<AccessKeyResponse | null>(null)
  const [showSecretKey, setShowSecretKey] = useState(false)

  useEffect(() => {
    loadAccessKeys()
  }, [])

  const loadAccessKeys = async () => {
    try {
      const data = await accessKeyApi.listAccessKeys()
      setAccessKeys(data)
    } catch (error) {
      console.error('Failed to load access keys:', error)
    } finally {
      setLoading(false)
    }
  }

  const handleGenerateKey = async () => {
    try {
      const key = await accessKeyApi.createAccessKey()
      setNewKey(key)
      loadAccessKeys()
    } catch (error: any) {
      alert(error.response?.data?.message || 'Failed to generate access key')
    }
  }

  const handleRevokeKey = async (id: string) => {
    if (!confirm('Are you sure you want to revoke this access key?')) {
      return
    }

    try {
      await accessKeyApi.revokeAccessKey(id)
      loadAccessKeys()
    } catch (error: any) {
      alert(error.response?.data?.message || 'Failed to revoke access key')
    }
  }

  const copyToClipboard = (text: string) => {
    navigator.clipboard.writeText(text)
  }

  return (
    <div className="p-8">
      <div className="mb-8">
        <h1 className="text-3xl font-bold text-dark-text mb-2">Profile</h1>
        <p className="text-dark-textSecondary">Manage your account and API credentials</p>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-8">
        <div className="bg-dark-surface border border-dark-border rounded-lg p-6">
          <h2 className="text-xl font-semibold text-dark-text mb-4">User Information</h2>
          <div className="space-y-4">
            <div>
              <label className="text-sm text-dark-textSecondary">Username</label>
              <p className="text-dark-text font-medium">{user?.username}</p>
            </div>
            <div>
              <label className="text-sm text-dark-textSecondary">Email</label>
              <p className="text-dark-text font-medium">{user?.email}</p>
            </div>
            <div>
              <label className="text-sm text-dark-textSecondary">Role</label>
              <p className="text-dark-text font-medium">
                {user?.is_admin ? 'Administrator' : 'User'}
              </p>
            </div>
          </div>
        </div>

        <div className="bg-dark-surface border border-dark-border rounded-lg p-6">
          <h2 className="text-xl font-semibold text-dark-text mb-4">Quick Stats</h2>
          <div className="space-y-4">
            <div>
              <label className="text-sm text-dark-textSecondary">Active Access Keys</label>
              <p className="text-2xl font-bold text-dark-text">{accessKeys.length}</p>
            </div>
          </div>
        </div>
      </div>

      <div className="bg-dark-surface border border-dark-border rounded-lg p-6">
        <div className="flex items-center justify-between mb-6">
          <div>
            <h2 className="text-xl font-semibold text-dark-text mb-1">Access Keys</h2>
            <p className="text-sm text-dark-textSecondary">Generate and manage API credentials</p>
          </div>
          <button
            onClick={handleGenerateKey}
            className="flex items-center gap-2 bg-blue-600 hover:bg-blue-700 text-white px-4 py-2 rounded-lg transition-colors"
          >
            <Plus className="w-5 h-5" />
            Generate New Key
          </button>
        </div>

        {newKey && (
          <div className="bg-green-500/10 border border-green-500 rounded-lg p-6 mb-6">
            <h3 className="text-green-500 font-semibold mb-3 flex items-center gap-2">
              <Key className="w-5 h-5" />
              New Access Key Generated
            </h3>
            <p className="text-dark-textSecondary text-sm mb-4">
              Save these credentials now. You won't be able to see the secret key again.
            </p>
            <div className="space-y-3">
              <div>
                <label className="text-sm text-dark-textSecondary block mb-1">Access Key</label>
                <div className="flex gap-2">
                  <input
                    type="text"
                    value={newKey.access_key}
                    readOnly
                    className="flex-1 px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text font-mono text-sm"
                  />
                  <button
                    onClick={() => copyToClipboard(newKey.access_key)}
                    className="bg-dark-bg hover:bg-dark-surfaceHover border border-dark-border text-dark-text px-4 py-2 rounded-lg transition-colors"
                  >
                    <Copy className="w-4 h-4" />
                  </button>
                </div>
              </div>
              <div>
                <label className="text-sm text-dark-textSecondary block mb-1">Secret Key</label>
                <div className="flex gap-2">
                  <input
                    type={showSecretKey ? 'text' : 'password'}
                    value={newKey.secret_key}
                    readOnly
                    className="flex-1 px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text font-mono text-sm"
                  />
                  <button
                    onClick={() => setShowSecretKey(!showSecretKey)}
                    className="bg-dark-bg hover:bg-dark-surfaceHover border border-dark-border text-dark-text px-4 py-2 rounded-lg transition-colors"
                  >
                    {showSecretKey ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                  </button>
                  <button
                    onClick={() => copyToClipboard(newKey.secret_key)}
                    className="bg-dark-bg hover:bg-dark-surfaceHover border border-dark-border text-dark-text px-4 py-2 rounded-lg transition-colors"
                  >
                    <Copy className="w-4 h-4" />
                  </button>
                </div>
              </div>
            </div>
            <button
              onClick={() => setNewKey(null)}
              className="mt-4 text-sm text-green-500 hover:text-green-400"
            >
              I've saved these credentials
            </button>
          </div>
        )}

        {loading ? (
          <div className="text-center py-8 text-dark-textSecondary">Loading...</div>
        ) : accessKeys.length === 0 ? (
          <div className="text-center py-8">
            <Key className="w-12 h-12 text-dark-textSecondary mx-auto mb-3 opacity-50" />
            <p className="text-dark-textSecondary">No access keys yet</p>
          </div>
        ) : (
          <div className="space-y-3">
            {accessKeys.map((key) => (
              <div
                key={key.id}
                className="flex items-center justify-between p-4 bg-dark-bg border border-dark-border rounded-lg"
              >
                <div className="flex-1">
                  <p className="text-dark-text font-mono text-sm">{key.access_key}</p>
                  <p className="text-xs text-dark-textSecondary mt-1">
                    Created {new Date(key.created_at).toLocaleDateString()}
                    {key.last_used_at &&
                      ` • Last used ${new Date(key.last_used_at).toLocaleDateString()}`}
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  {key.is_active ? (
                    <span className="text-xs bg-green-500/10 text-green-500 px-2 py-1 rounded">
                      Active
                    </span>
                  ) : (
                    <span className="text-xs bg-red-500/10 text-red-500 px-2 py-1 rounded">
                      Inactive
                    </span>
                  )}
                  <button
                    onClick={() => handleRevokeKey(key.id)}
                    className="bg-red-600 hover:bg-red-700 text-white px-3 py-2 rounded-lg transition-colors"
                  >
                    <Trash2 className="w-4 h-4" />
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
