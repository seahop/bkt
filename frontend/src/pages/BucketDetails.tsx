import { useEffect, useState, useRef } from 'react'
import { useParams, Link } from 'react-router-dom'
import { FolderOpen, Upload, Download, Trash2, File, ArrowLeft, RefreshCw, Folder, FolderPlus, Home } from 'lucide-react'
import { bucketApi } from '../services/api'
import type { Object as StorageObject } from '../types'

interface FolderItem {
  name: string
  prefix: string
  isFolder: true
}

interface FileItem extends StorageObject {
  isFolder: false
}

type BrowserItem = FolderItem | FileItem

export default function BucketDetails() {
  const { bucketName } = useParams<{ bucketName: string }>()
  const [objects, setObjects] = useState<StorageObject[]>([])
  const [currentPrefix, setCurrentPrefix] = useState('')
  const [loading, setLoading] = useState(true)
  const [uploading, setUploading] = useState(false)
  const [error, setError] = useState('')
  const [showCreateFolderModal, setShowCreateFolderModal] = useState(false)
  const [newFolderName, setNewFolderName] = useState('')
  const fileInputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (bucketName) {
      loadObjects()
    }
  }, [bucketName, currentPrefix])

  const loadObjects = async () => {
    if (!bucketName) return

    try {
      setError('')
      const data = await bucketApi.listObjects(bucketName)
      // Handle both array response and object response with objects property
      const objectList = Array.isArray(data) ? data : (data as any).objects || []
      setObjects(objectList)
    } catch (error: any) {
      console.error('Failed to load objects:', error)
      setError(error.response?.data?.message || 'Failed to load objects')
    } finally {
      setLoading(false)
    }
  }

  // Parse objects into folders and files for current prefix
  const getBrowserItems = (): BrowserItem[] => {
    const items: BrowserItem[] = []
    const folders = new Set<string>()

    objects.forEach(obj => {
      // Only show objects that start with current prefix
      if (!obj.key.startsWith(currentPrefix)) {
        return
      }

      // Get the part after the current prefix
      const relativePath = obj.key.substring(currentPrefix.length)

      // Check if this is a subfolder or a file in current directory
      const slashIndex = relativePath.indexOf('/')

      if (slashIndex > 0) {
        // This is in a subfolder
        const folderName = relativePath.substring(0, slashIndex)
        folders.add(folderName)
      } else if (relativePath.length > 0) {
        // This is a file in current directory
        items.push({ ...obj, isFolder: false })
      }
    })

    // Add folders at the beginning
    const folderItems: FolderItem[] = Array.from(folders).map(name => ({
      name,
      prefix: currentPrefix + name + '/',
      isFolder: true,
    }))

    return [...folderItems, ...items]
  }

  const navigateToFolder = (prefix: string) => {
    setCurrentPrefix(prefix)
  }

  const navigateUp = () => {
    if (currentPrefix === '') return
    const parts = currentPrefix.slice(0, -1).split('/')
    parts.pop()
    setCurrentPrefix(parts.length > 0 ? parts.join('/') + '/' : '')
  }

  const getBreadcrumbs = () => {
    if (currentPrefix === '') return []
    const parts = currentPrefix.slice(0, -1).split('/')
    return parts.map((part, index) => ({
      name: part,
      prefix: parts.slice(0, index + 1).join('/') + '/',
    }))
  }

  const handleUploadClick = () => {
    fileInputRef.current?.click()
  }

  const handleFileSelect = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const files = event.target.files
    if (!files || files.length === 0 || !bucketName) return

    setUploading(true)
    setError('')

    try {
      // Upload each selected file with current prefix
      for (const file of Array.from(files)) {
        const objectKey = currentPrefix + file.name
        await bucketApi.uploadObject(bucketName, objectKey, file)
      }

      // Reload objects list
      await loadObjects()

      // Reset file input
      if (fileInputRef.current) {
        fileInputRef.current.value = ''
      }
    } catch (error: any) {
      console.error('Failed to upload file:', error)
      setError(error.response?.data?.message || 'Failed to upload file')
    } finally {
      setUploading(false)
    }
  }

  const handleCreateFolder = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!bucketName || !newFolderName.trim()) return

    setError('')

    try {
      // Create a zero-byte object with trailing slash to represent the folder
      const folderKey = currentPrefix + newFolderName.trim() + '/.keep'
      const emptyBlob = new Blob([''], { type: 'text/plain' })
      const emptyFile = new File([emptyBlob], '.keep', { type: 'text/plain' })

      await bucketApi.uploadObject(bucketName, folderKey, emptyFile)

      setShowCreateFolderModal(false)
      setNewFolderName('')
      await loadObjects()
    } catch (error: any) {
      console.error('Failed to create folder:', error)
      setError(error.response?.data?.message || 'Failed to create folder')
    }
  }

  const handleDownload = async (object: StorageObject) => {
    if (!bucketName) return

    try {
      const blob = await bucketApi.downloadObject(bucketName, object.key)

      // Create download link
      const url = window.URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = url
      link.download = object.key
      document.body.appendChild(link)
      link.click()
      document.body.removeChild(link)
      window.URL.revokeObjectURL(url)
    } catch (error: any) {
      console.error('Failed to download object:', error)
      setError(error.response?.data?.message || 'Failed to download object')
    }
  }

  const handleDelete = async (object: StorageObject) => {
    if (!bucketName) return
    if (!confirm(`Are you sure you want to delete "${object.key}"?`)) return

    try {
      setError('')
      await bucketApi.deleteObject(bucketName, object.key)
      await loadObjects()
    } catch (error: any) {
      console.error('Failed to delete object:', error)
      setError(error.response?.data?.message || 'Failed to delete object')
    }
  }

  const formatFileSize = (bytes: number): string => {
    if (bytes === 0) return '0 Bytes'
    const k = 1024
    const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return Math.round((bytes / Math.pow(k, i)) * 100) / 100 + ' ' + sizes[i]
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-dark-textSecondary">Loading objects...</div>
      </div>
    )
  }

  const browserItems = getBrowserItems()
  const breadcrumbs = getBreadcrumbs()

  return (
    <div className="p-8">
      {/* Header */}
      <div className="mb-8">
        <Link to="/buckets" className="inline-flex items-center gap-2 text-blue-500 hover:text-blue-400 mb-4">
          <ArrowLeft className="w-4 h-4" />
          Back to Buckets
        </Link>
        <div className="flex items-center justify-between mb-4">
          <div className="flex items-center gap-3">
            <FolderOpen className="w-8 h-8 text-blue-500" />
            <div>
              <h1 className="text-3xl font-bold text-dark-text">{bucketName}</h1>
              <p className="text-dark-textSecondary">{browserItems.length} items</p>
            </div>
          </div>
          <div className="flex gap-3">
            <button
              onClick={loadObjects}
              className="flex items-center gap-2 bg-dark-surface border border-dark-border hover:bg-dark-surfaceHover text-dark-text px-4 py-2 rounded-lg transition-colors"
            >
              <RefreshCw className="w-5 h-5" />
              Refresh
            </button>
            <button
              onClick={() => setShowCreateFolderModal(true)}
              className="flex items-center gap-2 bg-dark-surface border border-dark-border hover:bg-dark-surfaceHover text-dark-text px-4 py-2 rounded-lg transition-colors"
            >
              <FolderPlus className="w-5 h-5" />
              Create Folder
            </button>
            <button
              onClick={handleUploadClick}
              disabled={uploading}
              className="flex items-center gap-2 bg-blue-600 hover:bg-blue-700 text-white px-4 py-2 rounded-lg transition-colors disabled:opacity-50"
            >
              <Upload className="w-5 h-5" />
              {uploading ? 'Uploading...' : 'Upload Files'}
            </button>
            <input
              ref={fileInputRef}
              type="file"
              multiple
              onChange={handleFileSelect}
              className="hidden"
            />
          </div>
        </div>

        {/* Breadcrumbs */}
        <div className="flex items-center gap-2 text-sm">
          <button
            onClick={() => setCurrentPrefix('')}
            className="flex items-center gap-1 text-blue-500 hover:text-blue-400"
          >
            <Home className="w-4 h-4" />
            <span>{bucketName}</span>
          </button>
          {breadcrumbs.map((crumb, index) => (
            <div key={index} className="flex items-center gap-2">
              <span className="text-dark-textSecondary">/</span>
              <button
                onClick={() => navigateToFolder(crumb.prefix)}
                className="text-blue-500 hover:text-blue-400"
              >
                {crumb.name}
              </button>
            </div>
          ))}
        </div>
      </div>

      {/* Error Message */}
      {error && (
        <div className="bg-red-500/10 border border-red-500 text-red-500 px-4 py-3 rounded-lg mb-6">
          {error}
        </div>
      )}

      {/* Objects List */}
      {browserItems.length === 0 ? (
        <div className="bg-dark-surface border border-dark-border rounded-lg p-12 text-center">
          <File className="w-16 h-16 text-dark-textSecondary mx-auto mb-4 opacity-50" />
          <h2 className="text-xl font-semibold text-dark-text mb-2">No items yet</h2>
          <p className="text-dark-textSecondary mb-6">Upload files or create folders to get started</p>
          <div className="flex gap-3 justify-center">
            <button
              onClick={() => setShowCreateFolderModal(true)}
              className="bg-dark-surface border border-dark-border hover:bg-dark-surfaceHover text-dark-text px-6 py-3 rounded-lg transition-colors"
            >
              Create Folder
            </button>
            <button
              onClick={handleUploadClick}
              className="bg-blue-600 hover:bg-blue-700 text-white px-6 py-3 rounded-lg transition-colors"
            >
              Upload Files
            </button>
          </div>
        </div>
      ) : (
        <div className="bg-dark-surface border border-dark-border rounded-lg overflow-hidden">
          <table className="w-full">
            <thead className="bg-dark-bg border-b border-dark-border">
              <tr>
                <th className="text-left px-6 py-4 text-sm font-semibold text-dark-text">Name</th>
                <th className="text-left px-6 py-4 text-sm font-semibold text-dark-text">Size</th>
                <th className="text-left px-6 py-4 text-sm font-semibold text-dark-text">Type</th>
                <th className="text-left px-6 py-4 text-sm font-semibold text-dark-text">Last Modified</th>
                <th className="text-right px-6 py-4 text-sm font-semibold text-dark-text">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-dark-border">
              {browserItems.map((item, index) => (
                item.isFolder ? (
                  <tr key={`folder-${index}`} className="hover:bg-dark-surfaceHover transition-colors cursor-pointer">
                    <td className="px-6 py-4" onClick={() => navigateToFolder(item.prefix)}>
                      <div className="flex items-center gap-3">
                        <Folder className="w-5 h-5 text-yellow-500" />
                        <span className="text-dark-text font-medium">{item.name}/</span>
                      </div>
                    </td>
                    <td className="px-6 py-4 text-dark-textSecondary">—</td>
                    <td className="px-6 py-4 text-dark-textSecondary">Folder</td>
                    <td className="px-6 py-4 text-dark-textSecondary">—</td>
                    <td className="px-6 py-4"></td>
                  </tr>
                ) : (
                  <tr key={item.id} className="hover:bg-dark-surfaceHover transition-colors">
                    <td className="px-6 py-4">
                      <div className="flex items-center gap-3">
                        <File className="w-5 h-5 text-blue-500" />
                        <span className="text-dark-text">{item.key.substring(currentPrefix.length)}</span>
                      </div>
                    </td>
                    <td className="px-6 py-4 text-dark-textSecondary">{formatFileSize(item.size)}</td>
                    <td className="px-6 py-4 text-dark-textSecondary">{item.content_type}</td>
                    <td className="px-6 py-4 text-dark-textSecondary">
                      {new Date(item.updated_at).toLocaleString()}
                    </td>
                    <td className="px-6 py-4">
                      <div className="flex items-center justify-end gap-2">
                        <button
                          onClick={() => handleDownload(item)}
                          className="p-2 hover:bg-blue-600 hover:text-white text-blue-500 rounded transition-colors"
                          title="Download"
                        >
                          <Download className="w-4 h-4" />
                        </button>
                        <button
                          onClick={() => handleDelete(item)}
                          className="p-2 hover:bg-red-600 hover:text-white text-red-500 rounded transition-colors"
                          title="Delete"
                        >
                          <Trash2 className="w-4 h-4" />
                        </button>
                      </div>
                    </td>
                  </tr>
                )
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Create Folder Modal */}
      {showCreateFolderModal && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
          <div className="bg-dark-surface border border-dark-border rounded-lg p-6 w-full max-w-md">
            <h2 className="text-2xl font-bold text-dark-text mb-6">Create Folder</h2>
            <form onSubmit={handleCreateFolder} className="space-y-4">
              <div>
                <label className="block text-sm font-medium text-dark-text mb-2">Folder Name</label>
                <input
                  type="text"
                  value={newFolderName}
                  onChange={(e) => setNewFolderName(e.target.value)}
                  className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
                  placeholder="my-folder"
                  required
                  pattern="[a-zA-Z0-9-_]+"
                  title="Only letters, numbers, hyphens, and underscores"
                />
                <p className="text-xs text-dark-textSecondary mt-1">
                  Only letters, numbers, hyphens, and underscores
                </p>
              </div>

              <div className="flex gap-3 pt-4">
                <button
                  type="button"
                  onClick={() => {
                    setShowCreateFolderModal(false)
                    setNewFolderName('')
                  }}
                  className="flex-1 px-4 py-2 border border-dark-border text-dark-text rounded-lg hover:bg-dark-surfaceHover transition-colors"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="flex-1 bg-blue-600 hover:bg-blue-700 text-white px-4 py-2 rounded-lg transition-colors"
                >
                  Create
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  )
}
