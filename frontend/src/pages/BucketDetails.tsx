import { useEffect, useState, useRef } from 'react'
import { useParams, Link } from 'react-router-dom'
import { FolderOpen, Upload, Download, Trash2, File as FileIcon, ArrowLeft, RefreshCw, Folder, FolderPlus, Home, Loader2, Pencil, Columns2, Info, Copy, ExternalLink, Search, X, Calendar, Filter, ChevronRight, CheckCircle2, XCircle, Link2, Check, History, Settings2 } from 'lucide-react'
import { bucketApi } from '../services/api'
import type { ObjectVersion } from '../services/api'
import type { Object as StorageObject, Bucket } from '../types'
import { getErrorMessage } from '../utils/errors'

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
  // Truncation state: set when the backend indicates more objects are available
  // than were returned (buckets with >1000 objects).
  const [truncated, setTruncated] = useState(false)
  const [continuationToken, setContinuationToken] = useState<string | undefined>(undefined)
  const [loadingMore, setLoadingMore] = useState(false)
  const [showCreateFolderModal, setShowCreateFolderModal] = useState(false)
  const [newFolderName, setNewFolderName] = useState('')
  const [createFolderPane, setCreateFolderPane] = useState<'left' | 'right'>('left')
  const [createFolderFromContextMenu, setCreateFolderFromContextMenu] = useState(false)
  const [activeUploads, setActiveUploads] = useState<ActiveUpload[]>([])
  const [uploadTargetPane, setUploadTargetPane] = useState<'left' | 'right' | 'single'>('left')
  const fileInputRef = useRef<HTMLInputElement>(null)

  // Tracks whether the component is still mounted so the recursive upload
  // poller can stop scheduling timeouts / setState calls after unmount.
  const isMountedRef = useRef(true)

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

  // Share (presigned URL) modal state
  const [shareTarget, setShareTarget] = useState<string | null>(null)
  const [shareExpiry, setShareExpiry] = useState(3600)
  const [shareResult, setShareResult] = useState<{ url: string; expires_at: string; capped_by_key: boolean; signing_key_name?: string } | null>(null)
  const [shareLoading, setShareLoading] = useState(false)
  const [shareError, setShareError] = useState('')
  const [shareNeedsKey, setShareNeedsKey] = useState(false)
  const [shareCopied, setShareCopied] = useState(false)

  // Version history modal state
  const [versionsTarget, setVersionsTarget] = useState<string | null>(null)
  const [versions, setVersions] = useState<ObjectVersion[]>([])
  const [versionsLoading, setVersionsLoading] = useState(false)
  const [versionsError, setVersionsError] = useState('')
  const [versionsBucketState, setVersionsBucketState] = useState('')

  // Bucket settings modal state
  const [showBucketSettings, setShowBucketSettings] = useState(false)
  const [bucketInfo, setBucketInfo] = useState<Bucket | null>(null)
  const [settingsLoading, setSettingsLoading] = useState(false)
  const [settingsError, setSettingsError] = useState('')
  const [settingsSuccess, setSettingsSuccess] = useState('')
  const [lifecycleExpireDays, setLifecycleExpireDays] = useState('')
  const [lifecyclePrefix, setLifecyclePrefix] = useState('')
  const [lifecycleNoncurrentDays, setLifecycleNoncurrentDays] = useState('')
  const [lifecycleSaving, setLifecycleSaving] = useState(false)
  // Quota / retention / notifications & replication settings state
  const [quotaMb, setQuotaMb] = useState('')
  const [retentionDays, setRetentionDays] = useState('')
  const [webhookUrl, setWebhookUrl] = useState('')
  const [webhookSecret, setWebhookSecret] = useState('')
  const [webhookCreated, setWebhookCreated] = useState(false)
  const [webhookRemoved, setWebhookRemoved] = useState(false)
  const [replicateTo, setReplicateTo] = useState('')
  const [generalSaving, setGeneralSaving] = useState(false)

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

  // Mark the component unmounted so in-flight pollers stop.
  useEffect(() => {
    isMountedRef.current = true
    return () => {
      isMountedRef.current = false
    }
  }, [])

  // The listing is fetched unscoped (see loadObjects), so folder navigation
  // doesn't need a refetch — only a bucket change does.
  useEffect(() => {
    if (bucketName) {
      loadObjects()
      loadActiveUploads()
    }
  }, [bucketName])

  const loadObjects = async () => {
    if (!bucketName) return

    try {
      setError('')
      // Fetch unscoped: split view, global search, and folder derivation all
      // read from this single `objects` state and assume it spans the whole
      // bucket, so a prefix-scoped fetch would break any pane/search outside
      // the current folder. Buckets past the page size surface is_truncated
      // and page in via the continuation token instead.
      const data = await bucketApi.listObjects(bucketName)
      // Handle both array response and object response with objects property
      const objectList = Array.isArray(data) ? data : data.objects || []
      setObjects(objectList)
      if (!Array.isArray(data)) {
        setTruncated(!!data.is_truncated)
        setContinuationToken(data.next_continuation_token)
      } else {
        setTruncated(false)
        setContinuationToken(undefined)
      }
    } catch (error: any) {
      console.error('Failed to load objects:', error)
      setError(getErrorMessage(error, 'Failed to load objects'))
    } finally {
      setLoading(false)
    }
  }

  // Fetch the next page of objects (when the listing was truncated) and append.
  const loadMoreObjects = async () => {
    if (!bucketName || !continuationToken) return

    try {
      setLoadingMore(true)
      setError('')
      const data = await bucketApi.listObjects(bucketName, { continuationToken })
      const more = Array.isArray(data) ? data : data.objects || []
      setObjects(prev => [...prev, ...more])
      if (!Array.isArray(data)) {
        setTruncated(!!data.is_truncated)
        setContinuationToken(data.next_continuation_token)
      } else {
        setTruncated(false)
        setContinuationToken(undefined)
      }
    } catch (error: any) {
      console.error('Failed to load more objects:', error)
      setError(getErrorMessage(error, 'Failed to load more objects'))
    } finally {
      setLoadingMore(false)
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
      // Stop polling entirely if the component has unmounted / navigated away.
      if (!isMountedRef.current) return

      try {
        const status = await bucketApi.getUploadStatus(uploadId)

        if (!isMountedRef.current) return

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
            if (!isMountedRef.current) return
            setActiveUploads(prev => prev.filter(u => u.uploadId !== uploadId))
          }, 2000)
          await loadObjects()
        } else if (status.status === 'failed') {
          // Keep failed upload visible for user to see error
          setTimeout(() => {
            if (!isMountedRef.current) return
            setActiveUploads(prev => prev.filter(u => u.uploadId !== uploadId))
          }, 10000)
        } else if (attempts < maxAttempts) {
          attempts++
          setTimeout(poll, 1000) // Poll every second
        }
      } catch (error) {
        console.error('Failed to poll upload status:', error)
        if (!isMountedRef.current) return
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
            setError(getErrorMessage(error, `Failed to upload ${file.name}`))
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
      setError(getErrorMessage(error, 'Failed to upload file'))
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
      setError(getErrorMessage(error, 'Failed to create folder'))
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
      setError(getErrorMessage(error, 'Failed to download object'))
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
      setError(getErrorMessage(error, 'Failed to delete object'))
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
      setError(getErrorMessage(error, 'Failed to rename object'))
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
        setError(getErrorMessage(error, 'Failed to move folder'))
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
        setError(getErrorMessage(error, 'Failed to move object'))
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

  // Share (presigned URL) handlers
  const handleShareClick = (object: StorageObject) => {
    setShareTarget(object.key)
    setShareExpiry(3600)
    setShareResult(null)
    setShareError('')
    setShareNeedsKey(false)
    setShareCopied(false)
    setContextMenu(prev => ({ ...prev, show: false }))
  }

  const closeShareModal = () => {
    setShareTarget(null)
    setShareExpiry(3600)
    setShareResult(null)
    setShareError('')
    setShareNeedsKey(false)
    setShareCopied(false)
  }

  const handleGenerateLink = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!bucketName || shareTarget === null) return

    setShareLoading(true)
    setShareError('')
    setShareNeedsKey(false)

    try {
      const result = await bucketApi.presignObject(bucketName, shareTarget, shareExpiry)
      setShareResult(result)
    } catch (error: any) {
      console.error('Failed to generate share link:', error)
      if (error?.response?.status === 409) {
        setShareNeedsKey(true)
      } else {
        setShareError(getErrorMessage(error, 'Failed to generate share link'))
      }
    } finally {
      setShareLoading(false)
    }
  }

  const handleCopyShareUrl = () => {
    if (!shareResult) return
    navigator.clipboard.writeText(shareResult.url)
    setShareCopied(true)
    setTimeout(() => {
      if (!isMountedRef.current) return
      setShareCopied(false)
    }, 2000)
  }

  // Version history handlers
  const loadVersions = async (key: string) => {
    if (!bucketName) return

    setVersionsLoading(true)
    setVersionsError('')

    try {
      const data = await bucketApi.listObjectVersions(bucketName, key)
      const sorted = [...(data.versions || [])].sort(
        (a, b) => new Date(b.last_modified).getTime() - new Date(a.last_modified).getTime()
      )
      setVersions(sorted)
      setVersionsBucketState(data.versioning || '')
    } catch (error: any) {
      console.error('Failed to load object versions:', error)
      setVersionsError(getErrorMessage(error, 'Failed to load version history'))
    } finally {
      setVersionsLoading(false)
    }
  }

  const handleVersionHistoryClick = (object: StorageObject) => {
    setVersionsTarget(object.key)
    setVersions([])
    setVersionsError('')
    setVersionsBucketState('')
    setContextMenu(prev => ({ ...prev, show: false }))
    loadVersions(object.key)
  }

  const closeVersionsModal = () => {
    setVersionsTarget(null)
    setVersions([])
    setVersionsError('')
    setVersionsBucketState('')
  }

  const handleRestoreVersion = async (versionId: string) => {
    if (!bucketName || versionsTarget === null) return

    try {
      setVersionsError('')
      await bucketApi.restoreObjectVersion(bucketName, versionsTarget, versionId)
      await loadVersions(versionsTarget)
      await loadObjects()
    } catch (error: any) {
      console.error('Failed to restore version:', error)
      setVersionsError(getErrorMessage(error, 'Failed to restore version'))
    }
  }

  const handleDeleteVersion = async (versionId: string) => {
    if (!bucketName || versionsTarget === null) return
    if (!confirm('Permanently delete this version? This cannot be undone.')) return

    try {
      setVersionsError('')
      await bucketApi.deleteObjectVersion(bucketName, versionsTarget, versionId)
      await loadVersions(versionsTarget)
      await loadObjects()
    } catch (error: any) {
      console.error('Failed to delete version:', error)
      setVersionsError(getErrorMessage(error, 'Failed to delete version'))
    }
  }

  // Bucket settings handlers
  const applyLifecyclePrefill = (bucket: Bucket) => {
    let expireDays = ''
    let prefix = ''
    let noncurrentDays = ''
    if (bucket.lifecycle) {
      try {
        const cfg = JSON.parse(bucket.lifecycle)
        if (cfg && typeof cfg === 'object') {
          if (cfg.expire_days) expireDays = String(cfg.expire_days)
          if (cfg.prefix) prefix = String(cfg.prefix)
          if (cfg.noncurrent_expire_days) noncurrentDays = String(cfg.noncurrent_expire_days)
        }
      } catch {
        // Malformed lifecycle JSON — leave the form empty.
      }
    }
    setLifecycleExpireDays(expireDays)
    setLifecyclePrefix(prefix)
    setLifecycleNoncurrentDays(noncurrentDays)
  }

  // Compose the webhook events CSV in a stable order ("created,removed").
  const composeWebhookEvents = (created: boolean, removed: boolean): string =>
    [created ? 'created' : '', removed ? 'removed' : ''].filter(Boolean).join(',')

  const applySettingsPrefill = (bucket: Bucket) => {
    setQuotaMb(bucket.quota_bytes ? String(Math.round(bucket.quota_bytes / (1024 * 1024))) : '')
    setRetentionDays(bucket.retention_days ? String(bucket.retention_days) : '')
    setWebhookUrl(bucket.webhook_url || '')
    setWebhookSecret('') // never returned by the backend; only sent when the user types one
    const events = (bucket.webhook_events || '').split(',').map(e => e.trim())
    setWebhookCreated(events.includes('created'))
    setWebhookRemoved(events.includes('removed'))
    setReplicateTo(bucket.replicate_to || '')
  }

  const handleSaveGeneralSettings = async () => {
    if (!bucketName) return

    setSettingsError('')
    setSettingsSuccess('')

    // Only send the fields the user actually changed.
    const payload: {
      quota_bytes?: number
      retention_days?: number
      webhook_url?: string
      webhook_secret?: string
      webhook_events?: string
      replicate_to?: string
    } = {}

    const newQuotaBytes = quotaMb.trim() === '' ? 0 : Math.round((parseFloat(quotaMb) || 0) * 1024 * 1024)
    if (newQuotaBytes !== (bucketInfo?.quota_bytes || 0)) {
      payload.quota_bytes = newQuotaBytes
    }

    const newRetentionDays = parseInt(retentionDays, 10) || 0
    if (newRetentionDays !== (bucketInfo?.retention_days || 0)) {
      payload.retention_days = newRetentionDays
    }

    if (webhookUrl.trim() !== (bucketInfo?.webhook_url || '')) {
      payload.webhook_url = webhookUrl.trim()
    }
    if (webhookSecret) {
      payload.webhook_secret = webhookSecret
    }

    const storedEvents = (bucketInfo?.webhook_events || '').split(',').map(e => e.trim())
    const storedCsv = composeWebhookEvents(storedEvents.includes('created'), storedEvents.includes('removed'))
    const newCsv = composeWebhookEvents(webhookCreated, webhookRemoved)
    if (newCsv !== storedCsv) {
      payload.webhook_events = newCsv
    }

    if (replicateTo.trim() !== (bucketInfo?.replicate_to || '')) {
      payload.replicate_to = replicateTo.trim()
    }

    if (Object.keys(payload).length === 0) {
      setSettingsSuccess('No changes to save')
      return
    }

    setGeneralSaving(true)
    try {
      await bucketApi.setBucketSettings(bucketName, payload)
      setSettingsSuccess('Settings saved')
      const bucket = await bucketApi.getBucket(bucketName)
      setBucketInfo(bucket)
      applySettingsPrefill(bucket)
    } catch (error: any) {
      console.error('Failed to save bucket settings:', error)
      setSettingsError(getErrorMessage(error, 'Failed to save bucket settings'))
    } finally {
      setGeneralSaving(false)
    }
  }

  const openBucketSettings = async () => {
    if (!bucketName) return

    setShowBucketSettings(true)
    setSettingsError('')
    setSettingsSuccess('')
    setSettingsLoading(true)

    try {
      const bucket = await bucketApi.getBucket(bucketName)
      setBucketInfo(bucket)
      applyLifecyclePrefill(bucket)
      applySettingsPrefill(bucket)
    } catch (error: any) {
      console.error('Failed to load bucket:', error)
      setSettingsError(getErrorMessage(error, 'Failed to load bucket settings'))
    } finally {
      setSettingsLoading(false)
    }
  }

  const closeBucketSettings = () => {
    setShowBucketSettings(false)
    setSettingsError('')
    setSettingsSuccess('')
  }

  const handleSetVersioning = async (versioning: 'enabled' | 'suspended') => {
    if (!bucketName) return

    setSettingsError('')
    setSettingsSuccess('')

    try {
      await bucketApi.setBucketVersioning(bucketName, versioning)
      const bucket = await bucketApi.getBucket(bucketName)
      setBucketInfo(bucket)
      setSettingsSuccess(versioning === 'enabled' ? 'Versioning enabled' : 'Versioning suspended')
    } catch (error: any) {
      console.error('Failed to update versioning:', error)
      if (error?.response?.status === 403) {
        setSettingsError('Only the bucket owner or an admin can change this')
      } else {
        setSettingsError(getErrorMessage(error, 'Failed to update versioning'))
      }
    }
  }

  const handleSaveLifecycle = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!bucketName) return

    setSettingsError('')
    setSettingsSuccess('')
    setLifecycleSaving(true)

    try {
      const expireDays = parseInt(lifecycleExpireDays, 10) || 0
      const noncurrentDays = parseInt(lifecycleNoncurrentDays, 10) || 0
      await bucketApi.setBucketLifecycle(bucketName, {
        expire_days: expireDays,
        prefix: lifecyclePrefix.trim() || undefined,
        noncurrent_expire_days: noncurrentDays,
      })
      setSettingsSuccess(
        expireDays === 0 && noncurrentDays === 0 ? 'Lifecycle rules cleared' : 'Lifecycle rules saved'
      )
      const bucket = await bucketApi.getBucket(bucketName)
      setBucketInfo(bucket)
      applyLifecyclePrefill(bucket)
    } catch (error: any) {
      console.error('Failed to save lifecycle rules:', error)
      if (error?.response?.status === 403) {
        setSettingsError('Only the bucket owner or an admin can change this')
      } else {
        setSettingsError(getErrorMessage(error, 'Failed to save lifecycle rules'))
      }
    } finally {
      setLifecycleSaving(false)
    }
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
      <div className="flex flex-col items-center justify-center h-64 gap-3">
        <div className="spinner" />
        <p className="text-sm text-dark-textSecondary">Loading objects…</p>
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
    <div className="page">
      {/* Header */}
      <div className="mb-8">
        <Link
          to="/buckets"
          className="inline-flex items-center gap-1.5 text-sm text-dark-textSecondary hover:text-dark-text transition-colors mb-4"
        >
          <ArrowLeft className="w-4 h-4" />
          Back to Buckets
        </Link>
        <div className="flex items-start justify-between gap-4 mb-6">
          <div>
            <h1 className="page-title font-mono">{bucketName}</h1>
            <p className="page-subtitle">
              {browserItems.length} item{browserItems.length !== 1 ? 's' : ''}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <button onClick={openBucketSettings} className="btn-icon" title="Bucket settings">
              <Settings2 className="w-4 h-4" />
            </button>
            <button
              onClick={() => {
                setSplitView(!splitView)
                if (!splitView) {
                  setRightPrefix(currentPrefix) // Initialize right pane to same location
                }
              }}
              className={
                splitView
                  ? 'btn-secondary !bg-accent-soft !text-blue-400 !border-blue-500/40'
                  : 'btn-secondary'
              }
              title={splitView ? 'Exit split view' : 'Enable split view'}
            >
              <Columns2 className="w-4 h-4" />
              Split View
            </button>
            <button onClick={loadObjects} className="btn-icon" title="Refresh">
              <RefreshCw className="w-4 h-4" />
            </button>
            <button
              onClick={() => {
                setCreateFolderFromContextMenu(false)
                setShowCreateFolderModal(true)
              }}
              className="btn-secondary"
            >
              <FolderPlus className="w-4 h-4" />
              New Folder
            </button>
            <button
              onClick={() => {
                setUploadTargetPane(splitView ? 'left' : 'single')
                handleUploadClick()
              }}
              disabled={uploading}
              className="btn-primary"
            >
              <Upload className="w-4 h-4" />
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
        <div className="card p-4">
          <div className="flex items-center gap-2">
            {/* Search Input */}
            <div className="flex-1 relative">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-dark-textMuted pointer-events-none" />
              <input
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder="Search files… (use * for wildcard, e.g. *.jpg, report*)"
                className="input !pl-9 !pr-9 !py-2"
              />
              {searchQuery && (
                <button
                  onClick={() => setSearchQuery('')}
                  className="absolute right-2 top-1/2 -translate-y-1/2 btn-icon !w-6 !h-6"
                  title="Clear search"
                >
                  <X className="w-4 h-4" />
                </button>
              )}
            </div>

            {/* Filter Toggle Button */}
            <button
              onClick={() => setShowFilters(!showFilters)}
              className={
                showFilters || (filterDateFrom || filterDateTo || filterExtension || filterMinSize || filterMaxSize || filterMaxDepth)
                  ? 'btn-secondary !bg-accent-soft !text-blue-400 !border-blue-500/40'
                  : 'btn-secondary'
              }
            >
              <Filter className="w-4 h-4" />
              Filters
              {(filterDateFrom || filterDateTo || filterExtension || filterMinSize || filterMaxSize || filterMaxDepth) && (
                <span className="badge-blue !px-1.5 !py-0">
                  {[filterDateFrom, filterDateTo, filterExtension, filterMinSize, filterMaxSize, filterMaxDepth].filter(Boolean).length}
                </span>
              )}
            </button>

            {/* Clear Filters */}
            {hasActiveFilters && (
              <button onClick={clearFilters} className="btn-ghost">
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
                  <label className="label">File Extension</label>
                  <input
                    type="text"
                    value={filterExtension}
                    onChange={(e) => setFilterExtension(e.target.value)}
                    placeholder="e.g. jpg, png, pdf"
                    className="input !py-2"
                  />
                </div>

                {/* Max Depth Filter */}
                <div>
                  <label className="label">Max Folder Depth</label>
                  <input
                    type="number"
                    min="0"
                    value={filterMaxDepth}
                    onChange={(e) => setFilterMaxDepth(e.target.value)}
                    placeholder="e.g. 2 (0 = root only)"
                    className="input !py-2"
                  />
                </div>

                {/* Size Filter */}
                <div className="sm:col-span-2 lg:col-span-1">
                  <label className="label">File Size</label>
                  <div className="flex gap-2">
                    <input
                      type="text"
                      value={filterMinSize}
                      onChange={(e) => setFilterMinSize(e.target.value)}
                      placeholder="Min (e.g. 1MB)"
                      className="input !py-2 flex-1"
                    />
                    <input
                      type="text"
                      value={filterMaxSize}
                      onChange={(e) => setFilterMaxSize(e.target.value)}
                      placeholder="Max (e.g. 10MB)"
                      className="input !py-2 flex-1"
                    />
                  </div>
                </div>
              </div>

              {/* Row 2: Date Filters */}
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                {/* Date From Filter */}
                <div>
                  <label className="label">Modified After</label>
                  <div className="relative">
                    <Calendar className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-dark-textMuted pointer-events-none" />
                    <input
                      type="date"
                      value={filterDateFrom}
                      onChange={(e) => setFilterDateFrom(e.target.value)}
                      className="input !py-2 !pl-9"
                    />
                  </div>
                </div>

                {/* Date To Filter */}
                <div>
                  <label className="label">Modified Before</label>
                  <div className="relative">
                    <Calendar className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-dark-textMuted pointer-events-none" />
                    <input
                      type="date"
                      value={filterDateTo}
                      onChange={(e) => setFilterDateTo(e.target.value)}
                      className="input !py-2 !pl-9"
                    />
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* Search Tips */}
          {searchQuery && !filterDateFrom && !filterDateTo && !filterExtension && !filterMinSize && !filterMaxSize && (
            <p className="mt-2 text-xs text-dark-textSecondary">
              Tip: Use <code className="kbd-mono">*</code> for any characters, <code className="kbd-mono">?</code> for a single character
            </p>
          )}

          {/* Active Filter Summary */}
          {hasActiveFilters && (
            <div className="mt-3 flex flex-wrap gap-1.5">
              {searchQuery && (
                <span className="badge-blue">
                  Search: "{searchQuery}"
                  <button onClick={() => setSearchQuery('')} className="hover:text-blue-300"><X className="w-3 h-3" /></button>
                </span>
              )}
              {filterExtension && (
                <span className="badge-green">
                  Extension: {filterExtension}
                  <button onClick={() => setFilterExtension('')} className="hover:text-green-300"><X className="w-3 h-3" /></button>
                </span>
              )}
              {filterDateFrom && (
                <span className="badge-purple">
                  After: {filterDateFrom}
                  <button onClick={() => setFilterDateFrom('')} className="hover:text-purple-300"><X className="w-3 h-3" /></button>
                </span>
              )}
              {filterDateTo && (
                <span className="badge-purple">
                  Before: {filterDateTo}
                  <button onClick={() => setFilterDateTo('')} className="hover:text-purple-300"><X className="w-3 h-3" /></button>
                </span>
              )}
              {filterMinSize && (
                <span className="badge-yellow">
                  Min: {filterMinSize}
                  <button onClick={() => setFilterMinSize('')} className="hover:text-yellow-300"><X className="w-3 h-3" /></button>
                </span>
              )}
              {filterMaxSize && (
                <span className="badge-yellow">
                  Max: {filterMaxSize}
                  <button onClick={() => setFilterMaxSize('')} className="hover:text-yellow-300"><X className="w-3 h-3" /></button>
                </span>
              )}
              {filterMaxDepth && (
                <span className="badge-gray">
                  Depth: {filterMaxDepth}
                  <button onClick={() => setFilterMaxDepth('')} className="hover:text-dark-text"><X className="w-3 h-3" /></button>
                </span>
              )}
            </div>
          )}

          {/* Search Results Info */}
          {isSearchMode && (
            <div className="mt-3 flex items-center gap-2 text-sm text-dark-textSecondary">
              <Search className="w-4 h-4 text-dark-textMuted" />
              <span>
                Showing <span className="text-dark-text font-medium">{browserItems.length}</span> result{browserItems.length !== 1 ? 's' : ''} from entire bucket
              </span>
            </div>
          )}
        </div>

      </div>

      {/* Active Uploads Progress */}
      {activeUploads.length > 0 && (
        <div className="card mb-6 divide-y divide-dark-border/60">
          {activeUploads.map((upload) => (
            <div key={upload.uploadId} className="px-4 py-3">
              <div className="flex items-center justify-between gap-3 mb-2">
                <div className="flex items-center gap-2 min-w-0">
                  {upload.status === 'completed' ? (
                    <CheckCircle2 className="w-4 h-4 text-green-400 shrink-0" />
                  ) : upload.status === 'failed' ? (
                    <XCircle className="w-4 h-4 text-red-400 shrink-0" />
                  ) : (
                    <Loader2 className="w-4 h-4 text-blue-500 animate-spin shrink-0" />
                  )}
                  <span className="text-sm font-medium text-dark-text truncate">{upload.filename}</span>
                </div>
                <span className="tabular-nums text-xs text-dark-textSecondary shrink-0">
                  {Math.round(upload.progress)}%
                </span>
              </div>
              <div className="w-full h-1.5 rounded-full bg-dark-inset overflow-hidden">
                <div
                  className={`h-1.5 rounded-full transition-all duration-300 ${
                    upload.status === 'completed' ? 'bg-green-500' :
                    upload.status === 'failed' ? 'bg-red-500' :
                    'bg-blue-500'
                  }`}
                  style={{ width: `${upload.progress}%` }}
                />
              </div>
              {upload.error && (
                <p className="mt-2 text-xs text-red-400">{upload.error}</p>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Error Message */}
      {error && <div className="alert-error mb-6">{error}</div>}

      {/* Truncation notice */}
      {truncated && (
        <div className="alert-info mb-6 !items-center justify-between gap-4">
          <span>Results are truncated — this bucket contains more objects than are shown.</span>
          <button
            onClick={loadMoreObjects}
            disabled={loadingMore || !continuationToken}
            className="btn-secondary btn-sm shrink-0"
          >
            {loadingMore ? 'Loading...' : 'Load more'}
          </button>
        </div>
      )}

      {/* Pane Content */}
      {splitView ? (
        // Dual pane view
        <div className="flex gap-4">
          {/* Left Pane */}
          <div className="card flex-1 min-w-0 overflow-hidden flex flex-col">
            {/* Left Pane Header: breadcrumbs + label */}
            <div className="flex items-center justify-between gap-3 px-3 py-2 border-b border-dark-border bg-dark-inset/50">
              <div className="flex items-center gap-1 text-sm min-w-0 flex-wrap">
                <button
                  onClick={() => navigateToFolder('', 'left')}
                  onDragOver={(e) => handleDragOver(e, '')}
                  onDragLeave={handleDragLeave}
                  onDrop={(e) => handleDrop(e, '')}
                  className={`flex items-center gap-1.5 px-1.5 py-0.5 rounded-md transition-colors ${
                    dropTarget === ''
                      ? 'bg-accent-soft ring-1 ring-blue-500/50 text-blue-400'
                      : leftBreadcrumbs.length === 0
                        ? 'text-dark-text font-medium'
                        : 'text-dark-textSecondary hover:text-dark-text'
                  }`}
                >
                  <Home className="w-3.5 h-3.5" />
                  <span className="truncate">{bucketName}</span>
                </button>
                {leftBreadcrumbs.map((crumb, index) => (
                  <div key={index} className="flex items-center gap-1">
                    <ChevronRight className="w-3.5 h-3.5 text-dark-textMuted shrink-0" />
                    <button
                      onClick={() => navigateToFolder(crumb.prefix, 'left')}
                      onDragOver={(e) => handleDragOver(e, crumb.prefix)}
                      onDragLeave={handleDragLeave}
                      onDrop={(e) => handleDrop(e, crumb.prefix)}
                      className={`px-1.5 py-0.5 rounded-md transition-colors truncate ${
                        dropTarget === crumb.prefix
                          ? 'bg-accent-soft ring-1 ring-blue-500/50 text-blue-400'
                          : index === leftBreadcrumbs.length - 1
                            ? 'text-dark-text font-medium'
                            : 'text-dark-textSecondary hover:text-dark-text'
                      }`}
                    >
                      {crumb.name}
                    </button>
                  </div>
                ))}
              </div>
              <span className="badge-gray shrink-0">Left</span>
            </div>
            {/* Left Table */}
            <div
              className={`overflow-hidden transition-colors min-h-[300px] flex-1 flex flex-col ${
                dropTarget === `pane:left:${currentPrefix}`
                  ? 'bg-accent-soft ring-1 ring-inset ring-blue-500/50'
                  : ''
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
              <table className="table">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th className="!text-right">Size</th>
                    <th className="!text-right">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {browserItems.map((item, index) => (
                    item.isFolder ? (
                      <tr
                        key={`left-folder-${index}`}
                        className={`cursor-pointer ${
                          dropTarget === item.prefix ? 'bg-accent-soft ring-1 ring-blue-500/50 ring-inset' : ''
                        } ${draggedItem?.isFolder && (draggedItem as FolderItem).prefix === item.prefix ? 'opacity-50' : ''}`}
                        draggable
                        onContextMenu={(e) => handleContextMenu(e, 'folder', 'left', item)}
                        onDragStart={(e) => handleDragStart(e, item)}
                        onDragEnd={handleDragEnd}
                        onDragOver={(e) => { e.stopPropagation(); handleDragOver(e, item.prefix) }}
                        onDragLeave={handleDragLeave}
                        onDrop={(e) => { e.stopPropagation(); handleDrop(e, item.prefix) }}
                      >
                        <td onClick={() => navigateToFolder(item.prefix, 'left')}>
                          <div className="flex items-center gap-2.5">
                            <Folder className={`w-4 h-4 shrink-0 ${dropTarget === item.prefix ? 'text-blue-300' : 'text-blue-400'}`} />
                            <span className="text-dark-text font-medium truncate">{item.name}/</span>
                          </div>
                        </td>
                        <td className="!text-right tabular-nums text-xs !text-dark-textMuted">—</td>
                        <td></td>
                      </tr>
                    ) : (
                      <tr
                        key={`left-${item.id}`}
                        className={
                          draggedItem && !draggedItem.isFolder && draggedItem.id === item.id ? 'opacity-50' : ''
                        }
                        draggable={!isSearchMode}
                        onContextMenu={(e) => handleContextMenu(e, 'file', 'left', item)}
                        onDragStart={(e) => !isSearchMode && handleDragStart(e, item)}
                        onDragEnd={handleDragEnd}
                      >
                        <td>
                          <div className="flex items-center gap-2.5">
                            <FileIcon className="w-4 h-4 shrink-0 text-dark-textMuted" />
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
                        <td className="!text-right tabular-nums !text-dark-textSecondary text-xs whitespace-nowrap">{formatFileSize(item.size)}</td>
                        <td>
                          <div className="flex items-center justify-end gap-1">
                            <button onClick={() => handleRenameClick(item)} className="btn-icon" title="Rename"><Pencil className="w-4 h-4" /></button>
                            <button onClick={() => handleDownload(item)} className="btn-icon" title="Download"><Download className="w-4 h-4" /></button>
                            <button onClick={() => handleShareClick(item)} className="btn-icon" title="Get shareable link"><Link2 className="w-4 h-4" /></button>
                            <button onClick={() => handleVersionHistoryClick(item)} className="btn-icon" title="Version history"><History className="w-4 h-4" /></button>
                            <button onClick={() => handleDelete(item)} className="btn-icon hover:!text-red-400 hover:!bg-red-500/10" title="Delete"><Trash2 className="w-4 h-4" /></button>
                          </div>
                        </td>
                      </tr>
                    )
                  ))}
                </tbody>
              </table>
            </div>
          </div>

          {/* Right Pane */}
          <div className="card flex-1 min-w-0 overflow-hidden flex flex-col">
            {/* Right Pane Header: breadcrumbs + label */}
            <div className="flex items-center justify-between gap-3 px-3 py-2 border-b border-dark-border bg-dark-inset/50">
              <div className="flex items-center gap-1 text-sm min-w-0 flex-wrap">
                <button
                  onClick={() => navigateToFolder('', 'right')}
                  onDragOver={(e) => handleDragOver(e, '')}
                  onDragLeave={handleDragLeave}
                  onDrop={(e) => handleDrop(e, '')}
                  className={`flex items-center gap-1.5 px-1.5 py-0.5 rounded-md transition-colors ${
                    dropTarget === ''
                      ? 'bg-accent-soft ring-1 ring-blue-500/50 text-blue-400'
                      : rightBreadcrumbs.length === 0
                        ? 'text-dark-text font-medium'
                        : 'text-dark-textSecondary hover:text-dark-text'
                  }`}
                >
                  <Home className="w-3.5 h-3.5" />
                  <span className="truncate">{bucketName}</span>
                </button>
                {rightBreadcrumbs.map((crumb, index) => (
                  <div key={index} className="flex items-center gap-1">
                    <ChevronRight className="w-3.5 h-3.5 text-dark-textMuted shrink-0" />
                    <button
                      onClick={() => navigateToFolder(crumb.prefix, 'right')}
                      onDragOver={(e) => handleDragOver(e, crumb.prefix)}
                      onDragLeave={handleDragLeave}
                      onDrop={(e) => handleDrop(e, crumb.prefix)}
                      className={`px-1.5 py-0.5 rounded-md transition-colors truncate ${
                        dropTarget === crumb.prefix
                          ? 'bg-accent-soft ring-1 ring-blue-500/50 text-blue-400'
                          : index === rightBreadcrumbs.length - 1
                            ? 'text-dark-text font-medium'
                            : 'text-dark-textSecondary hover:text-dark-text'
                      }`}
                    >
                      {crumb.name}
                    </button>
                  </div>
                ))}
              </div>
              <span className="badge-gray shrink-0">Right</span>
            </div>
            {/* Right Table */}
            <div
              className={`overflow-hidden transition-colors min-h-[300px] flex-1 flex flex-col ${
                dropTarget === `pane:right:${rightPrefix}`
                  ? 'bg-accent-soft ring-1 ring-inset ring-blue-500/50'
                  : ''
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
              <table className="table">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th className="!text-right">Size</th>
                    <th className="!text-right">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {rightBrowserItems.map((item, index) => (
                    item.isFolder ? (
                      <tr
                        key={`right-folder-${index}`}
                        className={`cursor-pointer ${
                          dropTarget === item.prefix ? 'bg-accent-soft ring-1 ring-blue-500/50 ring-inset' : ''
                        } ${draggedItem?.isFolder && (draggedItem as FolderItem).prefix === item.prefix ? 'opacity-50' : ''}`}
                        draggable
                        onContextMenu={(e) => handleContextMenu(e, 'folder', 'right', item)}
                        onDragStart={(e) => handleDragStart(e, item)}
                        onDragEnd={handleDragEnd}
                        onDragOver={(e) => { e.stopPropagation(); handleDragOver(e, item.prefix) }}
                        onDragLeave={handleDragLeave}
                        onDrop={(e) => { e.stopPropagation(); handleDrop(e, item.prefix) }}
                      >
                        <td onClick={() => navigateToFolder(item.prefix, 'right')}>
                          <div className="flex items-center gap-2.5">
                            <Folder className={`w-4 h-4 shrink-0 ${dropTarget === item.prefix ? 'text-blue-300' : 'text-blue-400'}`} />
                            <span className="text-dark-text font-medium truncate">{item.name}/</span>
                          </div>
                        </td>
                        <td className="!text-right tabular-nums text-xs !text-dark-textMuted">—</td>
                        <td></td>
                      </tr>
                    ) : (
                      <tr
                        key={`right-${item.id}`}
                        className={
                          draggedItem && !draggedItem.isFolder && draggedItem.id === item.id ? 'opacity-50' : ''
                        }
                        draggable={!isSearchMode}
                        onContextMenu={(e) => handleContextMenu(e, 'file', 'right', item)}
                        onDragStart={(e) => !isSearchMode && handleDragStart(e, item)}
                        onDragEnd={handleDragEnd}
                      >
                        <td>
                          <div className="flex items-center gap-2.5">
                            <FileIcon className="w-4 h-4 shrink-0 text-dark-textMuted" />
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
                        <td className="!text-right tabular-nums !text-dark-textSecondary text-xs whitespace-nowrap">{formatFileSize(item.size)}</td>
                        <td>
                          <div className="flex items-center justify-end gap-1">
                            <button onClick={() => handleRenameClick(item)} className="btn-icon" title="Rename"><Pencil className="w-4 h-4" /></button>
                            <button onClick={() => handleDownload(item)} className="btn-icon" title="Download"><Download className="w-4 h-4" /></button>
                            <button onClick={() => handleShareClick(item)} className="btn-icon" title="Get shareable link"><Link2 className="w-4 h-4" /></button>
                            <button onClick={() => handleVersionHistoryClick(item)} className="btn-icon" title="Version history"><History className="w-4 h-4" /></button>
                            <button onClick={() => handleDelete(item)} className="btn-icon hover:!text-red-400 hover:!bg-red-500/10" title="Delete"><Trash2 className="w-4 h-4" /></button>
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
          <div className="flex items-center gap-1 text-sm mb-4 flex-wrap">
            <button
              onClick={() => setCurrentPrefix('')}
              onDragOver={(e) => handleDragOver(e, '')}
              onDragLeave={handleDragLeave}
              onDrop={(e) => handleDrop(e, '')}
              className={`flex items-center gap-1.5 px-1.5 py-0.5 rounded-md transition-colors ${
                dropTarget === ''
                  ? 'bg-accent-soft ring-1 ring-blue-500/50 text-blue-400'
                  : leftBreadcrumbs.length === 0
                    ? 'text-dark-text font-medium'
                    : 'text-dark-textSecondary hover:text-dark-text'
              }`}
            >
              <Home className="w-3.5 h-3.5" />
              <span>{bucketName}</span>
            </button>
            {leftBreadcrumbs.map((crumb, index) => (
              <div key={index} className="flex items-center gap-1">
                <ChevronRight className="w-3.5 h-3.5 text-dark-textMuted shrink-0" />
                <button
                  onClick={() => navigateToFolder(crumb.prefix)}
                  onDragOver={(e) => handleDragOver(e, crumb.prefix)}
                  onDragLeave={handleDragLeave}
                  onDrop={(e) => handleDrop(e, crumb.prefix)}
                  className={`px-1.5 py-0.5 rounded-md transition-colors ${
                    dropTarget === crumb.prefix
                      ? 'bg-accent-soft ring-1 ring-blue-500/50 text-blue-400'
                      : index === leftBreadcrumbs.length - 1
                        ? 'text-dark-text font-medium'
                        : 'text-dark-textSecondary hover:text-dark-text'
                  }`}
                >
                  {crumb.name}
                </button>
              </div>
            ))}
          </div>

          {/* Objects List */}
          {browserItems.length === 0 ? (
            <div className="card empty-state">
              {isSearchMode ? (
                <>
                  <Search className="empty-state-icon" />
                  <h3 className="text-base font-semibold text-dark-text mb-1">No results found</h3>
                  <p className="text-sm text-dark-textSecondary mb-5 max-w-sm">Try adjusting your search or filters.</p>
                  <button onClick={clearFilters} className="btn-secondary">
                    Clear Search & Filters
                  </button>
                </>
              ) : (
                <>
                  <FolderOpen className="empty-state-icon" />
                  <h3 className="text-base font-semibold text-dark-text mb-1">This folder is empty</h3>
                  <p className="text-sm text-dark-textSecondary mb-5 max-w-sm">
                    Upload files or create a folder to get started — you can also drag files here to move them.
                  </p>
                  <div className="flex gap-2 justify-center">
                    <button
                      onClick={() => {
                        setCreateFolderFromContextMenu(false)
                        setShowCreateFolderModal(true)
                      }}
                      className="btn-secondary"
                    >
                      <FolderPlus className="w-4 h-4" />
                      Create Folder
                    </button>
                    <button
                      onClick={() => {
                        setUploadTargetPane('single')
                        handleUploadClick()
                      }}
                      className="btn-primary"
                    >
                      <Upload className="w-4 h-4" />
                      Upload Files
                    </button>
                  </div>
                </>
              )}
            </div>
          ) : (
            <div
              className={`card overflow-hidden transition-colors min-h-[400px] flex flex-col ${
                dropTarget === `pane:single:${currentPrefix}`
                  ? '!border-blue-500/50 bg-accent-soft ring-1 ring-inset ring-blue-500/50'
                  : ''
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
              <table className="table">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th className="!text-right">Size</th>
                    <th>Type</th>
                    <th className="!text-right">Last Modified</th>
                    <th className="!text-right">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {browserItems.map((item, index) => (
                    item.isFolder ? (
                      <tr
                        key={`folder-${index}`}
                        className={`cursor-pointer ${
                          dropTarget === item.prefix ? 'bg-accent-soft ring-1 ring-blue-500/50 ring-inset' : ''
                        } ${draggedItem?.isFolder && (draggedItem as FolderItem).prefix === item.prefix ? 'opacity-50' : ''}`}
                        draggable
                        onContextMenu={(e) => handleContextMenu(e, 'folder', 'single', item)}
                        onDragStart={(e) => handleDragStart(e, item)}
                        onDragEnd={handleDragEnd}
                        onDragOver={(e) => { e.stopPropagation(); handleDragOver(e, item.prefix) }}
                        onDragLeave={handleDragLeave}
                        onDrop={(e) => { e.stopPropagation(); handleDrop(e, item.prefix) }}
                      >
                        <td onClick={() => navigateToFolder(item.prefix)}>
                          <div className="flex items-center gap-2.5">
                            <Folder className={`w-4 h-4 shrink-0 ${dropTarget === item.prefix ? 'text-blue-300' : 'text-blue-400'}`} />
                            <span className="text-dark-text font-medium">{item.name}/</span>
                          </div>
                        </td>
                        <td className="!text-right tabular-nums text-xs !text-dark-textMuted">—</td>
                        <td className="!text-dark-textSecondary text-xs">Folder</td>
                        <td className="!text-right tabular-nums text-xs !text-dark-textMuted">—</td>
                        <td></td>
                      </tr>
                    ) : (
                      <tr
                        key={item.id}
                        className={
                          draggedItem && !draggedItem.isFolder && draggedItem.id === item.id ? 'opacity-50' : ''
                        }
                        draggable={!isSearchMode}
                        onContextMenu={(e) => handleContextMenu(e, 'file', 'single', item)}
                        onDragStart={(e) => !isSearchMode && handleDragStart(e, item)}
                        onDragEnd={handleDragEnd}
                      >
                        <td>
                          <div className="flex items-center gap-2.5">
                            <FileIcon className="w-4 h-4 shrink-0 text-dark-textMuted" />
                            <div className="min-w-0">
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
                        <td className="!text-right tabular-nums !text-dark-textSecondary text-xs whitespace-nowrap">{formatFileSize(item.size)}</td>
                        <td className="!text-dark-textSecondary text-xs truncate max-w-[160px]">{item.content_type}</td>
                        <td className="!text-right tabular-nums !text-dark-textSecondary text-xs whitespace-nowrap">
                          {new Date(item.updated_at).toLocaleString()}
                        </td>
                        <td>
                          <div className="flex items-center justify-end gap-1">
                            <button
                              onClick={() => handleRenameClick(item)}
                              className="btn-icon"
                              title="Rename"
                            >
                              <Pencil className="w-4 h-4" />
                            </button>
                            <button
                              onClick={() => handleDownload(item)}
                              className="btn-icon"
                              title="Download"
                            >
                              <Download className="w-4 h-4" />
                            </button>
                            <button
                              onClick={() => handleShareClick(item)}
                              className="btn-icon"
                              title="Get shareable link"
                            >
                              <Link2 className="w-4 h-4" />
                            </button>
                            <button
                              onClick={() => handleVersionHistoryClick(item)}
                              className="btn-icon"
                              title="Version history"
                            >
                              <History className="w-4 h-4" />
                            </button>
                            <button
                              onClick={() => handleDelete(item)}
                              className="btn-icon hover:!text-red-400 hover:!bg-red-500/10"
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
        <div className="modal-overlay">
          <div className="modal-panel">
            <div className="flex items-center justify-between mb-5">
              <h2 className="modal-title">Create Folder</h2>
            </div>
            <form onSubmit={handleCreateFolder} className="space-y-4">
              {splitView && !createFolderFromContextMenu && (
                <div>
                  <label className="label">Create In</label>
                  <div className="flex gap-2">
                    <button
                      type="button"
                      onClick={() => setCreateFolderPane('left')}
                      className={`flex-1 ${
                        createFolderPane === 'left'
                          ? 'btn-secondary !bg-accent-soft !text-blue-400 !border-blue-500/40'
                          : 'btn-secondary'
                      }`}
                    >
                      Left Pane
                    </button>
                    <button
                      type="button"
                      onClick={() => setCreateFolderPane('right')}
                      className={`flex-1 ${
                        createFolderPane === 'right'
                          ? 'btn-secondary !bg-accent-soft !text-blue-400 !border-blue-500/40'
                          : 'btn-secondary'
                      }`}
                    >
                      Right Pane
                    </button>
                  </div>
                  <p className="help-text font-mono">
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
                <label className="label">Folder Name</label>
                <input
                  type="text"
                  value={newFolderName}
                  onChange={(e) => setNewFolderName(e.target.value)}
                  className="input"
                  placeholder="my-folder"
                  required
                  pattern="[a-zA-Z0-9_\-]+"
                  title="Only letters, numbers, hyphens, and underscores"
                />
                <p className="help-text">
                  Only letters, numbers, hyphens, and underscores
                </p>
              </div>

              <div className="flex justify-end gap-2 mt-6">
                <button
                  type="button"
                  onClick={() => {
                    setShowCreateFolderModal(false)
                    setNewFolderName('')
                  }}
                  className="btn-ghost"
                >
                  Cancel
                </button>
                <button type="submit" className="btn-primary">
                  Create
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* Rename Modal */}
      {showRenameModal && renameTarget && (
        <div className="modal-overlay">
          <div className="modal-panel">
            <div className="flex items-center justify-between mb-5">
              <h2 className="modal-title">Rename File</h2>
            </div>
            <form onSubmit={handleRename} className="space-y-4">
              <div>
                <label className="label">New Name</label>
                <input
                  type="text"
                  value={newFileName}
                  onChange={(e) => setNewFileName(e.target.value)}
                  className="input"
                  placeholder="new-filename.txt"
                  required
                  autoFocus
                />
                <p className="help-text">
                  Enter the new name for the file (without path)
                </p>
              </div>

              <div className="flex justify-end gap-2 mt-6">
                <button
                  type="button"
                  onClick={() => {
                    setShowRenameModal(false)
                    setRenameTarget(null)
                    setNewFileName('')
                  }}
                  className="btn-ghost"
                >
                  Cancel
                </button>
                <button type="submit" className="btn-primary">
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
          className="fixed bg-dark-surface border border-dark-border rounded-lg shadow-elevated py-1.5 z-50 min-w-[180px] animate-scale-in"
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
                className="w-full px-3 py-2 text-left text-sm text-dark-text hover:bg-dark-surfaceHover flex items-center gap-2.5 transition-colors"
              >
                <FolderPlus className="w-4 h-4 text-dark-textMuted" />
                New Folder
              </button>
              <button
                onClick={() => {
                  setUploadTargetPane(contextMenu.pane)
                  handleUploadClick()
                  setContextMenu(prev => ({ ...prev, show: false }))
                }}
                className="w-full px-3 py-2 text-left text-sm text-dark-text hover:bg-dark-surfaceHover flex items-center gap-2.5 transition-colors"
              >
                <Upload className="w-4 h-4 text-dark-textMuted" />
                Upload Files
              </button>
              <div className="border-t border-dark-border my-1" />
              <button
                onClick={() => {
                  loadObjects()
                  setContextMenu(prev => ({ ...prev, show: false }))
                }}
                className="w-full px-3 py-2 text-left text-sm text-dark-text hover:bg-dark-surfaceHover flex items-center gap-2.5 transition-colors"
              >
                <RefreshCw className="w-4 h-4 text-dark-textMuted" />
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
                className="w-full px-3 py-2 text-left text-sm text-dark-text hover:bg-dark-surfaceHover flex items-center gap-2.5 transition-colors"
              >
                <FolderOpen className="w-4 h-4 text-blue-400" />
                Open
              </button>
              <button
                onClick={() => {
                  const folder = contextMenu.item as FolderItem
                  handleCopyPath(folder.prefix)
                }}
                className="w-full px-3 py-2 text-left text-sm text-dark-text hover:bg-dark-surfaceHover flex items-center gap-2.5 transition-colors"
              >
                <Copy className="w-4 h-4 text-dark-textMuted" />
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
                    .catch((err) => setError(getErrorMessage(err, 'Failed to delete folder')))
                  setContextMenu(prev => ({ ...prev, show: false }))
                }}
                className="w-full px-3 py-2 text-left text-sm text-red-400 hover:bg-red-500/10 flex items-center gap-2.5 transition-colors"
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
                className="w-full px-3 py-2 text-left text-sm text-dark-text hover:bg-dark-surfaceHover flex items-center gap-2.5 transition-colors"
              >
                <ExternalLink className="w-4 h-4 text-dark-textMuted" />
                Open in New Tab
              </button>
              <button
                onClick={() => {
                  handleDownload(contextMenu.item as FileItem)
                  setContextMenu(prev => ({ ...prev, show: false }))
                }}
                className="w-full px-3 py-2 text-left text-sm text-dark-text hover:bg-dark-surfaceHover flex items-center gap-2.5 transition-colors"
              >
                <Download className="w-4 h-4 text-dark-textMuted" />
                Download
              </button>
              <div className="border-t border-dark-border my-1" />
              <button
                onClick={() => {
                  handleRenameClick(contextMenu.item as FileItem)
                  setContextMenu(prev => ({ ...prev, show: false }))
                }}
                className="w-full px-3 py-2 text-left text-sm text-dark-text hover:bg-dark-surfaceHover flex items-center gap-2.5 transition-colors"
              >
                <Pencil className="w-4 h-4 text-dark-textMuted" />
                Rename
              </button>
              <button
                onClick={() => handleCopyPath((contextMenu.item as FileItem).key)}
                className="w-full px-3 py-2 text-left text-sm text-dark-text hover:bg-dark-surfaceHover flex items-center gap-2.5 transition-colors"
              >
                <Copy className="w-4 h-4 text-dark-textMuted" />
                Copy Path
              </button>
              <button
                onClick={() => handleShareClick(contextMenu.item as FileItem)}
                className="w-full px-3 py-2 text-left text-sm text-dark-text hover:bg-dark-surfaceHover flex items-center gap-2.5 transition-colors"
              >
                <Link2 className="w-4 h-4 text-dark-textMuted" />
                Get link
              </button>
              <button
                onClick={() => handleVersionHistoryClick(contextMenu.item as FileItem)}
                className="w-full px-3 py-2 text-left text-sm text-dark-text hover:bg-dark-surfaceHover flex items-center gap-2.5 transition-colors"
              >
                <History className="w-4 h-4 text-dark-textMuted" />
                Version history
              </button>
              <div className="border-t border-dark-border my-1" />
              <button
                onClick={() => handleShowFileInfo(contextMenu.item as FileItem)}
                className="w-full px-3 py-2 text-left text-sm text-dark-text hover:bg-dark-surfaceHover flex items-center gap-2.5 transition-colors"
              >
                <Info className="w-4 h-4 text-dark-textMuted" />
                File Info
              </button>
              <div className="border-t border-dark-border my-1" />
              <button
                onClick={() => {
                  handleDelete(contextMenu.item as FileItem)
                  setContextMenu(prev => ({ ...prev, show: false }))
                }}
                className="w-full px-3 py-2 text-left text-sm text-red-400 hover:bg-red-500/10 flex items-center gap-2.5 transition-colors"
              >
                <Trash2 className="w-4 h-4" />
                Delete
              </button>
            </>
          )}
        </div>
      )}

      {/* Share Modal */}
      {shareTarget !== null && (
        <div className="modal-overlay">
          <div className="modal-panel">
            <div className="flex items-center justify-between mb-5">
              <h2 className="modal-title">Share object</h2>
            </div>
            <div className="space-y-4">
              <p className="text-dark-text break-all font-mono text-sm bg-dark-inset border border-dark-border rounded-lg p-3">{shareTarget}</p>

              {shareNeedsKey && (
                <div className="alert-warning">
                  <span>
                    You need an active access key to generate shareable links.{' '}
                    <Link to="/profile" className="underline hover:text-yellow-300">
                      Create one in your profile
                    </Link>
                    .
                  </span>
                </div>
              )}

              {shareError && <div className="alert-error">{shareError}</div>}

              {shareResult ? (
                <>
                  <div>
                    <label className="label">Shareable link</label>
                    <div className="flex items-start gap-2">
                      <p className="flex-1 min-w-0 bg-dark-inset border border-dark-border rounded-lg p-3 font-mono text-xs break-all text-dark-text">
                        {shareResult.url}
                      </p>
                      <button
                        onClick={handleCopyShareUrl}
                        className={`btn-icon shrink-0 ${shareCopied ? '!text-green-400' : ''}`}
                        title={shareCopied ? 'Copied' : 'Copy link'}
                      >
                        {shareCopied ? <Check className="w-4 h-4" /> : <Copy className="w-4 h-4" />}
                      </button>
                    </div>
                    <p className="help-text tabular-nums">
                      Expires {new Date(shareResult.expires_at).toLocaleString()}
                    </p>
                  </div>
                  {shareResult.capped_by_key && (
                    <div className="alert-warning">Capped by your access key's expiry</div>
                  )}
                  <div className="flex justify-end gap-2 mt-6">
                    <button
                      onClick={() => {
                        setShareResult(null)
                        setShareCopied(false)
                      }}
                      className="btn-ghost"
                    >
                      Generate another
                    </button>
                    <button onClick={closeShareModal} className="btn-primary">
                      Close
                    </button>
                  </div>
                </>
              ) : (
                <form onSubmit={handleGenerateLink} className="space-y-4">
                  <div>
                    <label className="label">Link expires in</label>
                    <select
                      value={shareExpiry}
                      onChange={(e) => setShareExpiry(Number(e.target.value))}
                      className="input"
                    >
                      <option value={900}>15 minutes</option>
                      <option value={3600}>1 hour</option>
                      <option value={86400}>24 hours</option>
                      <option value={604800}>7 days</option>
                    </select>
                    <p className="help-text">
                      Anyone with the link can download this object until it expires.
                    </p>
                  </div>
                  <div className="flex justify-end gap-2 mt-6">
                    <button type="button" onClick={closeShareModal} className="btn-ghost">
                      Cancel
                    </button>
                    <button type="submit" disabled={shareLoading} className="btn-primary">
                      {shareLoading && <span className="spinner !w-4 !h-4" />}
                      {shareLoading ? 'Generating...' : 'Generate link'}
                    </button>
                  </div>
                </form>
              )}
            </div>
          </div>
        </div>
      )}

      {/* File Info Modal */}
      {showFileInfo && fileInfoTarget && (
        <div className="modal-overlay">
          <div className="modal-panel">
            <div className="flex items-center justify-between mb-5">
              <div className="flex items-center gap-2.5">
                <FileIcon className="w-5 h-5 text-dark-textMuted" />
                <h2 className="modal-title">File Information</h2>
              </div>
            </div>
            <div className="space-y-4">
              <div>
                <p className="text-xs font-medium text-dark-textSecondary mb-1">Name</p>
                <p className="text-sm text-dark-text break-all">{fileInfoTarget.key.split('/').pop()}</p>
              </div>
              <div>
                <p className="text-xs font-medium text-dark-textSecondary mb-1">Full Path</p>
                <p className="text-dark-text break-all font-mono text-sm bg-dark-inset border border-dark-border rounded-lg p-3">{fileInfoTarget.key}</p>
              </div>
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <p className="text-xs font-medium text-dark-textSecondary mb-1">Size</p>
                  <p className="text-sm text-dark-text tabular-nums">{formatFileSize(fileInfoTarget.size)}</p>
                </div>
                <div>
                  <p className="text-xs font-medium text-dark-textSecondary mb-1">Type</p>
                  <p className="text-sm text-dark-text break-all">{fileInfoTarget.content_type}</p>
                </div>
              </div>
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <p className="text-xs font-medium text-dark-textSecondary mb-1">Created</p>
                  <p className="text-sm text-dark-text tabular-nums">{new Date(fileInfoTarget.created_at).toLocaleString()}</p>
                </div>
                <div>
                  <p className="text-xs font-medium text-dark-textSecondary mb-1">Modified</p>
                  <p className="text-sm text-dark-text tabular-nums">{new Date(fileInfoTarget.updated_at).toLocaleString()}</p>
                </div>
              </div>
              {fileInfoTarget.etag && (
                <div>
                  <p className="text-xs font-medium text-dark-textSecondary mb-1">ETag</p>
                  <p className="text-dark-text font-mono text-sm bg-dark-inset border border-dark-border rounded-lg p-3 break-all">{fileInfoTarget.etag}</p>
                </div>
              )}
            </div>
            <div className="flex justify-end gap-2 mt-6">
              <button
                onClick={() => {
                  handleCopyPath(fileInfoTarget.key)
                  setShowFileInfo(false)
                }}
                className="btn-secondary"
              >
                <Copy className="w-4 h-4" />
                Copy Path
              </button>
              <button
                onClick={() => {
                  setShowFileInfo(false)
                  setFileInfoTarget(null)
                }}
                className="btn-primary"
              >
                Close
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Version History Modal */}
      {versionsTarget !== null && (
        <div className="modal-overlay">
          <div className="modal-panel !max-w-2xl">
            <div className="flex items-center justify-between mb-5">
              <h2 className="modal-title">Version history</h2>
              <button onClick={closeVersionsModal} className="btn-icon" title="Close">
                <X className="w-4 h-4" />
              </button>
            </div>
            <div className="space-y-4">
              <p className="text-dark-text break-all font-mono text-sm bg-dark-inset border border-dark-border rounded-lg p-3">{versionsTarget}</p>

              {!versionsLoading && versionsBucketState !== 'enabled' && (
                <div className="alert-info">
                  Versioning is {versionsBucketState === 'suspended' ? 'suspended' : 'not enabled'} on this bucket — new versions only accrue while versioning is on.
                </div>
              )}

              {versionsError && <div className="alert-error">{versionsError}</div>}

              {versionsLoading ? (
                <div className="flex items-center justify-center py-8">
                  <div className="spinner" />
                </div>
              ) : versions.length === 0 ? (
                <p className="text-sm text-dark-textSecondary text-center py-6">No previous versions</p>
              ) : (
                <div className="border border-dark-border rounded-lg overflow-hidden divide-y divide-dark-border/60">
                  {versions.map((version) => (
                    <div key={version.version_id} className="flex items-center gap-3 px-3 py-2.5">
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2 flex-wrap">
                          <span className="kbd-mono" title={version.version_id}>
                            {version.version_id.slice(0, 8)}…
                          </span>
                          {version.is_latest && <span className="badge-blue">Current</span>}
                          {version.is_delete_marker && <span className="badge-red">Delete marker</span>}
                        </div>
                        <p className="text-xs text-dark-textSecondary tabular-nums mt-1">
                          {formatFileSize(version.size)} · {new Date(version.last_modified).toLocaleString()}
                        </p>
                      </div>
                      <div className="flex items-center gap-1 shrink-0">
                        {!version.is_latest && !version.is_delete_marker && (
                          <button
                            onClick={() => handleRestoreVersion(version.version_id)}
                            className="btn-secondary btn-sm"
                          >
                            Restore
                          </button>
                        )}
                        <button
                          onClick={() => handleDeleteVersion(version.version_id)}
                          className="btn-icon hover:!text-red-400 hover:!bg-red-500/10"
                          title="Delete permanently"
                        >
                          <Trash2 className="w-4 h-4" />
                        </button>
                      </div>
                    </div>
                  ))}
                </div>
              )}

              <div className="flex justify-end gap-2 mt-6">
                <button onClick={closeVersionsModal} className="btn-primary">
                  Close
                </button>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Bucket Settings Modal */}
      {showBucketSettings && (
        <div className="modal-overlay">
          <div className="modal-panel !max-w-lg">
            <div className="flex items-center justify-between mb-5">
              <h2 className="modal-title">Bucket settings</h2>
              <button onClick={closeBucketSettings} className="btn-icon" title="Close">
                <X className="w-4 h-4" />
              </button>
            </div>

            {settingsError && <div className="alert-error mb-4">{settingsError}</div>}
            {settingsSuccess && <div className="alert-success mb-4">{settingsSuccess}</div>}

            {settingsLoading ? (
              <div className="flex items-center justify-center py-8">
                <div className="spinner" />
              </div>
            ) : (
              <div className="space-y-6">
                {/* Versioning */}
                <div>
                  <h3 className="text-base font-semibold text-dark-text mb-2">Versioning</h3>
                  <div className="flex items-center gap-3 mb-3">
                    <span className="text-sm text-dark-textSecondary">Current state:</span>
                    {bucketInfo?.versioning === 'enabled' ? (
                      <span className="badge-green">Enabled</span>
                    ) : bucketInfo?.versioning === 'suspended' ? (
                      <span className="badge-yellow">Suspended</span>
                    ) : (
                      <span className="badge-gray">Disabled</span>
                    )}
                  </div>
                  <div className="flex gap-2">
                    {bucketInfo?.versioning !== 'enabled' && (
                      <button onClick={() => handleSetVersioning('enabled')} className="btn-primary btn-sm">
                        Enable versioning
                      </button>
                    )}
                    {bucketInfo?.versioning === 'enabled' && (
                      <button onClick={() => handleSetVersioning('suspended')} className="btn-secondary btn-sm">
                        Suspend
                      </button>
                    )}
                  </div>
                  <p className="help-text">
                    While enabled, overwritten and deleted objects keep previous versions you can restore.
                  </p>
                </div>

                {/* Lifecycle */}
                <div className="pt-6 border-t border-dark-border">
                  <h3 className="text-base font-semibold text-dark-text mb-2">Lifecycle</h3>
                  <form onSubmit={handleSaveLifecycle} className="space-y-4">
                    <div>
                      <label className="label">Expire objects after (days)</label>
                      <input
                        type="number"
                        min="0"
                        value={lifecycleExpireDays}
                        onChange={(e) => setLifecycleExpireDays(e.target.value)}
                        placeholder="e.g. 30"
                        className="input"
                      />
                    </div>
                    <div>
                      <label className="label">Prefix (optional)</label>
                      <input
                        type="text"
                        value={lifecyclePrefix}
                        onChange={(e) => setLifecyclePrefix(e.target.value)}
                        placeholder="e.g. logs/"
                        className="input font-mono"
                      />
                      <p className="help-text">Only objects whose keys start with this prefix are expired.</p>
                    </div>
                    <div>
                      <label className="label">Expire noncurrent versions after (days)</label>
                      <input
                        type="number"
                        min="0"
                        value={lifecycleNoncurrentDays}
                        onChange={(e) => setLifecycleNoncurrentDays(e.target.value)}
                        placeholder="e.g. 7"
                        className="input"
                      />
                    </div>
                    <p className="help-text">Set both day values to 0 (or leave empty) to clear the lifecycle rules.</p>
                    <div className="flex justify-end gap-2">
                      <button type="submit" disabled={lifecycleSaving} className="btn-primary">
                        {lifecycleSaving && <span className="spinner !w-4 !h-4" />}
                        {lifecycleSaving ? 'Saving...' : 'Save'}
                      </button>
                    </div>
                  </form>
                </div>

                {/* Quota */}
                <div className="pt-6 border-t border-dark-border">
                  <h3 className="text-base font-semibold text-dark-text mb-2">Quota</h3>
                  <div>
                    <label className="label">Storage quota (MB)</label>
                    <input
                      type="number"
                      min="0"
                      value={quotaMb}
                      onChange={(e) => setQuotaMb(e.target.value)}
                      placeholder="e.g. 1024"
                      className="input"
                    />
                    <p className="help-text">
                      Maximum total size of objects in this bucket. Leave empty (or 0) for unlimited.
                    </p>
                  </div>
                </div>

                {/* Retention (WORM) */}
                <div className="pt-6 border-t border-dark-border">
                  <h3 className="text-base font-semibold text-dark-text mb-2">Retention (WORM)</h3>
                  <div className="alert-info mb-3">
                    While a retention period is set, no object version can be permanently deleted
                    until it ages out, and versioning cannot be suspended.
                  </div>
                  <div>
                    <label className="label">Retention period (days)</label>
                    <input
                      type="number"
                      min="0"
                      value={retentionDays}
                      onChange={(e) => setRetentionDays(e.target.value)}
                      placeholder="e.g. 30"
                      disabled={bucketInfo?.versioning !== 'enabled'}
                      className="input"
                    />
                    {bucketInfo?.versioning !== 'enabled' ? (
                      <p className="help-text">Enable versioning above to configure retention.</p>
                    ) : (
                      <p className="help-text">Set to 0 (or leave empty) to remove the retention period.</p>
                    )}
                  </div>
                </div>

                {/* Notifications & replication */}
                <div className="pt-6 border-t border-dark-border">
                  <h3 className="text-base font-semibold text-dark-text mb-2">Notifications &amp; replication</h3>
                  <div className="space-y-4">
                    <div>
                      <label className="label">Webhook URL</label>
                      <input
                        type="text"
                        value={webhookUrl}
                        onChange={(e) => setWebhookUrl(e.target.value)}
                        placeholder="https://example.com/webhook"
                        className="input font-mono"
                      />
                      <p className="help-text">
                        POSTed on object events. Leave empty to disable notifications.
                      </p>
                    </div>
                    <div>
                      <label className="label">Webhook secret</label>
                      <input
                        type="password"
                        value={webhookSecret}
                        onChange={(e) => setWebhookSecret(e.target.value)}
                        placeholder="(unchanged)"
                        className="input"
                      />
                      <p className="help-text">Used to sign webhook payloads. Only sent if you type a new one.</p>
                    </div>
                    <div>
                      <label className="label">Events</label>
                      <div className="flex items-center gap-6">
                        <label className="flex items-center gap-2 text-sm text-dark-text">
                          <input
                            type="checkbox"
                            checked={webhookCreated}
                            onChange={(e) => setWebhookCreated(e.target.checked)}
                            className="w-4 h-4 text-blue-600 bg-dark-inset border-dark-border rounded focus:ring-blue-500"
                          />
                          Created
                        </label>
                        <label className="flex items-center gap-2 text-sm text-dark-text">
                          <input
                            type="checkbox"
                            checked={webhookRemoved}
                            onChange={(e) => setWebhookRemoved(e.target.checked)}
                            className="w-4 h-4 text-blue-600 bg-dark-inset border-dark-border rounded focus:ring-blue-500"
                          />
                          Removed
                        </label>
                      </div>
                    </div>
                    <div>
                      <label className="label">Replicate to bucket</label>
                      <input
                        type="text"
                        value={replicateTo}
                        onChange={(e) => setReplicateTo(e.target.value)}
                        placeholder="e.g. backups"
                        className="input font-mono"
                      />
                      <p className="help-text">
                        Mirror this bucket's objects into another bkt bucket (one-way, periodic).
                      </p>
                    </div>
                  </div>
                  <div className="flex justify-end mt-5">
                    <button
                      onClick={handleSaveGeneralSettings}
                      disabled={generalSaving}
                      className="btn-primary"
                    >
                      {generalSaving && <span className="spinner !w-4 !h-4" />}
                      {generalSaving ? 'Saving...' : 'Save settings'}
                    </button>
                  </div>
                </div>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
