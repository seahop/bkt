import { useEffect, useState } from 'react'
import { Key, Plus, Trash2, Copy, Eye, EyeOff, Clock, X } from 'lucide-react'
import { useAuthStore } from '../store/authStore'
import { accessKeyApi, stsApi } from '../services/api'
import type { AccessKey, AccessKeyResponse } from '../types'
import { getErrorMessage } from '../utils/errors'

export default function Profile() {
  const { user } = useAuthStore()
  const [accessKeys, setAccessKeys] = useState<AccessKey[]>([])
  const [loading, setLoading] = useState(true)
  const [newKey, setNewKey] = useState<AccessKeyResponse | null>(null)
  const [loadError, setLoadError] = useState('')
  const [showSecretKey, setShowSecretKey] = useState(false)

  // Temporary (STS) credentials modal state
  const [showStsModal, setShowStsModal] = useState(false)
  const [stsDuration, setStsDuration] = useState(3600)
  const [stsReadOnly, setStsReadOnly] = useState(false)
  const [stsLoading, setStsLoading] = useState(false)
  const [stsError, setStsError] = useState('')
  const [stsResult, setStsResult] = useState<{
    access_key: string
    secret_key: string
    expires_at: string
    read_only: boolean
  } | null>(null)
  const [showStsSecret, setShowStsSecret] = useState(false)

  useEffect(() => {
    loadAccessKeys()
  }, [])

  const loadAccessKeys = async () => {
    try {
      setLoadError('')
      const data = await accessKeyApi.listAccessKeys()
      setAccessKeys(data)
    } catch (error) {
      console.error('Failed to load access keys:', error)
      setLoadError(getErrorMessage(error, 'Failed to load access keys'))
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
      alert(getErrorMessage(error, 'Failed to generate access key'))
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
      alert(getErrorMessage(error, 'Failed to revoke access key'))
    }
  }

  const copyToClipboard = (text: string) => {
    navigator.clipboard.writeText(text)
  }

  const openStsModal = () => {
    setStsDuration(3600)
    setStsReadOnly(false)
    setStsError('')
    setStsResult(null)
    setShowStsSecret(false)
    setShowStsModal(true)
  }

  const closeStsModal = () => {
    setShowStsModal(false)
    setStsError('')
    setStsResult(null)
    setShowStsSecret(false)
  }

  const handleGenerateStsCredentials = async () => {
    setStsLoading(true)
    setStsError('')

    try {
      const result = await stsApi.issueTemporaryCredentials(stsDuration, stsReadOnly)
      setStsResult(result)
    } catch (error: any) {
      console.error('Failed to issue temporary credentials:', error)
      setStsError(getErrorMessage(error, 'Failed to issue temporary credentials'))
    } finally {
      setStsLoading(false)
    }
  }

  return (
    <div className="page">
      {loadError && <div className="alert-error mb-6">{loadError}</div>}

      <div className="flex items-start justify-between gap-4 mb-8">
        <div>
          <h1 className="page-title">Profile</h1>
          <p className="page-subtitle">Manage your account and API credentials</p>
        </div>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-8">
        <div className="card p-6">
          <h2 className="text-base font-semibold text-dark-text mb-4">Account</h2>
          <div className="flex items-center gap-4">
            <div className="w-12 h-12 rounded-full bg-gradient-to-br from-blue-500 to-blue-700 flex items-center justify-center text-white text-lg font-semibold shrink-0">
              {user?.username.charAt(0).toUpperCase()}
            </div>
            <div className="flex-1 min-w-0">
              <p className="text-sm font-medium text-dark-text truncate">
                {user?.username}
                {user?.is_admin ? (
                  <span className="ml-2 align-middle badge-blue">Administrator</span>
                ) : (
                  <span className="ml-2 align-middle badge-gray">User</span>
                )}
              </p>
              <p className="text-xs text-dark-textMuted truncate mt-0.5">{user?.email}</p>
            </div>
          </div>
        </div>

        <div className="card p-5 flex items-center">
          <div className="flex items-center gap-4">
            <span className="flex items-center justify-center w-11 h-11 rounded-lg bg-green-500/10 text-green-500 shrink-0">
              <Key className="w-5 h-5" />
            </span>
            <div>
              <p className="text-2xl font-semibold tabular-nums text-dark-text leading-tight">
                {accessKeys.length}
              </p>
              <p className="text-xs uppercase tracking-wider text-dark-textSecondary mt-0.5">
                Active Access Keys
              </p>
            </div>
          </div>
        </div>
      </div>

      <div className="card p-6">
        <div className="flex items-start justify-between gap-4 mb-6">
          <div>
            <h2 className="text-base font-semibold text-dark-text">Access Keys</h2>
            <p className="text-sm text-dark-textSecondary mt-0.5">
              Generate and manage API credentials
            </p>
          </div>
          <div className="flex items-center gap-2">
            <button onClick={openStsModal} className="btn-secondary">
              <Clock className="w-4 h-4" />
              Temporary credentials
            </button>
            <button onClick={handleGenerateKey} className="btn-primary">
              <Plus className="w-4 h-4" />
              Generate New Key
            </button>
          </div>
        </div>

        {newKey && (
          <div className="mb-6 space-y-3">
            <div className="alert-success">
              <Key className="w-4 h-4 mt-0.5 shrink-0" />
              <span>New access key generated. Save these credentials now.</span>
            </div>
            <div>
              <label className="label">Access Key</label>
              <div className="flex items-center gap-2 bg-dark-inset border border-dark-border rounded-lg p-3">
                <span className="flex-1 font-mono text-sm text-dark-text truncate">
                  {newKey.access_key}
                </span>
                <button
                  onClick={() => copyToClipboard(newKey.access_key)}
                  title="Copy access key"
                  className="btn-icon shrink-0"
                >
                  <Copy className="w-4 h-4" />
                </button>
              </div>
            </div>
            <div>
              <label className="label">Secret Key</label>
              <div className="flex items-center gap-2 bg-dark-inset border border-dark-border rounded-lg p-3">
                <span className="flex-1 font-mono text-sm text-dark-text truncate">
                  {showSecretKey ? newKey.secret_key : '••••••••••••••••••••••••••••'}
                </span>
                <button
                  onClick={() => setShowSecretKey(!showSecretKey)}
                  title={showSecretKey ? 'Hide secret key' : 'Show secret key'}
                  className="btn-icon shrink-0"
                >
                  {showSecretKey ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                </button>
                <button
                  onClick={() => copyToClipboard(newKey.secret_key)}
                  title="Copy secret key"
                  className="btn-icon shrink-0"
                >
                  <Copy className="w-4 h-4" />
                </button>
              </div>
            </div>
            <div className="alert-warning">
              This secret key is shown only once. You won't be able to see it again.
            </div>
            <button onClick={() => setNewKey(null)} className="btn-secondary btn-sm">
              I've saved these credentials
            </button>
          </div>
        )}

        {loading ? (
          <div className="flex flex-col items-center justify-center py-10 gap-3">
            <div className="spinner" />
            <p className="text-sm text-dark-textSecondary">Loading access keys…</p>
          </div>
        ) : accessKeys.length === 0 ? (
          <div className="empty-state !py-10">
            <Key className="empty-state-icon" />
            <h3 className="text-base font-semibold text-dark-text mb-1">No access keys yet</h3>
            <p className="text-sm text-dark-textSecondary max-w-sm">
              Generate a key to access the S3-compatible API.
            </p>
          </div>
        ) : (
          <div className="space-y-2">
            {accessKeys.map((key) => (
              <div
                key={key.id}
                className="flex items-center justify-between gap-4 px-4 py-3 bg-dark-inset border border-dark-border rounded-lg hover:border-dark-borderStrong transition-colors"
              >
                <div className="flex-1 min-w-0">
                  <p className="font-mono text-sm text-dark-text truncate">{key.access_key}</p>
                  <p className="text-xs tabular-nums text-dark-textMuted mt-1">
                    Created {new Date(key.created_at).toLocaleDateString()}
                    {key.last_used_at &&
                      ` • Last used ${new Date(key.last_used_at).toLocaleDateString()}`}
                  </p>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  {key.is_active ? (
                    <span className="badge-green">Active</span>
                  ) : (
                    <span className="badge-red">Inactive</span>
                  )}
                  <button
                    onClick={() => handleRevokeKey(key.id)}
                    title="Revoke access key"
                    className="btn-icon hover:!text-red-400 hover:!bg-red-500/10"
                  >
                    <Trash2 className="w-4 h-4" />
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Temporary Credentials Modal */}
      {showStsModal && (
        <div className="modal-overlay">
          <div className="modal-panel">
            <div className="flex items-center justify-between mb-5">
              <h2 className="modal-title">Temporary credentials</h2>
              <button onClick={closeStsModal} className="btn-icon" title="Close">
                <X className="w-4 h-4" />
              </button>
            </div>

            {stsError && <div className="alert-error mb-4">{stsError}</div>}

            {!stsResult ? (
              <>
                <div className="space-y-4">
                  <div>
                    <label className="label">Duration</label>
                    <select
                      value={stsDuration}
                      onChange={(e) => setStsDuration(parseInt(e.target.value, 10))}
                      className="input"
                    >
                      <option value={900}>15 minutes</option>
                      <option value={3600}>1 hour</option>
                      <option value={14400}>4 hours</option>
                      <option value={43200}>12 hours</option>
                    </select>
                    <p className="help-text">
                      Short-lived S3 credentials that expire automatically.
                    </p>
                  </div>
                  <label className="flex items-center gap-2 text-sm text-dark-text">
                    <input
                      type="checkbox"
                      checked={stsReadOnly}
                      onChange={(e) => setStsReadOnly(e.target.checked)}
                      className="w-4 h-4 text-blue-600 bg-dark-inset border-dark-border rounded focus:ring-blue-500"
                    />
                    Read-only
                  </label>
                </div>
                <div className="flex justify-end gap-2 mt-6">
                  <button onClick={closeStsModal} className="btn-ghost">
                    Cancel
                  </button>
                  <button
                    onClick={handleGenerateStsCredentials}
                    disabled={stsLoading}
                    className="btn-primary"
                  >
                    {stsLoading && <span className="spinner !w-4 !h-4" />}
                    {stsLoading ? 'Generating...' : 'Generate'}
                  </button>
                </div>
              </>
            ) : (
              <div className="space-y-3">
                <div>
                  <label className="label">Access Key</label>
                  <div className="flex items-center gap-2 bg-dark-inset border border-dark-border rounded-lg p-3">
                    <span className="flex-1 font-mono text-sm text-dark-text truncate">
                      {stsResult.access_key}
                    </span>
                    <button
                      onClick={() => copyToClipboard(stsResult.access_key)}
                      title="Copy access key"
                      className="btn-icon shrink-0"
                    >
                      <Copy className="w-4 h-4" />
                    </button>
                  </div>
                </div>
                <div>
                  <label className="label">Secret Key</label>
                  <div className="flex items-center gap-2 bg-dark-inset border border-dark-border rounded-lg p-3">
                    <span className="flex-1 font-mono text-sm text-dark-text truncate">
                      {showStsSecret ? stsResult.secret_key : '••••••••••••••••••••••••••••'}
                    </span>
                    <button
                      onClick={() => setShowStsSecret(!showStsSecret)}
                      title={showStsSecret ? 'Hide secret key' : 'Show secret key'}
                      className="btn-icon shrink-0"
                    >
                      {showStsSecret ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                    </button>
                    <button
                      onClick={() => copyToClipboard(stsResult.secret_key)}
                      title="Copy secret key"
                      className="btn-icon shrink-0"
                    >
                      <Copy className="w-4 h-4" />
                    </button>
                  </div>
                </div>
                {stsResult.read_only && (
                  <span className="badge-yellow">Read-only</span>
                )}
                <div className="alert-warning">
                  Expires {new Date(stsResult.expires_at).toLocaleString()} — shown only once.
                </div>
                <div className="flex justify-end mt-3">
                  <button onClick={closeStsModal} className="btn-secondary">
                    Done
                  </button>
                </div>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
