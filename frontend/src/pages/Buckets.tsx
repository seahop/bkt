import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { FolderOpen, Plus, Trash2, Calendar, Globe } from 'lucide-react'
import { bucketApi, s3ConfigApi } from '../services/api'
import { useAuthStore } from '../store/authStore'
import type { Bucket, S3Configuration } from '../types'
import { getErrorMessage } from '../utils/errors'

export default function Buckets() {
  const { user } = useAuthStore()
  const [buckets, setBuckets] = useState<Bucket[]>([])
  const [loading, setLoading] = useState(true)
  const [showCreateModal, setShowCreateModal] = useState(false)
  const [newBucketName, setNewBucketName] = useState('')
  const [isPublic, setIsPublic] = useState(false)
  const [storageBackend, setStorageBackend] = useState<'local' | 's3'>('local')
  const [selectedS3ConfigId, setSelectedS3ConfigId] = useState('')
  const [s3Configs, setS3Configs] = useState<S3Configuration[]>([])
  const [loadingS3Configs, setLoadingS3Configs] = useState(false)
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')
  const [loadError, setLoadError] = useState('')

  useEffect(() => {
    loadBuckets()
  }, [])

  useEffect(() => {
    if (storageBackend === 's3' && showCreateModal) {
      loadS3Configs()
    }
  }, [storageBackend, showCreateModal])

  const loadBuckets = async () => {
    try {
      setLoadError('')
      const data = await bucketApi.listBuckets()
      setBuckets(data)
    } catch (error) {
      console.error('Failed to load buckets:', error)
      setLoadError(getErrorMessage(error, 'Failed to load buckets'))
    } finally {
      setLoading(false)
    }
  }

  const loadS3Configs = async () => {
    try {
      setLoadingS3Configs(true)
      const data = await s3ConfigApi.listS3Configs()
      setS3Configs(data)
      // Auto-select default config if available
      const defaultConfig = data.find(c => c.is_default)
      if (defaultConfig) {
        setSelectedS3ConfigId(defaultConfig.id)
      }
    } catch (error) {
      console.error('Failed to load S3 configurations:', error)
    } finally {
      setLoadingS3Configs(false)
    }
  }

  const handleCreateBucket = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setCreating(true)

    try {
      await bucketApi.createBucket(
        newBucketName,
        isPublic,
        'us-east-1',
        storageBackend,
        selectedS3ConfigId || undefined
      )
      setShowCreateModal(false)
      setNewBucketName('')
      setIsPublic(false)
      setStorageBackend('local')
      setSelectedS3ConfigId('')
      loadBuckets()
    } catch (err: any) {
      setError(getErrorMessage(err, 'Failed to create bucket'))
    } finally {
      setCreating(false)
    }
  }

  const handleDeleteBucket = async (bucketName: string) => {
    if (!confirm(`Are you sure you want to delete bucket "${bucketName}"?`)) {
      return
    }

    try {
      await bucketApi.deleteBucket(bucketName)
      loadBuckets()
    } catch (error: any) {
      alert(getErrorMessage(error, 'Failed to delete bucket'))
    }
  }

  if (loading) {
    return (
      <div className="flex flex-col items-center justify-center h-64 gap-3">
        <div className="spinner" />
        <p className="text-sm text-dark-textSecondary">Loading buckets…</p>
      </div>
    )
  }

  return (
    <div className="page">
      <div className="flex items-start justify-between gap-4 mb-8">
        <div>
          <h1 className="page-title">Buckets</h1>
          <p className="page-subtitle">
            {user?.is_admin ? 'Manage your storage buckets' : 'Your accessible buckets'}
          </p>
        </div>
        {user?.is_admin && (
          <button onClick={() => setShowCreateModal(true)} className="btn-primary">
            <Plus className="w-4 h-4" />
            Create Bucket
          </button>
        )}
      </div>

      {loadError && <div className="alert-error mb-6">{loadError}</div>}

      {buckets.length === 0 ? (
        <div className="card empty-state">
          <FolderOpen className="empty-state-icon" />
          <h3 className="text-base font-semibold text-dark-text mb-1">
            {user?.is_admin ? 'No buckets yet' : 'No accessible buckets'}
          </h3>
          <p className="text-sm text-dark-textSecondary mb-5 max-w-sm">
            {user?.is_admin
              ? 'Create your first bucket to start storing objects'
              : 'Contact your administrator to grant you access to buckets'}
          </p>
          {user?.is_admin && (
            <button onClick={() => setShowCreateModal(true)} className="btn-primary">
              <Plus className="w-4 h-4" />
              Create Bucket
            </button>
          )}
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
          {buckets.map((bucket) => (
            <div
              key={bucket.id}
              className="card p-5 hover:border-dark-borderStrong transition-colors flex flex-col"
            >
              <Link to={`/buckets/${bucket.name}`} className="block">
                <div className="flex items-start gap-3">
                  <span className="flex items-center justify-center w-10 h-10 rounded-lg bg-blue-500/10 shrink-0">
                    <FolderOpen className="w-5 h-5 text-blue-500" />
                  </span>
                  <div className="flex-1 min-w-0">
                    <h3 className="text-base font-semibold text-dark-text truncate">
                      {bucket.name}
                    </h3>
                    <div className="flex items-center gap-3 mt-1 text-xs text-dark-textMuted">
                      <span className="inline-flex items-center gap-1">
                        <Globe className="w-3 h-3" />
                        {bucket.region}
                      </span>
                      <span className="inline-flex items-center gap-1">
                        <Calendar className="w-3 h-3" />
                        {new Date(bucket.created_at).toLocaleDateString()}
                      </span>
                    </div>
                  </div>
                </div>
              </Link>

              <div className="flex flex-wrap gap-1.5 mt-4">
                {bucket.is_public ? (
                  <span className="badge-green">Public</span>
                ) : (
                  <span className="badge-gray">Private</span>
                )}
                {bucket.storage_backend === 's3' && <span className="badge-purple">S3</span>}
                {bucket.storage_backend === 'local' && <span className="badge-blue">Local</span>}
              </div>

              <div className="flex items-center gap-2 mt-4 pt-4 border-t border-dark-border">
                <Link
                  to={`/buckets/${bucket.name}`}
                  className="btn-secondary btn-sm flex-1 !justify-center"
                >
                  View Objects
                </Link>
                {user?.is_admin && (
                  <button
                    onClick={() => handleDeleteBucket(bucket.name)}
                    title="Delete bucket"
                    className="btn-icon hover:!text-red-400 hover:!bg-red-500/10"
                  >
                    <Trash2 className="w-4 h-4" />
                  </button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Create Bucket Modal */}
      {showCreateModal && (
        <div className="modal-overlay">
          <div className="modal-panel">
            <div className="flex items-center justify-between mb-5">
              <h2 className="modal-title">Create Bucket</h2>
            </div>
            <form onSubmit={handleCreateBucket} className="space-y-4">
              {error && <div className="alert-error">{error}</div>}

              <div>
                <label className="label">Bucket Name</label>
                <input
                  type="text"
                  value={newBucketName}
                  onChange={(e) => setNewBucketName(e.target.value)}
                  className="input"
                  placeholder="my-bucket-name"
                  required
                  minLength={3}
                  maxLength={63}
                  pattern="[a-z0-9-]+"
                  title="Only lowercase letters, numbers, and hyphens"
                />
                <p className="help-text">
                  Only lowercase letters, numbers, and hyphens (3-63 characters)
                </p>
              </div>

              <div>
                <label className="label">Storage Backend</label>
                <select
                  value={storageBackend}
                  onChange={(e) => setStorageBackend(e.target.value as 'local' | 's3')}
                  className="input"
                >
                  <option value="local">Local Storage</option>
                  <option value="s3">S3 Storage</option>
                </select>
                <p className="help-text">Choose where to store this bucket's data</p>
              </div>

              {storageBackend === 's3' && (
                <div>
                  <label className="label">S3 Configuration</label>
                  {loadingS3Configs ? (
                    <div className="flex items-center gap-2 text-sm text-dark-textSecondary">
                      <span className="spinner !w-4 !h-4" />
                      Loading configurations...
                    </div>
                  ) : s3Configs.length === 0 ? (
                    <div className="alert-warning">
                      No S3 configurations available. Please create one first in S3 Configs.
                    </div>
                  ) : (
                    <>
                      <select
                        value={selectedS3ConfigId}
                        onChange={(e) => setSelectedS3ConfigId(e.target.value)}
                        className="input"
                      >
                        <option value="">Use default configuration from .env</option>
                        {s3Configs.map((config) => (
                          <option key={config.id} value={config.id}>
                            {config.name} ({config.endpoint})
                            {config.is_default && ' - Default'}
                          </option>
                        ))}
                      </select>
                      <p className="help-text">
                        Select an S3 configuration or use the default from .env
                      </p>
                    </>
                  )}
                </div>
              )}

              <label
                htmlFor="isPublic"
                className="flex items-center gap-2.5 cursor-pointer select-none"
              >
                <input
                  type="checkbox"
                  id="isPublic"
                  checked={isPublic}
                  onChange={(e) => setIsPublic(e.target.checked)}
                  className="w-4 h-4 accent-blue-600"
                />
                <span className="text-sm text-dark-text">Make bucket public</span>
              </label>

              <div className="flex justify-end gap-2 mt-6 pt-2">
                <button
                  type="button"
                  onClick={() => {
                    setShowCreateModal(false)
                    setError('')
                    setNewBucketName('')
                    setIsPublic(false)
                    setStorageBackend('local')
                    setSelectedS3ConfigId('')
                  }}
                  className="btn-ghost"
                >
                  Cancel
                </button>
                <button type="submit" disabled={creating} className="btn-primary">
                  {creating && <span className="spinner !w-4 !h-4 !border-white/30 !border-t-white" />}
                  {creating ? 'Creating...' : 'Create'}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  )
}
