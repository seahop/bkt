import axios from 'axios'
import type { AuthResponse, User, Bucket, AccessKey, AccessKeyResponse, Object as StorageObject, S3Configuration, Group } from '../types'

// Shape returned by the objects endpoint when the backend paginates results.
// Older/simple backends may just return a bare StorageObject[] instead.
export interface ListObjectsResponse {
  objects: StorageObject[]
  is_truncated?: boolean
  next_continuation_token?: string
}

// A single version of an object, as returned by the versions endpoint.
export interface ObjectVersion {
  version_id: string
  is_latest: boolean
  is_delete_marker: boolean
  size: number
  content_type: string
  etag: string
  last_modified: string
}

export interface ListObjectVersionsResponse {
  bucket: string
  key: string
  versioning: string
  versions: ObjectVersion[]
}

// Use relative URL to leverage Vite's proxy configuration
// The proxy will forward /api/* requests to the backend
const api = axios.create({
  baseURL: '/api',
})

// Request interceptor to add auth token and set Content-Type
api.interceptors.request.use((config) => {
  const token = localStorage.getItem('token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }

  // Set Content-Type to application/json for non-FormData requests
  // For FormData, axios will automatically set multipart/form-data with boundary
  if (!(config.data instanceof FormData)) {
    config.headers['Content-Type'] = 'application/json'
  }

  return config
})

// Response interceptor to handle errors
api.interceptors.response.use(
  (response) => response,
  async (error) => {
    if (error.response?.status === 401) {
      // Clear all auth data
      localStorage.removeItem('token')
      localStorage.removeItem('refresh_token')
      localStorage.removeItem('auth-storage') // Clear persisted Zustand state
      sessionStorage.removeItem('auth_timestamp')

      // Import authStore dynamically to avoid circular dependency
      const { useAuthStore } = await import('../store/authStore')

      // Clear the auth store state
      useAuthStore.setState({
        user: null,
        token: null,
        isAuthenticated: false,
        lastAuthTime: null
      })

      // Only redirect if not already on login page and not on callback pages
      const path = window.location.pathname
      if (!path.includes('/login') && !path.includes('/callback')) {
        window.location.href = '/login'
      }
    }
    return Promise.reject(error)
  }
)

// Auth API
export const authApi = {
  register: async (username: string, email: string, password: string): Promise<AuthResponse> => {
    const { data } = await api.post<AuthResponse>('/auth/register', { username, email, password })
    return data
  },

  login: async (username: string, password: string): Promise<AuthResponse> => {
    const { data } = await api.post<AuthResponse>('/auth/login', { username, password })
    return data
  },

  logout: async (): Promise<void> => {
    await api.post('/auth/logout')
  },

  refreshToken: async (refreshToken: string): Promise<{ token: string }> => {
    const { data } = await api.post<{ token: string }>('/auth/refresh', { refresh_token: refreshToken })
    return data
  },
}

// User API
export const userApi = {
  getCurrentUser: async (): Promise<User> => {
    const { data } = await api.get<User>('/users/me')
    return data
  },

  updateCurrentUser: async (email?: string, password?: string): Promise<User> => {
    const { data } = await api.put<User>('/users/me', { email, password })
    return data
  },

  listUsers: async (): Promise<User[]> => {
    const { data } = await api.get<User[]>('/users')
    return data
  },

  createUser: async (username: string, email: string, password: string, is_admin: boolean = false): Promise<User> => {
    const { data } = await api.post<User>('/users', { username, email, password, is_admin })
    return data
  },

  deleteUser: async (id: string): Promise<void> => {
    await api.delete(`/users/${id}`)
  },
}

// Bucket API
export const bucketApi = {
  listBuckets: async (): Promise<Bucket[]> => {
    const { data } = await api.get<Bucket[]>('/buckets')
    return data
  },

  createBucket: async (name: string, isPublic: boolean = false, region: string = 'us-east-1', storageBackend: string = 'local', s3ConfigId?: string): Promise<Bucket> => {
    const { data } = await api.post<Bucket>('/buckets', {
      name,
      is_public: isPublic,
      region,
      storage_backend: storageBackend,
      s3_config_id: s3ConfigId
    })
    return data
  },

  getBucket: async (name: string): Promise<Bucket> => {
    const { data } = await api.get<Bucket>(`/buckets/${name}`)
    return data
  },

  deleteBucket: async (name: string): Promise<void> => {
    await api.delete(`/buckets/${name}`)
  },

  listObjects: async (
    bucketName: string,
    options?: { prefix?: string; maxKeys?: number; continuationToken?: string }
  ): Promise<StorageObject[] | ListObjectsResponse> => {
    const params: Record<string, string | number> = {}
    if (options?.prefix) params.prefix = options.prefix
    if (options?.maxKeys != null) params['max-keys'] = options.maxKeys
    if (options?.continuationToken) params['continuation-token'] = options.continuationToken
    const { data } = await api.get<StorageObject[] | ListObjectsResponse>(
      `/buckets/${bucketName}/objects`,
      { params }
    )
    return data
  },

  uploadObject: async (bucketName: string, key: string, file: File): Promise<StorageObject> => {
    const formData = new FormData()
    formData.append('file', file)
    formData.append('key', key)
    // Don't set Content-Type - let axios handle multipart/form-data with boundary
    const { data } = await api.post<StorageObject>(`/buckets/${bucketName}/objects`, formData)
    return data
  },

  uploadObjectAsync: async (bucketName: string, key: string, file: File): Promise<{ upload_id: string; status: string; message: string }> => {
    const formData = new FormData()
    formData.append('file', file)
    formData.append('key', key)
    const { data } = await api.post<{ upload_id: string; status: string; message: string }>(`/buckets/${bucketName}/objects/async`, formData)
    return data
  },

  getUploadStatus: async (uploadId: string): Promise<{
    id: string
    status: string
    filename: string
    object_key: string
    total_size: number
    uploaded_size: number
    progress_percent: number
    error_message?: string
    object_id?: string
    created_at: string
    completed_at?: string
  }> => {
    const { data } = await api.get(`/uploads/${uploadId}/status`)
    return data
  },

  listUploads: async (status?: string): Promise<Array<{
    id: string
    status: string
    filename: string
    object_key: string
    total_size: number
    uploaded_size: number
    progress_percent: number
    error_message?: string
    object_id?: string
    created_at: string
    completed_at?: string
  }>> => {
    const params = status ? { status } : {}
    const { data } = await api.get('/uploads', { params })
    return data
  },

  deleteObject: async (bucketName: string, key: string): Promise<void> => {
    await api.delete(`/buckets/${bucketName}/objects/${key}`)
  },

  downloadObject: async (bucketName: string, key: string): Promise<Blob> => {
    const { data } = await api.get(`/buckets/${bucketName}/objects/${key}`, {
      responseType: 'blob',
    })
    return data
  },

  presignObject: async (bucketName: string, key: string, expiresIn: number): Promise<{ url: string; expires_at: string; capped_by_key: boolean; signing_key_name?: string }> => {
    const { data } = await api.post<{ url: string; expires_at: string; capped_by_key: boolean; signing_key_name?: string }>(`/buckets/${bucketName}/objects/presign`, {
      key,
      expires_in: expiresIn,
    })
    return data
  },

  moveObject: async (bucketName: string, sourceKey: string, destinationKey: string): Promise<StorageObject> => {
    const { data } = await api.post<StorageObject>(`/buckets/${bucketName}/objects/move`, {
      source_key: sourceKey,
      destination_key: destinationKey,
    })
    return data
  },

  renameObject: async (bucketName: string, sourceKey: string, newName: string): Promise<StorageObject> => {
    const { data } = await api.post<StorageObject>(`/buckets/${bucketName}/objects/rename`, {
      source_key: sourceKey,
      new_name: newName,
    })
    return data
  },

  moveFolder: async (bucketName: string, sourcePrefix: string, destinationPrefix: string): Promise<{ moved_count: number }> => {
    const { data } = await api.post<{ moved_count: number }>(`/buckets/${bucketName}/folders/move`, {
      source_prefix: sourcePrefix,
      destination_prefix: destinationPrefix,
    })
    return data
  },

  listObjectVersions: async (bucketName: string, key: string): Promise<ListObjectVersionsResponse> => {
    const { data } = await api.get<ListObjectVersionsResponse>(`/buckets/${bucketName}/object-versions`, {
      params: { key },
    })
    return data
  },

  restoreObjectVersion: async (bucketName: string, key: string, versionId: string): Promise<{ message: string }> => {
    const { data } = await api.post<{ message: string }>(`/buckets/${bucketName}/objects/restore`, {
      key,
      version_id: versionId,
    })
    return data
  },

  deleteObjectVersion: async (bucketName: string, key: string, versionId: string): Promise<{ message: string }> => {
    const { data } = await api.delete<{ message: string }>(`/buckets/${bucketName}/object-versions`, {
      params: { key, version_id: versionId },
    })
    return data
  },

  setBucketVersioning: async (bucketName: string, versioning: 'enabled' | 'suspended'): Promise<{ message: string }> => {
    const { data } = await api.put<{ message: string }>(`/buckets/${bucketName}/versioning`, { versioning })
    return data
  },

  setBucketLifecycle: async (
    bucketName: string,
    cfg: { expire_days: number; prefix?: string; noncurrent_expire_days?: number }
  ): Promise<{ message: string }> => {
    const { data } = await api.put<{ message: string }>(`/buckets/${bucketName}/lifecycle`, cfg)
    return data
  },

  setBucketSettings: async (
    bucketName: string,
    settings: {
      quota_bytes?: number
      retention_days?: number
      webhook_url?: string
      webhook_secret?: string
      webhook_events?: string
      replicate_to?: string
    }
  ): Promise<{ message: string }> => {
    const { data } = await api.put<{ message: string }>(`/buckets/${bucketName}/settings`, settings)
    return data
  },
}

// Access Key API
export const accessKeyApi = {
  listAccessKeys: async (): Promise<AccessKey[]> => {
    const { data } = await api.get<AccessKey[]>('/access-keys')
    return data
  },

  createAccessKey: async (): Promise<AccessKeyResponse> => {
    const { data } = await api.post<AccessKeyResponse>('/access-keys')
    return data
  },

  revokeAccessKey: async (id: string): Promise<void> => {
    await api.delete(`/access-keys/${id}`)
  },
}

// Group API (admin)
export const groupApi = {
  listGroups: async (): Promise<Group[]> => {
    const { data } = await api.get<Group[]>('/groups')
    return data
  },

  createGroup: async (name: string, description?: string): Promise<Group> => {
    const { data } = await api.post<Group>('/groups', { name, description })
    return data
  },

  deleteGroup: async (id: string): Promise<void> => {
    await api.delete(`/groups/${id}`)
  },

  addMember: async (groupId: string, userId: string): Promise<void> => {
    await api.post(`/groups/${groupId}/members`, { user_id: userId })
  },

  removeMember: async (groupId: string, userId: string): Promise<void> => {
    await api.delete(`/groups/${groupId}/members/${userId}`)
  },

  attachPolicy: async (groupId: string, policyId: string): Promise<void> => {
    await api.post(`/groups/${groupId}/policies`, { policy_id: policyId })
  },

  detachPolicy: async (groupId: string, policyId: string): Promise<void> => {
    await api.delete(`/groups/${groupId}/policies/${policyId}`)
  },
}

// STS (temporary credentials) API
export const stsApi = {
  issueTemporaryCredentials: async (
    durationSeconds?: number,
    readOnly?: boolean
  ): Promise<{ access_key: string; secret_key: string; expires_at: string; read_only: boolean }> => {
    const { data } = await api.post<{ access_key: string; secret_key: string; expires_at: string; read_only: boolean }>(
      '/sts/credentials',
      { duration_seconds: durationSeconds, read_only: readOnly }
    )
    return data
  },
}

// S3 Configuration API
export const s3ConfigApi = {
  listS3Configs: async (): Promise<S3Configuration[]> => {
    const { data } = await api.get<S3Configuration[]>('/s3-configs')
    return data
  },

  createS3Config: async (config: {
    name: string
    endpoint: string
    region: string
    access_key_id: string
    secret_access_key: string
    bucket_prefix?: string
    use_ssl?: boolean
    force_path_style?: boolean
    is_default?: boolean
  }): Promise<S3Configuration> => {
    const { data } = await api.post<S3Configuration>('/s3-configs', config)
    return data
  },

  getS3Config: async (id: string): Promise<S3Configuration> => {
    const { data } = await api.get<S3Configuration>(`/s3-configs/${id}`)
    return data
  },

  updateS3Config: async (id: string, config: {
    name?: string
    endpoint?: string
    region?: string
    access_key_id?: string
    secret_access_key?: string
    bucket_prefix?: string
    use_ssl?: boolean
    force_path_style?: boolean
    is_default?: boolean
  }): Promise<S3Configuration> => {
    const { data } = await api.put<S3Configuration>(`/s3-configs/${id}`, config)
    return data
  },

  deleteS3Config: async (id: string): Promise<void> => {
    await api.delete(`/s3-configs/${id}`)
  },
}

export default api
