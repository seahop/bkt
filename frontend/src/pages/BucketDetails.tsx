import { useEffect, useState, useRef } from 'react'
import { useParams, Link } from 'react-router-dom'
import { FolderOpen, Upload, Download, Trash2, File as FileIcon, ArrowLeft, RefreshCw, Folder, FolderPlus, Home, Loader2, Pencil, Columns2, Info, FolderInput, Copy, ExternalLink, Search, X, Calendar, Filter } from 'lucide-react'
import { bucketApi } from '../services/api'
import type { Object as StorageObject } from '../types'

interface ContextMenuState {
  show: boolean
  x: number
  y: number
  type: 'pane' | 'file' | 'folder'
  item?: BrowserItem
  pane: 'left' | 'right' | 'single'
}

interface FolderItem {
  name: string
  prefix: string
  isFolder: true
}

interface FileItem extends StorageObject {
  isFolder: false
}

type BrowserItem = FolderItem | FileItem

interface ActiveUpload {
  uploadId: string
  filename: string
  progress: number
  status: string
  error?: string
}

export default function BucketDetails() {
  const { bucketName } = useParams<{ bucketName: string }>()
  const [objects, setObjects] = useState<StorageObject[]>([])
  const [currentPrefix, setCurrentPrefix] = useState('')
  const [loading, setLoading] = useState(true)
  const [uploading, setUploading] = useState(false)
  const [error, setError] = useState('')
  const [showCreateFolderModal, setShowCreateFolderModal] = useState(false)
  const [newFolderName, setNewFolderName] = useState('')
  const [createFolderPane, setCreateFolderPane] = useState<'left' | 'right'>('left')
  const [createFolderFromContextMenu, setCreateFolderFromContextMenu] = useState(false)
  const [activeUploads, setActiveUploads] = useState<ActiveUpload[]>([])
  const [uploadTargetPane, setUploadTargetPane] = useState<'left' | 'right' | 'single'>('left')
  const fileInputRef = useRef<HTMLInputElement>(null)

  // Rename state
  const [showRenameModal, setShowRenameModal] = useState(false)
  const [renameTarget, setRenameTarget] = useState<StorageObject | null>(null)
  const [newFileName, setNewFileName] = useState('')

  // Drag and drop state
  const [draggedItem, setDraggedItem] = useState<BrowserItem | null>(null)
  const [dropTarget, setDropTarget] = useState<string | null>(null)

  // Split view state
  const [splitView, setSplitView] = useState(false)
  const [rightPrefix, setRightPrefix] = useState('')

  // Context menu state
  const [contextMenu, setContextMenu] = useState<ContextMenuState>({
    show: false,
    x: 0,
    y: 0,
    type: 'pane',
    pane: 'left'
  })

  // File info modal state
  const [showFileInfo, setShowFileInfo] = useState(false)
  const [fileInfoTarget, setFileInfoTarget] = useState<StorageObject | null>(null)

  // Search and filter state
  const [searchQuery, setSearchQuery] = useState('')
  const [showFilters, setShowFilters] = useState(false)
  const [filterDateFrom, setFilterDateFrom] = useState('')
  const [filterDateTo, setFilterDateTo] = useState('')
  const [filterExtension, setFilterExtension] = useState('')
  const [filterMinSize, setFilterMinSize] = useState('')
  const [filterMaxSize, setFilterMaxSize] = useState('')
  const [filterMaxDepth, setFilterMaxDepth] = useState('')

  // Close context menu on click outside
  useEffect(() => {
    const handleClick = () => setContextMenu(prev => ({ ...prev, show: false }))
    document.addEventListener('click', handleClick)
    return () => document.removeEventListener('click', handleClick)
  }, [])

  useEffect(() => {
    if (bucketName) {
      loadObjects()
      loadActiveUploads()
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

  const loadActiveUploads = async () => {
    try {
      // Load uploads that are pending or processing
      const uploads = await bucketApi.listUploads('processing')
      const pendingUploads = await bucketApi.listUploads('pending')

      const allActiveUploads = [...uploads, ...pendingUploads]

      // Convert to ActiveUpload format and start polling
      const activeUploadsList: ActiveUpload[] = allActiveUploads.map(upload => ({
        uploadId: upload.id,
        filename: upload.filename,
        progress: upload.progress_percent,
        status: upload.status,
        error: upload.error_message
      }))

      setActiveUploads(activeUploadsList)

      // Start polling for each active upload
      allActiveUploads.forEach(upload => {
        if (upload.status === 'pending' || upload.status === 'processing') {
          pollUploadStatus(upload.id, upload.filename)
        }
      })
    } catch (error) {
      console.error('Failed to load active uploads:', error)
    }
  }

  // Poll upload status
  const pollUploadStatus = async (uploadId: string, filename: string) => {
    const maxAttempts = 600 // 10 minutes with 1 second intervals
    let attempts = 0

    const poll = async () => {
      try {
        const status = await bucketApi.getUploadStatus(uploadId)

        setActiveUploads(prev =>
          prev.map(u =>
            u.uploadId === uploadId
              ? {
                  ...u,
                  progress: status.progress_percent,
                  status: status.status,
                  error: status.error_message
                }
              : u
          )
        )

        if (status.status === 'completed') {
          // Remove from active uploads after a brief delay
          setTimeout(() => {
            setActiveUploads(prev => prev.filter(u => u.uploadId !== uploadId))
          }, 2000)
          await loadObjects()
        } else if (status.status === 'failed') {
          // Keep failed upload visible for user to see error
          setTimeout(() => {
            setActiveUploads(prev => prev.filter(u => u.uploadId !== uploadId))
          }, 10000)
        } else if (attempts < maxAttempts) {
          attempts++
          setTimeout(poll, 1000) // Poll every second
        }
      } catch (error) {
        console.error('Failed to poll upload status:', error)
        setActiveUploads(prev => prev.filter(u => u.uploadId !== uploadId))
      }
    }

    poll()
  }

  // Parse objects into folders and files for a given prefix
  const getBrowserItemsForPrefix = (prefix: string): BrowserItem[] => {
    const items: BrowserItem[] = []
    const folders = new Set<string>()

    objects.forEach(obj => {
      // Only show objects that start with the given prefix
      if (!obj.key.startsWith(prefix)) {
        return
      }

      // Get the part after the prefix
      const relativePath = obj.key.substring(prefix.length)

      // Check if this is a subfolder or a file in current directory
      const slashIndex = relativePath.indexOf('/')

      if (slashIndex > 0) {
        // This is in a subfolder
        const folderName = relativePath.substring(0, slashIndex)
        folders.add(folderName)
      } else if (relativePath.length > 0 && relativePath !== '.keep') {
        // This is a file in current directory (skip .keep files)
        items.push({ ...obj, isFolder: false })
      }
    })

    // Add folders at the beginning
    const folderItems: FolderItem[] = Array.from(folders).map(name => ({
      name,
      prefix: prefix + name + '/',
      isFolder: true,
    }))

    return [...folderItems, ...items]
  }

  const navigateToFolder = (prefix: string, pane: 'left' | 'right' = 'left') => {
    if (pane === 'right') {
      setRightPrefix(prefix)
    } else {
      setCurrentPrefix(prefix)
    }
  }

  const navigateUp = () => {
    if (currentPrefix === '') return
    const parts = currentPrefix.slice(0, -1).split('/')
    parts.pop()
    setCurrentPrefix(parts.length > 0 ? parts.join('/') + '/' : '')
  }

  const getBreadcrumbsForPrefix = (prefix: string) => {
    if (prefix === '') return []
    const parts = prefix.slice(0, -1).split('/')
    return parts.map((part, index) => ({
      name: part,
      prefix: parts.slice(0, index + 1).join('/') + '/',
    }))
  }

  // Convert wildcard pattern to regex
  // Supports: * (any characters), ? (single character)
  const wildcardToRegex = (pattern: string): RegExp => {
    const escaped = pattern
      .replace(/[.+^${}()|[\]\\]/g, '\\$&') // Escape regex special chars except * and ?
      .replace(/\*/g, '.*')  // * matches any characters
      .replace(/\?/g, '.')   // ? matches single character
    return new RegExp(`^${escaped}$`, 'i') // Case insensitive
  }

  // Check if search query has active filters
  const hasActiveFilters = searchQuery || filterDateFrom || filterDateTo || filterExtension || filterMinSize || filterMaxSize || filterMaxDepth

  // Calculate folder depth from key (number of slashes)
  const getFolderDepth = (key: string): number => {
    const parts = key.split('/').filter(p => p.length > 0)
    return parts.length - 1 // -1 because the last part is the filename
  }

  // Check if item matches search criteria
  const matchesSearchCriteria = (obj: StorageObject): boolean => {
    const name = obj.key.split('/').pop() || ''

    // Max depth filter
    if (filterMaxDepth) {
      const maxDepth = parseInt(filterMaxDepth, 10)
      if (!isNaN(maxDepth) && getFolderDepth(obj.key) > maxDepth) {
        return false
      }
    }

    // Search query filter (with wildcard support)
    if (searchQuery) {
      const pattern = wildcardToRegex(searchQuery)
      if (!pattern.test(name)) {
        // Also check if it's a partial match without wildcards
        if (!name.toLowerCase().includes(searchQuery.toLowerCase())) {
          return false
        }
      }
    }

    // Extension filter
    if (filterExtension) {
      const ext = name.split('.').pop()?.toLowerCase() || ''
      const filterExts = filterExtension.toLowerCase().split(',').map(e => e.trim().replace(/^\./, ''))
      if (!filterExts.some(fe => ext === fe || wildcardToRegex(fe).test(ext))) {
        return false
      }
    }

    // Date filters
    const fileDate = new Date(obj.updated_at)

    if (filterDateFrom) {
      const fromDate = new Date(filterDateFrom)
      if (fileDate < fromDate) return false
    }

    if (filterDateTo) {
      const toDate = new Date(filterDateTo)
      toDate.setHours(23, 59, 59, 999)
      if (fileDate > toDate) return false
    }

    // Size filters
    if (filterMinSize) {
      const minBytes = parseSize(filterMinSize)
      if (minBytes !== null && obj.size < minBytes) return false
    }

    if (filterMaxSize) {
      const maxBytes = parseSize(filterMaxSize)
      if (maxBytes !== null && obj.size > maxBytes) return false
    }

    return true
  }

  // Get all matching files across the entire bucket when searching
  const getGlobalSearchResults = (): FileItem[] => {
    if (!hasActiveFilters) return []

    return objects
      .filter(obj => !obj.key.endsWith('.keep')) // Skip .keep files
      .filter(matchesSearchCriteria)
      .map(obj => ({ ...obj, isFolder: false as const }))
  }

  // Filter browser items based on search query and filters (for current directory view)
  const filterBrowserItems = (items: BrowserItem[]): BrowserItem[] => {
    if (!hasActiveFilters) return items

    return items.filter(item => {
      // Get the name to search
      const name = item.isFolder ? item.name : item.key.split('/').pop() || ''

      // Search query filter (with wildcard support)
      if (searchQuery) {
        const pattern = wildcardToRegex(searchQuery)
        if (!pattern.test(name)) {
          // Also check if it's a partial match without wildcards
          if (!name.toLowerCase().includes(searchQuery.toLowerCase())) {
            return false
          }
        }
      }

      // Extension filter (only for files)
      if (filterExtension && !item.isFolder) {
        const ext = name.split('.').pop()?.toLowerCase() || ''
        const filterExts = filterExtension.toLowerCase().split(',').map(e => e.trim().replace(/^\./, ''))
        if (!filterExts.some(fe => ext === fe || wildcardToRegex(fe).test(ext))) {
          return false
        }
      }

      // Date filters (only for files)
      if (!item.isFolder) {
        const fileItem = item as FileItem
        const fileDate = new Date(fileItem.updated_at)

        if (filterDateFrom) {
          const fromDate = new Date(filterDateFrom)
          if (fileDate < fromDate) return false
        }

        if (filterDateTo) {
          const toDate = new Date(filterDateTo)
          toDate.setHours(23, 59, 59, 999) // End of day
          if (fileDate > toDate) return false
        }

        // Size filters
        if (filterMinSize) {
          const minBytes = parseSize(filterMinSize)
          if (minBytes !== null && fileItem.size < minBytes) return false
        }

        if (filterMaxSize) {
          const maxBytes = parseSize(filterMaxSize)
          if (maxBytes !== null && fileItem.size > maxBytes) return false
        }
      }

      return true
    })
  }

  // Get folder path for a file (for display in search results)
  const getFolderPath = (key: string): string => {
    const parts = key.split('/')
    parts.pop() // Remove filename
    return parts.length > 0 ? parts.join('/') + '/' : '/'
  }

  // Parse size string like "10KB", "5MB", "1GB" to bytes
  const parseSize = (sizeStr: string): number | null => {
    const match = sizeStr.trim().match(/^(\d+(?:\.\d+)?)\s*(bytes?|kb|mb|gb|tb)?$/i)
    if (!match) return null

    const value = parseFloat(match[1])
    const unit = (match[2] || 'bytes').toLowerCase()

    const multipliers: Record<string, number> = {
      'byte': 1, 'bytes': 1,
      'kb': 1024,
      'mb': 1024 * 1024,
      'gb': 1024 * 1024 * 1024,
      'tb': 1024 * 1024 * 1024 * 1024,
    }

    return value * (multipliers[unit] || 1)
  }

  // Clear all filters
  const clearFilters = () => {
    setSearchQuery('')
    setFilterDateFrom('')
    setFilterDateTo('')
    setFilterExtension('')
    setFilterMinSize('')
    setFilterMaxSize('')
    setFilterMaxDepth('')
  }

  const handleUploadClick = () => {
    fileInputRef.current?.click()
  }

  const handleFileSelect = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const files = event.target.files
    if (!files || files.length === 0 || !bucketName) return

    setUploading(true)
    setError('')

    // Determine the target prefix based on which pane initiated the upload
    const targetPrefix = uploadTargetPane === 'right' ? rightPrefix : currentPrefix

    try {
      // Upload each selected file
      for (const file of Array.from(files)) {
        const objectKey = targetPrefix + file.name
        const fileSizeMB = file.size / (1024 * 1024)

        // Use async upload for files larger than 10MB
        if (fileSizeMB > 10) {
          try {
            const response = await bucketApi.uploadObjectAsync(bucketName, objectKey, file)

            // Add to active uploads
            setActiveUploads(prev => [
              ...prev,
              {
                uploadId: response.upload_id,
                filename: file.name,
                progress: 0,
                status: 'pending',
              }
            ])

            // Start polling for status
            pollUploadStatus(response.upload_id, file.name)
          } catch (error: any) {
            console.error('Failed to start async upload:', error)
            setError(error.response?.data?.message || `Failed to upload ${file.name}`)
          }
        } else {
          // Use synchronous upload for smaller files
          await bucketApi.uploadObject(bucketName, objectKey, file)
        }
      }

      // Reload objects list for synchronous uploads
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
      // Use the appropriate prefix based on selected pane
      const targetPrefix = splitView && createFolderPane === 'right' ? rightPrefix : currentPrefix
      // Create a zero-byte object with trailing slash to represent the folder
      const folderKey = targetPrefix + newFolderName.trim() + '/.keep'
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

  const handleRenameClick = (object: StorageObject) => {
    setRenameTarget(object)
    // Extract just the filename from the key
    const filename = object.key.substring(currentPrefix.length)
    setNewFileName(filename)
    setShowRenameModal(true)
  }

  const handleRename = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!bucketName || !renameTarget || !newFileName.trim()) return

    try {
      setError('')
      await bucketApi.renameObject(bucketName, renameTarget.key, newFileName.trim())
      setShowRenameModal(false)
      setRenameTarget(null)
      setNewFileName('')
      await loadObjects()
    } catch (error: any) {
      console.error('Failed to rename object:', error)
      setError(error.response?.data?.message || 'Failed to rename object')
    }
  }

  // Drag and drop handlers
  const handleDragStart = (e: React.DragEvent, item: BrowserItem) => {
    setDraggedItem(item)
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('text/plain', item.isFolder ? item.prefix : item.key)
  }

  const handleDragOver = (e: React.DragEvent, targetPrefix: string) => {
    e.preventDefault()
    if (draggedItem) {
      // Don't allow dropping a folder into itself or its children
      if (draggedItem.isFolder && targetPrefix.startsWith(draggedItem.prefix)) {
        return
      }
      e.dataTransfer.dropEffect = 'move'
      setDropTarget(targetPrefix)
    }
  }

  const handleDragLeave = () => {
    setDropTarget(null)
  }

  const handleDrop = async (e: React.DragEvent, targetPrefix: string) => {
    e.preventDefault()
    setDropTarget(null)

    if (!draggedItem || !bucketName) return

    if (draggedItem.isFolder) {
      // Moving a folder - use the folder name
      const folderName = draggedItem.name
      const destinationPrefix = targetPrefix + folderName + '/'

      // Don't move if it's the same location or into itself
      if (draggedItem.prefix === destinationPrefix || destinationPrefix.startsWith(draggedItem.prefix)) {
        setDraggedItem(null)
        return
      }

      try {
        setError('')
        await bucketApi.moveFolder(bucketName, draggedItem.prefix, destinationPrefix)
        await loadObjects()
      } catch (error: any) {
        console.error('Failed to move folder:', error)
        setError(error.response?.data?.message || 'Failed to move folder')
      } finally {
        setDraggedItem(null)
      }
    } else {
      // Moving a file
      const filename = draggedItem.key.split('/').pop() || draggedItem.key
      const destinationKey = targetPrefix + filename

      // Don't move if it's the same location
      if (draggedItem.key === destinationKey) {
        setDraggedItem(null)
        return
      }

      try {
        setError('')
        await bucketApi.moveObject(bucketName, draggedItem.key, destinationKey)
        await loadObjects()
      } catch (error: any) {
        console.error('Failed to move object:', error)
        setError(error.response?.data?.message || 'Failed to move object')
      } finally {
        setDraggedItem(null)
      }
    }
  }

  const handleDragEnd = () => {
    setDraggedItem(null)
    setDropTarget(null)
  }

  // Context menu handlers
  const handleContextMenu = (
    e: React.MouseEvent,
    type: 'pane' | 'file' | 'folder',
    pane: 'left' | 'right' | 'single',
    item?: BrowserItem
  ) => {
    e.preventDefault()
    e.stopPropagation()
    setContextMenu({
      show: true,
      x: e.clientX,
      y: e.clientY,
      type,
      item,
      pane
    })
  }

  const handleCopyPath = (path: string) => {
    navigator.clipboard.writeText(path)
    setContextMenu(prev => ({ ...prev, show: false }))
  }

  const handleShowFileInfo = (item: FileItem) => {
    setFileInfoTarget(item)
    setShowFileInfo(true)
    setContextMenu(prev => ({ ...prev, show: false }))
  }

  const handleOpenInNewTab = (item: FileItem) => {
    if (!bucketName) return
    // Create a download URL and open in new tab
    bucketApi.downloadObject(bucketName, item.key).then(blob => {
      const url = window.URL.createObjectURL(blob)
      window.open(url, '_blank')
    })
    setContextMenu(prev => ({ ...prev, show: false }))
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

  // When searching, show global results from entire bucket; otherwise show current directory
  const browserItems = hasActiveFilters
    ? getGlobalSearchResults()
    : getBrowserItemsForPrefix(currentPrefix)
  const leftBreadcrumbs = getBreadcrumbsForPrefix(currentPrefix)
  const rightBrowserItems = hasActiveFilters
    ? getGlobalSearchResults()
    : getBrowserItemsForPrefix(rightPrefix)
  const rightBreadcrumbs = getBreadcrumbsForPrefix(rightPrefix)
  const isSearchMode = !!hasActiveFilters

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
              onClick={() => {
                setSplitView(!splitView)
                if (!splitView) {
                  setRightPrefix(currentPrefix) // Initialize right pane to same location
                }
              }}
              className={`flex items-center gap-2 px-4 py-2 rounded-lg transition-colors ${
                splitView
                  ? 'bg-blue-600 text-white'
                  : 'bg-dark-surface border border-dark-border hover:bg-dark-surfaceHover text-dark-text'
              }`}
              title={splitView ? 'Exit split view' : 'Enable split view'}
            >
              <Columns2 className="w-5 h-5" />
              Split View
            </button>
            <button
              onClick={loadObjects}
              className="flex items-center gap-2 bg-dark-surface border border-dark-border hover:bg-dark-surfaceHover text-dark-text px-4 py-2 rounded-lg transition-colors"
            >
              <RefreshCw className="w-5 h-5" />
              Refresh
            </button>
            <button
              onClick={() => {
                setCreateFolderFromContextMenu(false)
                setShowCreateFolderModal(true)
              }}
              className="flex items-center gap-2 bg-dark-surface border border-dark-border hover:bg-dark-surfaceHover text-dark-text px-4 py-2 rounded-lg transition-colors"
            >
              <FolderPlus className="w-5 h-5" />
              Create Folder
            </button>
            <button
              onClick={() => {
                setUploadTargetPane(splitView ? 'left' : 'single')
                handleUploadClick()
              }}
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

        {/* Search and Filter Bar */}
        <div className="bg-dark-surface border border-dark-border rounded-lg p-4">
          <div className="flex items-center gap-4">
            {/* Search Input */}
            <div className="flex-1 relative">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-dark-textSecondary" />
              <input
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder="Search files... (use * for wildcard, e.g., *.jpg, report*)"
                className="w-full pl-10 pr-10 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text placeholder-dark-textSecondary focus:outline-none focus:ring-2 focus:ring-blue-500"
              />
              {searchQuery && (
                <button
                  onClick={() => setSearchQuery('')}
                  className="absolute right-3 top-1/2 -translate-y-1/2 text-dark-textSecondary hover:text-dark-text"
                >
                  <X className="w-4 h-4" />
                </button>
              )}
            </div>

            {/* Filter Toggle Button */}
            <button
              onClick={() => setShowFilters(!showFilters)}
              className={`flex items-center gap-2 px-4 py-2 rounded-lg transition-colors ${
                showFilters || (filterDateFrom || filterDateTo || filterExtension || filterMinSize || filterMaxSize || filterMaxDepth)
                  ? 'bg-blue-600 text-white'
                  : 'bg-dark-bg border border-dark-border text-dark-text hover:bg-dark-surfaceHover'
              }`}
            >
              <Filter className="w-4 h-4" />
              Filters
              {(filterDateFrom || filterDateTo || filterExtension || filterMinSize || filterMaxSize || filterMaxDepth) && (
                <span className="bg-white/20 text-xs px-1.5 py-0.5 rounded">
                  {[filterDateFrom, filterDateTo, filterExtension, filterMinSize, filterMaxSize, filterMaxDepth].filter(Boolean).length}
                </span>
              )}
            </button>

            {/* Clear Filters */}
            {hasActiveFilters && (
              <button
                onClick={clearFilters}
                className="flex items-center gap-2 px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-textSecondary hover:text-dark-text hover:bg-dark-surfaceHover transition-colors"
              >
                <X className="w-4 h-4" />
                Clear
              </button>
            )}
          </div>

          {/* Expanded Filters */}
          {showFilters && (
            <div className="mt-4 pt-4 border-t border-dark-border space-y-4">
              {/* Row 1: Extension and Max Depth */}
              <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
                {/* Extension Filter */}
                <div>
                  <label className="block text-sm font-medium text-dark-textSecondary mb-1">File Extension</label>
                  <input
                    type="text"
                    value={filterExtension}
                    onChange={(e) => setFilterExtension(e.target.value)}
                    placeholder="e.g., jpg, png, pdf"
                    className="w-full px-3 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text placeholder-dark-textSecondary focus:outline-none focus:ring-2 focus:ring-blue-500 text-sm"
                  />
                </div>

                {/* Max Depth Filter */}
                <div>
                  <label className="block text-sm font-medium text-dark-textSecondary mb-1">Max Folder Depth</label>
                  <input
                    type="number"
                    min="0"
                    value={filterMaxDepth}
                    onChange={(e) => setFilterMaxDepth(e.target.value)}
                    placeholder="e.g., 2 (0 = root only)"
                    className="w-full px-3 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text placeholder-dark-textSecondary focus:outline-none focus:ring-2 focus:ring-blue-500 text-sm"
                  />
                </div>

                {/* Size Filter */}
                <div className="sm:col-span-2 lg:col-span-1">
                  <label className="block text-sm font-medium text-dark-textSecondary mb-1">File Size</label>
                  <div className="flex gap-2">
                    <input
                      type="text"
                      value={filterMinSize}
                      onChange={(e) => setFilterMinSize(e.target.value)}
                      placeholder="Min (e.g., 1MB)"
                      className="flex-1 px-3 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text placeholder-dark-textSecondary focus:outline-none focus:ring-2 focus:ring-blue-500 text-sm"
                    />
                    <input
                      type="text"
                      value={filterMaxSize}
                      onChange={(e) => setFilterMaxSize(e.target.value)}
                      placeholder="Max (e.g., 10MB)"
                      className="flex-1 px-3 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text placeholder-dark-textSecondary focus:outline-none focus:ring-2 focus:ring-blue-500 text-sm"
                    />
                  </div>
                </div>
              </div>

              {/* Row 2: Date Filters */}
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                {/* Date From Filter */}
                <div>
                  <label className="block text-sm font-medium text-dark-textSecondary mb-1">Modified After</label>
                  <div className="relative">
                    <Calendar className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-dark-textSecondary" />
                    <input
                      type="date"
                      value={filterDateFrom}
                      onChange={(e) => setFilterDateFrom(e.target.value)}
                      className="w-full pl-10 pr-3 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500 text-sm"
                    />
                  </div>
                </div>

                {/* Date To Filter */}
                <div>
                  <label className="block text-sm font-medium text-dark-textSecondary mb-1">Modified Before</label>
                  <div className="relative">
                    <Calendar className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-dark-textSecondary" />
                    <input
                      type="date"
                      value={filterDateTo}
                      onChange={(e) => setFilterDateTo(e.target.value)}
                      className="w-full pl-10 pr-3 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500 text-sm"
                    />
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* Search Tips */}
          {searchQuery && !filterDateFrom && !filterDateTo && !filterExtension && !filterMinSize && !filterMaxSize && (
            <p className="mt-2 text-xs text-dark-textSecondary">
              Tip: Use <code className="bg-dark-bg px-1 rounded">*</code> for any characters, <code className="bg-dark-bg px-1 rounded">?</code> for single character
            </p>
          )}

          {/* Active Filter Summary */}
          {hasActiveFilters && (
            <div className="mt-3 flex flex-wrap gap-2">
              {searchQuery && (
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-blue-500/20 text-blue-400 rounded text-sm">
                  Search: "{searchQuery}"
                  <button onClick={() => setSearchQuery('')} className="hover:text-blue-300"><X className="w-3 h-3" /></button>
                </span>
              )}
              {filterExtension && (
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-green-500/20 text-green-400 rounded text-sm">
                  Extension: {filterExtension}
                  <button onClick={() => setFilterExtension('')} className="hover:text-green-300"><X className="w-3 h-3" /></button>
                </span>
              )}
              {filterDateFrom && (
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-purple-500/20 text-purple-400 rounded text-sm">
                  After: {filterDateFrom}
                  <button onClick={() => setFilterDateFrom('')} className="hover:text-purple-300"><X className="w-3 h-3" /></button>
                </span>
              )}
              {filterDateTo && (
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-purple-500/20 text-purple-400 rounded text-sm">
                  Before: {filterDateTo}
                  <button onClick={() => setFilterDateTo('')} className="hover:text-purple-300"><X className="w-3 h-3" /></button>
                </span>
              )}
              {filterMinSize && (
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-orange-500/20 text-orange-400 rounded text-sm">
                  Min: {filterMinSize}
                  <button onClick={() => setFilterMinSize('')} className="hover:text-orange-300"><X className="w-3 h-3" /></button>
                </span>
              )}
              {filterMaxSize && (
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-orange-500/20 text-orange-400 rounded text-sm">
                  Max: {filterMaxSize}
                  <button onClick={() => setFilterMaxSize('')} className="hover:text-orange-300"><X className="w-3 h-3" /></button>
                </span>
              )}
              {filterMaxDepth && (
                <span className="inline-flex items-center gap-1 px-2 py-1 bg-cyan-500/20 text-cyan-400 rounded text-sm">
                  Depth: {filterMaxDepth}
                  <button onClick={() => setFilterMaxDepth('')} className="hover:text-cyan-300"><X className="w-3 h-3" /></button>
                </span>
              )}
            </div>
          )}

          {/* Search Results Info */}
          {isSearchMode && (
            <div className="mt-3 flex items-center gap-2 text-sm text-dark-textSecondary">
              <Search className="w-4 h-4" />
              <span>
                Showing <span className="text-dark-text font-medium">{browserItems.length}</span> result{browserItems.length !== 1 ? 's' : ''} from entire bucket
              </span>
            </div>
          )}
        </div>

      </div>

      {/* Active Uploads Progress */}
      {activeUploads.length > 0 && (
        <div className="mb-6 space-y-3">
          {activeUploads.map((upload) => (
            <div key={upload.uploadId} className="bg-dark-surface border border-dark-border rounded-lg p-4">
              <div className="flex items-center justify-between mb-2">
                <div className="flex items-center gap-2">
                  {upload.status === 'completed' ? (
                    <div className="text-green-500 text-sm font-medium">✓ Completed</div>
                  ) : upload.status === 'failed' ? (
                    <div className="text-red-500 text-sm font-medium">✗ Failed</div>
                  ) : (
                    <Loader2 className="w-4 h-4 text-blue-500 animate-spin" />
                  )}
                  <span className="text-dark-text font-medium">{upload.filename}</span>
                </div>
                <span className="text-dark-textSecondary text-sm">{Math.round(upload.progress)}%</span>
              </div>
              <div className="w-full bg-dark-bg rounded-full h-2">
                <div
                  className={`h-2 rounded-full transition-all duration-300 ${
                    upload.status === 'completed' ? 'bg-green-500' :
                    upload.status === 'failed' ? 'bg-red-500' :
                    'bg-blue-500'
                  }`}
                  style={{ width: `${upload.progress}%` }}
                />
              </div>
              {upload.error && (
                <div className="mt-2 text-red-500 text-sm">{upload.error}</div>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Error Message */}
      {error && (
        <div className="bg-red-500/10 border border-red-500 text-red-500 px-4 py-3 rounded-lg mb-6">
          {error}
        </div>
      )}

      {/* Pane Content */}
      {splitView ? (
        // Dual pane view
        <div className="flex gap-4">
          {/* Left Pane */}
          <div className="flex-1 min-w-0">
            {/* Left Breadcrumbs */}
            <div className="flex items-center gap-2 text-sm mb-4">
              <button
                onClick={() => navigateToFolder('', 'left')}
                onDragOver={(e) => handleDragOver(e, '')}
                onDragLeave={handleDragLeave}
                onDrop={(e) => handleDrop(e, '')}
                className={`flex items-center gap-1 px-2 py-1 rounded transition-colors ${
                  dropTarget === ''
                    ? 'bg-blue-500/20 ring-2 ring-blue-500 text-blue-400'
                    : 'text-blue-500 hover:text-blue-400'
                }`}
              >
                <Home className="w-4 h-4" />
                <span>{bucketName}</span>
              </button>
              {leftBreadcrumbs.map((crumb, index) => (
                <div key={index} className="flex items-center gap-2">
                  <span className="text-dark-textSecondary">/</span>
                  <button
                    onClick={() => navigateToFolder(crumb.prefix, 'left')}
                    onDragOver={(e) => handleDragOver(e, crumb.prefix)}
                    onDragLeave={handleDragLeave}
                    onDrop={(e) => handleDrop(e, crumb.prefix)}
                    className={`px-2 py-1 rounded transition-colors ${
                      dropTarget === crumb.prefix
                        ? 'bg-blue-500/20 ring-2 ring-blue-500 text-blue-400'
                        : 'text-blue-500 hover:text-blue-400'
                    }`}
                  >
                    {crumb.name}
                  </button>
                </div>
              ))}
            </div>
            {/* Left Table */}
            <div
              className={`bg-dark-surface border rounded-lg overflow-hidden transition-colors min-h-[300px] flex flex-col ${
                dropTarget === `pane:left:${currentPrefix}`
                  ? 'border-blue-500 bg-blue-500/10'
                  : 'border-dark-border'
              }`}
              onContextMenu={(e) => handleContextMenu(e, 'pane', 'left')}
              onDragOver={(e) => {
                e.preventDefault()
                if (draggedItem) {
                  e.dataTransfer.dropEffect = 'move'
                  setDropTarget(`pane:left:${currentPrefix}`)
                }
              }}
              onDragLeave={(e) => {
                // Only clear if leaving the container entirely
                if (!e.currentTarget.contains(e.relatedTarget as Node)) {
                  setDropTarget(null)
                }
              }}
              onDrop={(e) => {
                e.preventDefault()
                // Only handle if not dropping on a folder row
                if (dropTarget === `pane:left:${currentPrefix}`) {
                  handleDrop(e, currentPrefix)
                }
              }}
            >
              <table className="w-full">
                <thead className="bg-dark-bg border-b border-dark-border">
                  <tr>
                    <th className="text-left px-4 py-3 text-sm font-semibold text-dark-text">Name</th>
                    <th className="text-left px-4 py-3 text-sm font-semibold text-dark-text">Size</th>
                    <th className="text-right px-4 py-3 text-sm font-semibold text-dark-text">Actions</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-dark-border">
                  {browserItems.map((item, index) => (
                    item.isFolder ? (
                      <tr
                        key={`left-folder-${index}`}
                        className={`hover:bg-dark-surfaceHover transition-colors cursor-pointer ${
                          dropTarget === item.prefix ? 'bg-blue-500/20 ring-2 ring-blue-500 ring-inset' : ''
                        } ${draggedItem?.isFolder && (draggedItem as FolderItem).prefix === item.prefix ? 'opacity-50' : ''}`}
                        draggable
                        onContextMenu={(e) => handleContextMenu(e, 'folder', 'left', item)}
                        onDragStart={(e) => handleDragStart(e, item)}
                        onDragEnd={handleDragEnd}
                        onDragOver={(e) => { e.stopPropagation(); handleDragOver(e, item.prefix) }}
                        onDragLeave={handleDragLeave}
                        onDrop={(e) => { e.stopPropagation(); handleDrop(e, item.prefix) }}
                      >
                        <td className="px-4 py-3" onClick={() => navigateToFolder(item.prefix, 'left')}>
                          <div className="flex items-center gap-2">
                            <Folder className={`w-4 h-4 ${dropTarget === item.prefix ? 'text-blue-500' : 'text-yellow-500'}`} />
                            <span className="text-dark-text font-medium truncate">{item.name}/</span>
                          </div>
                        </td>
                        <td className="px-4 py-3 text-dark-textSecondary">—</td>
                        <td className="px-4 py-3"></td>
                      </tr>
                    ) : (
                      <tr
                        key={`left-${item.id}`}
                        className={`hover:bg-dark-surfaceHover transition-colors ${
                          draggedItem?.id === item.id ? 'opacity-50' : ''
                        }`}
                        draggable={!isSearchMode}
                        onContextMenu={(e) => handleContextMenu(e, 'file', 'left', item)}
                        onDragStart={(e) => !isSearchMode && handleDragStart(e, item)}
                        onDragEnd={handleDragEnd}
                      >
                        <td className="px-4 py-3">
                          <div className="flex items-center gap-2">
                            <FileIcon className="w-4 h-4 text-blue-500" />
                            <div className="min-w-0">
                              <span className="text-dark-text truncate block">{item.key.split('/').pop()}</span>
                              {isSearchMode && (
                                <button
                                  onClick={() => {
                                    clearFilters()
                                    navigateToFolder(getFolderPath(item.key) === '/' ? '' : getFolderPath(item.key), 'left')
                                  }}
                                  className="block text-xs text-blue-400 hover:text-blue-300 truncate text-left"
                                  title="Go to folder"
                                >
                                  {getFolderPath(item.key) === '/' ? '/' : getFolderPath(item.key)}
                                </button>
                              )}
                            </div>
                          </div>
                        </td>
                        <td className="px-4 py-3 text-dark-textSecondary text-sm">{formatFileSize(item.size)}</td>
                        <td className="px-4 py-3">
                          <div className="flex items-center justify-end gap-1">
                            <button onClick={() => handleRenameClick(item)} className="p-1.5 hover:bg-yellow-600 hover:text-white text-yellow-500 rounded" title="Rename"><Pencil className="w-3.5 h-3.5" /></button>
                            <button onClick={() => handleDownload(item)} className="p-1.5 hover:bg-blue-600 hover:text-white text-blue-500 rounded" title="Download"><Download className="w-3.5 h-3.5" /></button>
                            <button onClick={() => handleDelete(item)} className="p-1.5 hover:bg-red-600 hover:text-white text-red-500 rounded" title="Delete"><Trash2 className="w-3.5 h-3.5" /></button>
                          </div>
                        </td>
                      </tr>
                    )
                  ))}
                </tbody>
              </table>
            </div>
          </div>

          {/* Divider */}
          <div className="w-px bg-dark-border" />

          {/* Right Pane */}
          <div className="flex-1 min-w-0">
            {/* Right Breadcrumbs */}
            <div className="flex items-center gap-2 text-sm mb-4">
              <button
                onClick={() => navigateToFolder('', 'right')}
                onDragOver={(e) => handleDragOver(e, '')}
                onDragLeave={handleDragLeave}
                onDrop={(e) => handleDrop(e, '')}
                className={`flex items-center gap-1 px-2 py-1 rounded transition-colors ${
                  dropTarget === ''
                    ? 'bg-blue-500/20 ring-2 ring-blue-500 text-blue-400'
                    : 'text-blue-500 hover:text-blue-400'
                }`}
              >
                <Home className="w-4 h-4" />
                <span>{bucketName}</span>
              </button>
              {rightBreadcrumbs.map((crumb, index) => (
                <div key={index} className="flex items-center gap-2">
                  <span className="text-dark-textSecondary">/</span>
                  <button
                    onClick={() => navigateToFolder(crumb.prefix, 'right')}
                    onDragOver={(e) => handleDragOver(e, crumb.prefix)}
                    onDragLeave={handleDragLeave}
                    onDrop={(e) => handleDrop(e, crumb.prefix)}
                    className={`px-2 py-1 rounded transition-colors ${
                      dropTarget === crumb.prefix
                        ? 'bg-blue-500/20 ring-2 ring-blue-500 text-blue-400'
                        : 'text-blue-500 hover:text-blue-400'
                    }`}
                  >
                    {crumb.name}
                  </button>
                </div>
              ))}
            </div>
            {/* Right Table */}
            <div
              className={`bg-dark-surface border rounded-lg overflow-hidden transition-colors min-h-[300px] flex flex-col ${
                dropTarget === `pane:right:${rightPrefix}`
                  ? 'border-blue-500 bg-blue-500/10'
                  : 'border-dark-border'
              }`}
              onContextMenu={(e) => handleContextMenu(e, 'pane', 'right')}
              onDragOver={(e) => {
                e.preventDefault()
                if (draggedItem) {
                  e.dataTransfer.dropEffect = 'move'
                  setDropTarget(`pane:right:${rightPrefix}`)
                }
              }}
              onDragLeave={(e) => {
                // Only clear if leaving the container entirely
                if (!e.currentTarget.contains(e.relatedTarget as Node)) {
                  setDropTarget(null)
                }
              }}
              onDrop={(e) => {
                e.preventDefault()
                // Only handle if not dropping on a folder row
                if (dropTarget === `pane:right:${rightPrefix}`) {
                  handleDrop(e, rightPrefix)
                }
              }}
            >
              <table className="w-full">
                <thead className="bg-dark-bg border-b border-dark-border">
                  <tr>
                    <th className="text-left px-4 py-3 text-sm font-semibold text-dark-text">Name</th>
                    <th className="text-left px-4 py-3 text-sm font-semibold text-dark-text">Size</th>
                    <th className="text-right px-4 py-3 text-sm font-semibold text-dark-text">Actions</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-dark-border">
                  {rightBrowserItems.map((item, index) => (
                    item.isFolder ? (
                      <tr
                        key={`right-folder-${index}`}
                        className={`hover:bg-dark-surfaceHover transition-colors cursor-pointer ${
                          dropTarget === item.prefix ? 'bg-blue-500/20 ring-2 ring-blue-500 ring-inset' : ''
                        } ${draggedItem?.isFolder && (draggedItem as FolderItem).prefix === item.prefix ? 'opacity-50' : ''}`}
                        draggable
                        onContextMenu={(e) => handleContextMenu(e, 'folder', 'right', item)}
                        onDragStart={(e) => handleDragStart(e, item)}
                        onDragEnd={handleDragEnd}
                        onDragOver={(e) => { e.stopPropagation(); handleDragOver(e, item.prefix) }}
                        onDragLeave={handleDragLeave}
                        onDrop={(e) => { e.stopPropagation(); handleDrop(e, item.prefix) }}
                      >
                        <td className="px-4 py-3" onClick={() => navigateToFolder(item.prefix, 'right')}>
                          <div className="flex items-center gap-2">
                            <Folder className={`w-4 h-4 ${dropTarget === item.prefix ? 'text-blue-500' : 'text-yellow-500'}`} />
                            <span className="text-dark-text font-medium truncate">{item.name}/</span>
                          </div>
                        </td>
                        <td className="px-4 py-3 text-dark-textSecondary">—</td>
                        <td className="px-4 py-3"></td>
                      </tr>
                    ) : (
                      <tr
                        key={`right-${item.id}`}
                        className={`hover:bg-dark-surfaceHover transition-colors ${
                          draggedItem?.id === item.id ? 'opacity-50' : ''
                        }`}
                        draggable={!isSearchMode}
                        onContextMenu={(e) => handleContextMenu(e, 'file', 'right', item)}
                        onDragStart={(e) => !isSearchMode && handleDragStart(e, item)}
                        onDragEnd={handleDragEnd}
                      >
                        <td className="px-4 py-3">
                          <div className="flex items-center gap-2">
                            <FileIcon className="w-4 h-4 text-blue-500" />
                            <div className="min-w-0">
                              <span className="text-dark-text truncate block">{item.key.split('/').pop()}</span>
                              {isSearchMode && (
                                <button
                                  onClick={() => {
                                    clearFilters()
                                    navigateToFolder(getFolderPath(item.key) === '/' ? '' : getFolderPath(item.key), 'right')
                                  }}
                                  className="block text-xs text-blue-400 hover:text-blue-300 truncate text-left"
                                  title="Go to folder"
                                >
                                  {getFolderPath(item.key) === '/' ? '/' : getFolderPath(item.key)}
                                </button>
                              )}
                            </div>
                          </div>
                        </td>
                        <td className="px-4 py-3 text-dark-textSecondary text-sm">{formatFileSize(item.size)}</td>
                        <td className="px-4 py-3">
                          <div className="flex items-center justify-end gap-1">
                            <button onClick={() => handleRenameClick(item)} className="p-1.5 hover:bg-yellow-600 hover:text-white text-yellow-500 rounded" title="Rename"><Pencil className="w-3.5 h-3.5" /></button>
                            <button onClick={() => handleDownload(item)} className="p-1.5 hover:bg-blue-600 hover:text-white text-blue-500 rounded" title="Download"><Download className="w-3.5 h-3.5" /></button>
                            <button onClick={() => handleDelete(item)} className="p-1.5 hover:bg-red-600 hover:text-white text-red-500 rounded" title="Delete"><Trash2 className="w-3.5 h-3.5" /></button>
                          </div>
                        </td>
                      </tr>
                    )
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      ) : (
        // Single pane view
        <>
          {/* Breadcrumbs - droppable for moving files up */}
          <div className="flex items-center gap-2 text-sm mb-4">
            <button
              onClick={() => setCurrentPrefix('')}
              onDragOver={(e) => handleDragOver(e, '')}
              onDragLeave={handleDragLeave}
              onDrop={(e) => handleDrop(e, '')}
              className={`flex items-center gap-1 px-2 py-1 rounded transition-colors ${
                dropTarget === ''
                  ? 'bg-blue-500/20 ring-2 ring-blue-500 text-blue-400'
                  : 'text-blue-500 hover:text-blue-400'
              }`}
            >
              <Home className="w-4 h-4" />
              <span>{bucketName}</span>
            </button>
            {leftBreadcrumbs.map((crumb, index) => (
              <div key={index} className="flex items-center gap-2">
                <span className="text-dark-textSecondary">/</span>
                <button
                  onClick={() => navigateToFolder(crumb.prefix)}
                  onDragOver={(e) => handleDragOver(e, crumb.prefix)}
                  onDragLeave={handleDragLeave}
                  onDrop={(e) => handleDrop(e, crumb.prefix)}
                  className={`px-2 py-1 rounded transition-colors ${
                    dropTarget === crumb.prefix
                      ? 'bg-blue-500/20 ring-2 ring-blue-500 text-blue-400'
                      : 'text-blue-500 hover:text-blue-400'
                  }`}
                >
                  {crumb.name}
                </button>
              </div>
            ))}
          </div>

          {/* Objects List */}
          {browserItems.length === 0 ? (
            <div className="bg-dark-surface border border-dark-border rounded-lg p-12 text-center">
              {isSearchMode ? (
                <>
                  <Search className="w-16 h-16 text-dark-textSecondary mx-auto mb-4 opacity-50" />
                  <h2 className="text-xl font-semibold text-dark-text mb-2">No results found</h2>
                  <p className="text-dark-textSecondary mb-6">Try adjusting your search or filters</p>
                  <button
                    onClick={clearFilters}
                    className="bg-blue-600 hover:bg-blue-700 text-white px-6 py-3 rounded-lg transition-colors"
                  >
                    Clear Search & Filters
                  </button>
                </>
              ) : (
                <>
                  <FileIcon className="w-16 h-16 text-dark-textSecondary mx-auto mb-4 opacity-50" />
                  <h2 className="text-xl font-semibold text-dark-text mb-2">No items yet</h2>
                  <p className="text-dark-textSecondary mb-6">Upload files or create folders to get started</p>
                  <div className="flex gap-3 justify-center">
                    <button
                      onClick={() => {
                        setCreateFolderFromContextMenu(false)
                        setShowCreateFolderModal(true)
                      }}
                      className="bg-dark-surface border border-dark-border hover:bg-dark-surfaceHover text-dark-text px-6 py-3 rounded-lg transition-colors"
                    >
                      Create Folder
                    </button>
                    <button
                      onClick={() => {
                        setUploadTargetPane('single')
                        handleUploadClick()
                      }}
                      className="bg-blue-600 hover:bg-blue-700 text-white px-6 py-3 rounded-lg transition-colors"
                    >
                      Upload Files
                    </button>
                  </div>
                </>
              )}
            </div>
          ) : (
            <div
              className={`bg-dark-surface border rounded-lg overflow-hidden transition-colors min-h-[400px] flex flex-col ${
                dropTarget === `pane:single:${currentPrefix}`
                  ? 'border-blue-500 bg-blue-500/10'
                  : 'border-dark-border'
              }`}
              onContextMenu={(e) => handleContextMenu(e, 'pane', 'single')}
              onDragOver={(e) => {
                e.preventDefault()
                if (draggedItem) {
                  e.dataTransfer.dropEffect = 'move'
                  setDropTarget(`pane:single:${currentPrefix}`)
                }
              }}
              onDragLeave={(e) => {
                if (!e.currentTarget.contains(e.relatedTarget as Node)) {
                  setDropTarget(null)
                }
              }}
              onDrop={(e) => {
                e.preventDefault()
                if (dropTarget === `pane:single:${currentPrefix}`) {
                  handleDrop(e, currentPrefix)
                }
              }}
            >
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
                      <tr
                        key={`folder-${index}`}
                        className={`hover:bg-dark-surfaceHover transition-colors cursor-pointer ${
                          dropTarget === item.prefix ? 'bg-blue-500/20 ring-2 ring-blue-500 ring-inset' : ''
                        } ${draggedItem?.isFolder && (draggedItem as FolderItem).prefix === item.prefix ? 'opacity-50' : ''}`}
                        draggable
                        onContextMenu={(e) => handleContextMenu(e, 'folder', 'single', item)}
                        onDragStart={(e) => handleDragStart(e, item)}
                        onDragEnd={handleDragEnd}
                        onDragOver={(e) => { e.stopPropagation(); handleDragOver(e, item.prefix) }}
                        onDragLeave={handleDragLeave}
                        onDrop={(e) => { e.stopPropagation(); handleDrop(e, item.prefix) }}
                      >
                        <td className="px-6 py-4" onClick={() => navigateToFolder(item.prefix)}>
                          <div className="flex items-center gap-3">
                            <Folder className={`w-5 h-5 ${dropTarget === item.prefix ? 'text-blue-500' : 'text-yellow-500'}`} />
                            <span className="text-dark-text font-medium">{item.name}/</span>
                          </div>
                        </td>
                        <td className="px-6 py-4 text-dark-textSecondary">—</td>
                        <td className="px-6 py-4 text-dark-textSecondary">Folder</td>
                        <td className="px-6 py-4 text-dark-textSecondary">—</td>
                        <td className="px-6 py-4"></td>
                      </tr>
                    ) : (
                      <tr
                        key={item.id}
                        className={`hover:bg-dark-surfaceHover transition-colors ${
                          draggedItem?.id === item.id ? 'opacity-50' : ''
                        }`}
                        draggable={!isSearchMode}
                        onContextMenu={(e) => handleContextMenu(e, 'file', 'single', item)}
                        onDragStart={(e) => !isSearchMode && handleDragStart(e, item)}
                        onDragEnd={handleDragEnd}
                      >
                        <td className="px-6 py-4">
                          <div className="flex items-center gap-3">
                            <FileIcon className="w-5 h-5 text-blue-500" />
                            <div>
                              <span className="text-dark-text">{item.key.split('/').pop()}</span>
                              {isSearchMode && (
                                <button
                                  onClick={() => {
                                    clearFilters()
                                    navigateToFolder(getFolderPath(item.key) === '/' ? '' : getFolderPath(item.key))
                                  }}
                                  className="block text-xs text-blue-400 hover:text-blue-300 mt-0.5 text-left"
                                  title="Go to folder"
                                >
                                  {getFolderPath(item.key) === '/' ? '/' : getFolderPath(item.key)}
                                </button>
                              )}
                            </div>
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
                              onClick={() => handleRenameClick(item)}
                              className="p-2 hover:bg-yellow-600 hover:text-white text-yellow-500 rounded transition-colors"
                              title="Rename"
                            >
                              <Pencil className="w-4 h-4" />
                            </button>
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
        </>
      )}

      {/* Create Folder Modal */}
      {showCreateFolderModal && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
          <div className="bg-dark-surface border border-dark-border rounded-lg p-6 w-full max-w-md">
            <h2 className="text-2xl font-bold text-dark-text mb-6">Create Folder</h2>
            <form onSubmit={handleCreateFolder} className="space-y-4">
              {splitView && !createFolderFromContextMenu && (
                <div>
                  <label className="block text-sm font-medium text-dark-text mb-2">Create In</label>
                  <div className="flex gap-2">
                    <button
                      type="button"
                      onClick={() => setCreateFolderPane('left')}
                      className={`flex-1 px-4 py-2 rounded-lg transition-colors ${
                        createFolderPane === 'left'
                          ? 'bg-blue-600 text-white'
                          : 'bg-dark-bg border border-dark-border text-dark-text hover:bg-dark-surfaceHover'
                      }`}
                    >
                      Left Pane
                    </button>
                    <button
                      type="button"
                      onClick={() => setCreateFolderPane('right')}
                      className={`flex-1 px-4 py-2 rounded-lg transition-colors ${
                        createFolderPane === 'right'
                          ? 'bg-blue-600 text-white'
                          : 'bg-dark-bg border border-dark-border text-dark-text hover:bg-dark-surfaceHover'
                      }`}
                    >
                      Right Pane
                    </button>
                  </div>
                  <p className="text-xs text-dark-textSecondary mt-1">
                    {createFolderPane === 'left' ? currentPrefix || '/' : rightPrefix || '/'}
                  </p>
                </div>
              )}
              {splitView && createFolderFromContextMenu && (
                <p className="text-sm text-dark-textSecondary">
                  Creating in: <span className="text-dark-text font-medium">{createFolderPane === 'left' ? 'Left' : 'Right'} Pane</span>
                  <span className="text-dark-textSecondary"> ({createFolderPane === 'left' ? currentPrefix || '/' : rightPrefix || '/'})</span>
                </p>
              )}
              <div>
                <label className="block text-sm font-medium text-dark-text mb-2">Folder Name</label>
                <input
                  type="text"
                  value={newFolderName}
                  onChange={(e) => setNewFolderName(e.target.value)}
                  className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
                  placeholder="my-folder"
                  required
                  pattern="[a-zA-Z0-9_\-]+"
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

      {/* Rename Modal */}
      {showRenameModal && renameTarget && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
          <div className="bg-dark-surface border border-dark-border rounded-lg p-6 w-full max-w-md">
            <h2 className="text-2xl font-bold text-dark-text mb-6">Rename File</h2>
            <form onSubmit={handleRename} className="space-y-4">
              <div>
                <label className="block text-sm font-medium text-dark-text mb-2">New Name</label>
                <input
                  type="text"
                  value={newFileName}
                  onChange={(e) => setNewFileName(e.target.value)}
                  className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
                  placeholder="new-filename.txt"
                  required
                  autoFocus
                />
                <p className="text-xs text-dark-textSecondary mt-1">
                  Enter the new name for the file (without path)
                </p>
              </div>

              <div className="flex gap-3 pt-4">
                <button
                  type="button"
                  onClick={() => {
                    setShowRenameModal(false)
                    setRenameTarget(null)
                    setNewFileName('')
                  }}
                  className="flex-1 px-4 py-2 border border-dark-border text-dark-text rounded-lg hover:bg-dark-surfaceHover transition-colors"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="flex-1 bg-blue-600 hover:bg-blue-700 text-white px-4 py-2 rounded-lg transition-colors"
                >
                  Rename
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* Context Menu */}
      {contextMenu.show && (
        <div
          className="fixed bg-dark-surface border border-dark-border rounded-lg shadow-xl py-1 z-50 min-w-[180px]"
          style={{ left: contextMenu.x, top: contextMenu.y }}
          onClick={(e) => e.stopPropagation()}
        >
          {contextMenu.type === 'pane' && (
            <>
              <button
                onClick={() => {
                  if (contextMenu.pane === 'right') {
                    setCreateFolderPane('right')
                  } else {
                    setCreateFolderPane('left')
                  }
                  setCreateFolderFromContextMenu(true)
                  setShowCreateFolderModal(true)
                  setContextMenu(prev => ({ ...prev, show: false }))
                }}
                className="w-full px-4 py-2 text-left text-dark-text hover:bg-dark-surfaceHover flex items-center gap-3"
              >
                <FolderPlus className="w-4 h-4 text-yellow-500" />
                New Folder
              </button>
              <button
                onClick={() => {
                  setUploadTargetPane(contextMenu.pane)
                  handleUploadClick()
                  setContextMenu(prev => ({ ...prev, show: false }))
                }}
                className="w-full px-4 py-2 text-left text-dark-text hover:bg-dark-surfaceHover flex items-center gap-3"
              >
                <Upload className="w-4 h-4 text-blue-500" />
                Upload Files
              </button>
              <div className="border-t border-dark-border my-1" />
              <button
                onClick={() => {
                  loadObjects()
                  setContextMenu(prev => ({ ...prev, show: false }))
                }}
                className="w-full px-4 py-2 text-left text-dark-text hover:bg-dark-surfaceHover flex items-center gap-3"
              >
                <RefreshCw className="w-4 h-4 text-dark-textSecondary" />
                Refresh
              </button>
            </>
          )}

          {contextMenu.type === 'folder' && contextMenu.item && contextMenu.item.isFolder && (
            <>
              <button
                onClick={() => {
                  const folder = contextMenu.item as FolderItem
                  if (contextMenu.pane === 'right') {
                    navigateToFolder(folder.prefix, 'right')
                  } else if (contextMenu.pane === 'left' || contextMenu.pane === 'single') {
                    navigateToFolder(folder.prefix, 'left')
                  }
                  setContextMenu(prev => ({ ...prev, show: false }))
                }}
                className="w-full px-4 py-2 text-left text-dark-text hover:bg-dark-surfaceHover flex items-center gap-3"
              >
                <FolderOpen className="w-4 h-4 text-yellow-500" />
                Open
              </button>
              <button
                onClick={() => {
                  const folder = contextMenu.item as FolderItem
                  handleCopyPath(folder.prefix)
                }}
                className="w-full px-4 py-2 text-left text-dark-text hover:bg-dark-surfaceHover flex items-center gap-3"
              >
                <Copy className="w-4 h-4 text-dark-textSecondary" />
                Copy Path
              </button>
              <div className="border-t border-dark-border my-1" />
              <button
                onClick={() => {
                  const folder = contextMenu.item as FolderItem
                  if (!bucketName) return
                  if (!confirm(`Delete folder "${folder.name}" and all its contents?`)) {
                    setContextMenu(prev => ({ ...prev, show: false }))
                    return
                  }
                  // Delete all objects with this prefix
                  const objectsToDelete = objects.filter(obj => obj.key.startsWith(folder.prefix))
                  Promise.all(objectsToDelete.map(obj => bucketApi.deleteObject(bucketName, obj.key)))
                    .then(() => loadObjects())
                    .catch((err) => setError(err.response?.data?.message || 'Failed to delete folder'))
                  setContextMenu(prev => ({ ...prev, show: false }))
                }}
                className="w-full px-4 py-2 text-left text-red-500 hover:bg-red-500/10 flex items-center gap-3"
              >
                <Trash2 className="w-4 h-4" />
                Delete Folder
              </button>
            </>
          )}

          {contextMenu.type === 'file' && contextMenu.item && !contextMenu.item.isFolder && (
            <>
              <button
                onClick={() => handleOpenInNewTab(contextMenu.item as FileItem)}
                className="w-full px-4 py-2 text-left text-dark-text hover:bg-dark-surfaceHover flex items-center gap-3"
              >
                <ExternalLink className="w-4 h-4 text-blue-500" />
                Open in New Tab
              </button>
              <button
                onClick={() => {
                  handleDownload(contextMenu.item as FileItem)
                  setContextMenu(prev => ({ ...prev, show: false }))
                }}
                className="w-full px-4 py-2 text-left text-dark-text hover:bg-dark-surfaceHover flex items-center gap-3"
              >
                <Download className="w-4 h-4 text-blue-500" />
                Download
              </button>
              <div className="border-t border-dark-border my-1" />
              <button
                onClick={() => {
                  handleRenameClick(contextMenu.item as FileItem)
                  setContextMenu(prev => ({ ...prev, show: false }))
                }}
                className="w-full px-4 py-2 text-left text-dark-text hover:bg-dark-surfaceHover flex items-center gap-3"
              >
                <Pencil className="w-4 h-4 text-yellow-500" />
                Rename
              </button>
              <button
                onClick={() => handleCopyPath((contextMenu.item as FileItem).key)}
                className="w-full px-4 py-2 text-left text-dark-text hover:bg-dark-surfaceHover flex items-center gap-3"
              >
                <Copy className="w-4 h-4 text-dark-textSecondary" />
                Copy Path
              </button>
              <div className="border-t border-dark-border my-1" />
              <button
                onClick={() => handleShowFileInfo(contextMenu.item as FileItem)}
                className="w-full px-4 py-2 text-left text-dark-text hover:bg-dark-surfaceHover flex items-center gap-3"
              >
                <Info className="w-4 h-4 text-dark-textSecondary" />
                File Info
              </button>
              <div className="border-t border-dark-border my-1" />
              <button
                onClick={() => {
                  handleDelete(contextMenu.item as FileItem)
                  setContextMenu(prev => ({ ...prev, show: false }))
                }}
                className="w-full px-4 py-2 text-left text-red-500 hover:bg-red-500/10 flex items-center gap-3"
              >
                <Trash2 className="w-4 h-4" />
                Delete
              </button>
            </>
          )}
        </div>
      )}

      {/* File Info Modal */}
      {showFileInfo && fileInfoTarget && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
          <div className="bg-dark-surface border border-dark-border rounded-lg p-6 w-full max-w-md">
            <div className="flex items-center gap-3 mb-6">
              <FileIcon className="w-8 h-8 text-blue-500" />
              <h2 className="text-2xl font-bold text-dark-text">File Information</h2>
            </div>
            <div className="space-y-4">
              <div>
                <label className="block text-sm font-medium text-dark-textSecondary mb-1">Name</label>
                <p className="text-dark-text break-all">{fileInfoTarget.key.split('/').pop()}</p>
              </div>
              <div>
                <label className="block text-sm font-medium text-dark-textSecondary mb-1">Full Path</label>
                <p className="text-dark-text break-all font-mono text-sm bg-dark-bg px-3 py-2 rounded">{fileInfoTarget.key}</p>
              </div>
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label className="block text-sm font-medium text-dark-textSecondary mb-1">Size</label>
                  <p className="text-dark-text">{formatFileSize(fileInfoTarget.size)}</p>
                </div>
                <div>
                  <label className="block text-sm font-medium text-dark-textSecondary mb-1">Type</label>
                  <p className="text-dark-text">{fileInfoTarget.content_type}</p>
                </div>
              </div>
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label className="block text-sm font-medium text-dark-textSecondary mb-1">Created</label>
                  <p className="text-dark-text text-sm">{new Date(fileInfoTarget.created_at).toLocaleString()}</p>
                </div>
                <div>
                  <label className="block text-sm font-medium text-dark-textSecondary mb-1">Modified</label>
                  <p className="text-dark-text text-sm">{new Date(fileInfoTarget.updated_at).toLocaleString()}</p>
                </div>
              </div>
              {fileInfoTarget.etag && (
                <div>
                  <label className="block text-sm font-medium text-dark-textSecondary mb-1">ETag</label>
                  <p className="text-dark-text font-mono text-sm bg-dark-bg px-3 py-2 rounded break-all">{fileInfoTarget.etag}</p>
                </div>
              )}
            </div>
            <div className="flex gap-3 pt-6">
              <button
                onClick={() => {
                  handleCopyPath(fileInfoTarget.key)
                  setShowFileInfo(false)
                }}
                className="flex-1 px-4 py-2 border border-dark-border text-dark-text rounded-lg hover:bg-dark-surfaceHover transition-colors flex items-center justify-center gap-2"
              >
                <Copy className="w-4 h-4" />
                Copy Path
              </button>
              <button
                onClick={() => {
                  setShowFileInfo(false)
                  setFileInfoTarget(null)
                }}
                className="flex-1 bg-blue-600 hover:bg-blue-700 text-white px-4 py-2 rounded-lg transition-colors"
              >
                Close
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
