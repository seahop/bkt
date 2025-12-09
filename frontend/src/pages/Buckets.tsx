import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { FolderOpen, Plus, Trash2, Calendar } from 'lucide-react'
import { bucketApi, s3ConfigApi } from '../services/api'
import { useAuthStore } from '../store/authStore'
import type { Bucket, S3Configuration } from '../types'

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
      const data = await bucketApi.listBuckets()
      setBuckets(data)
    } catch (error) {
      console.error('Failed to load buckets:', error)
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
      setError(err.response?.data?.message || 'Failed to create bucket')
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
      alert(error.response?.data?.message || 'Failed to delete bucket')
    }
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-dark-textSecondary">Loading buckets...</div>
      </div>
    )
  }

  return (
    <div className="p-8">
      <div className="flex items-center justify-between mb-8">
        <div>
          <h1 className="text-3xl font-bold text-dark-text mb-2">Buckets</h1>
          <p className="text-dark-textSecondary">
            {user?.is_admin ? 'Manage your storage buckets' : 'Your accessible buckets'}
          </p>
        </div>
        {user?.is_admin && (
          <button
            onClick={() => setShowCreateModal(true)}
            className="flex items-center gap-2 bg-blue-600 hover:bg-blue-700 text-white px-4 py-2 rounded-lg transition-colors"
          >
            <Plus className="w-5 h-5" />
            Create Bucket
          </button>
        )}
      </div>

      {buckets.length === 0 ? (
        <div className="bg-dark-surface border border-dark-border rounded-lg p-12 text-center">
          <FolderOpen className="w-16 h-16 text-dark-textSecondary mx-auto mb-4 opacity-50" />
          <h2 className="text-xl font-semibold text-dark-text mb-2">
            {user?.is_admin ? 'No buckets yet' : 'No accessible buckets'}
          </h2>
          <p className="text-dark-textSecondary mb-6">
            {user?.is_admin
              ? 'Create your first bucket to start storing objects'
              : 'Contact your administrator to grant you access to buckets'}
          </p>
          {user?.is_admin && (
            <button
              onClick={() => setShowCreateModal(true)}
              className="bg-blue-600 hover:bg-blue-700 text-white px-6 py-3 rounded-lg transition-colors"
            >
              Create Bucket
            </button>
          )}
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
          {buckets.map((bucket) => (
            <div key={bucket.id} className="bg-dark-surface border border-dark-border rounded-lg p-6 hover:border-blue-500/50 transition-colors">
              <Link to={`/buckets/${bucket.name}`}>
                <div className="flex items-start justify-between mb-4">
                  <div className="flex items-center gap-3">
                    <FolderOpen className="w-8 h-8 text-blue-500" />
                    <div>
                      <h3 className="text-lg font-semibold text-dark-text">{bucket.name}</h3>
                      <p className="text-xs text-dark-textSecondary">{bucket.region}</p>
                    </div>
                  </div>
                  <div className="flex gap-2">
                    {bucket.storage_backend === 's3' && (
                      <span className="text-xs bg-purple-500/10 text-purple-500 px-2 py-1 rounded">
                        S3
                      </span>
                    )}
                    {bucket.storage_backend === 'local' && (
                      <span className="text-xs bg-blue-500/10 text-blue-500 px-2 py-1 rounded">
                        Local
                      </span>
                    )}
                    {bucket.is_public && (
                      <span className="text-xs bg-green-500/10 text-green-500 px-2 py-1 rounded">
                        Public
                      </span>
                    )}
                  </div>
                </div>
              </Link>

              <div className="flex items-center gap-2 text-sm text-dark-textSecondary mb-4">
                <Calendar className="w-4 h-4" />
                <span>Created {new Date(bucket.created_at).toLocaleDateString()}</span>
              </div>

              <div className="flex gap-2 pt-4 border-t border-dark-border">
                <Link
                  to={`/buckets/${bucket.name}`}
                  className={`${user?.is_admin ? 'flex-1' : 'w-full'} text-center bg-blue-600 hover:bg-blue-700 text-white px-4 py-2 rounded-lg transition-colors text-sm`}
                >
                  View Objects
                </Link>
                {user?.is_admin && (
                  <button
                    onClick={() => handleDeleteBucket(bucket.name)}
                    className="bg-red-600 hover:bg-red-700 text-white px-4 py-2 rounded-lg transition-colors"
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
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
          <div className="bg-dark-surface border border-dark-border rounded-lg p-6 w-full max-w-md">
            <h2 className="text-2xl font-bold text-dark-text mb-6">Create Bucket</h2>
            <form onSubmit={handleCreateBucket} className="space-y-4">
              {error && (
                <div className="bg-red-500/10 border border-red-500 text-red-500 px-4 py-3 rounded-lg text-sm">
                  {error}
                </div>
              )}

              <div>
                <label className="block text-sm font-medium text-dark-text mb-2">Bucket Name</label>
                <input
                  type="text"
                  value={newBucketName}
                  onChange={(e) => setNewBucketName(e.target.value)}
                  className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
                  placeholder="my-bucket-name"
                  required
                  minLength={3}
                  maxLength={63}
                  pattern="[a-z0-9-]+"
                  title="Only lowercase letters, numbers, and hyphens"
                />
                <p className="text-xs text-dark-textSecondary mt-1">
                  Only lowercase letters, numbers, and hyphens (3-63 characters)
                </p>
              </div>

              <div>
                <label className="block text-sm font-medium text-dark-text mb-2">Storage Backend</label>
                <select
                  value={storageBackend}
                  onChange={(e) => setStorageBackend(e.target.value as 'local' | 's3')}
                  className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
                >
                  <option value="local">Local Storage</option>
                  <option value="s3">S3 Storage</option>
                </select>
                <p className="text-xs text-dark-textSecondary mt-1">
                  Choose where to store this bucket's data
                </p>
              </div>

              {storageBackend === 's3' && (
                <div>
                  <label className="block text-sm font-medium text-dark-text mb-2">S3 Configuration</label>
                  {loadingS3Configs ? (
                    <div className="text-sm text-dark-textSecondary">Loading configurations...</div>
                  ) : s3Configs.length === 0 ? (
                    <div className="text-sm text-yellow-500">
                      No S3 configurations available. Please create one first in S3 Configs.
                    </div>
                  ) : (
                    <>
                      <select
                        value={selectedS3ConfigId}
                        onChange={(e) => setSelectedS3ConfigId(e.target.value)}
                        className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
                      >
                        <option value="">Use default configuration from .env</option>
                        {s3Configs.map((config) => (
                          <option key={config.id} value={config.id}>
                            {config.name} ({config.endpoint})
                            {config.is_default && ' - Default'}
                          </option>
                        ))}
                      </select>
                      <p className="text-xs text-dark-textSecondary mt-1">
                        Select an S3 configuration or use the default from .env
                      </p>
                    </>
                  )}
                </div>
              )}

              <div className="flex items-center gap-2">
                <input
                  type="checkbox"
                  id="isPublic"
                  checked={isPublic}
                  onChange={(e) => setIsPublic(e.target.checked)}
                  className="w-4 h-4"
                />
                <label htmlFor="isPublic" className="text-sm text-dark-text">
                  Make bucket public
                </label>
              </div>

              <div className="flex gap-3 pt-4">
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
                  className="flex-1 px-4 py-2 border border-dark-border text-dark-text rounded-lg hover:bg-dark-surfaceHover transition-colors"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  disabled={creating}
                  className="flex-1 bg-blue-600 hover:bg-blue-700 text-white px-4 py-2 rounded-lg transition-colors disabled:opacity-50"
                >
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
