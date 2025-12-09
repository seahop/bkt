import axios from 'axios'
import type { AuthResponse, User, Bucket, AccessKey, AccessKeyResponse, Policy, Object as StorageObject, S3Configuration } from '../types'

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

      // Import authStore dynamically to avoid circular dependency
      const { useAuthStore } = await import('../store/authStore')

      // Clear the auth store state
      useAuthStore.setState({
        user: null,
        token: null,
        isAuthenticated: false
      })

      // Only redirect if not already on login page
      if (!window.location.pathname.includes('/login')) {
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

  listObjects: async (bucketName: string): Promise<StorageObject[]> => {
    const { data } = await api.get<StorageObject[]>(`/buckets/${bucketName}/objects`)
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

// Policy API
export const policyApi = {
  listPolicies: async (): Promise<Policy[]> => {
    const { data } = await api.get<Policy[]>('/policies')
    return data
  },

  createPolicy: async (name: string, document: string): Promise<Policy> => {
    const { data } = await api.post<Policy>('/policies', { name, document })
    return data
  },

  getPolicy: async (id: string): Promise<Policy> => {
    const { data } = await api.get<Policy>(`/policies/${id}`)
    return data
  },

  updatePolicy: async (id: string, name: string, document: string): Promise<Policy> => {
    const { data } = await api.put<Policy>(`/policies/${id}`, { name, document })
    return data
  },

  deletePolicy: async (id: string): Promise<void> => {
    await api.delete(`/policies/${id}`)
  },

  attachPolicyToUser: async (userId: string, policyId: string): Promise<void> => {
    await api.post(`/policies/users/${userId}/attach`, { policy_id: policyId })
  },

  detachPolicyFromUser: async (userId: string, policyId: string): Promise<void> => {
    await api.delete(`/policies/users/${userId}/detach/${policyId}`)
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
