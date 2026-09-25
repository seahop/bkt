import axios from 'axios'
import type { InternalAxiosRequestConfig } from 'axios'
import { withCrossTabLock } from '../utils/crossTabLock'
import { isJwtExpired } from '../utils/jwt'
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

// Every console request carries this header. The backend then keeps the
// refresh token out of JSON bodies (it lives only in the httpOnly bkt_refresh
// cookie, out of reach of page script) and accepts that cookie at
// /auth/refresh. Being a custom header, it also forces a CORS preflight on any
// cross-origin attempt to use the cookie.
const CLIENT_HEADERS = { 'X-Bkt-Client': 'console' }

// Use relative URL to leverage Vite's proxy configuration
// The proxy will forward /api/* requests to the backend
const api = axios.create({
  baseURL: '/api',
  headers: CLIENT_HEADERS,
})

// ---------------------------------------------------------------------------
// Session bridge. The auth store (store/authStore.ts) is the single source of
// truth for the access token; it registers these accessors at load time so
// this module never touches token storage itself (and needs no circular
// import). The refresh token is never visible to script: it is the httpOnly
// bkt_refresh cookie, which the browser attaches to /api/auth/* requests.
// ---------------------------------------------------------------------------

export interface AuthSessionBridge {
  /** Access token this tab currently holds in memory. */
  current(): string | null
  /** Access token as persisted in localStorage — newer than memory if another tab just refreshed. */
  persisted(): string | null
  /** Store a renewed access token; must persist synchronously before returning. */
  update(token: string): void
  /** Drop the local session without calling the server (it is already invalid). */
  expire(): void
}

let session: AuthSessionBridge | null = null

export function registerAuthSession(bridge: AuthSessionBridge): void {
  session = bridge
}

// Bare client for /auth/refresh: no interceptors, so a failed refresh can
// never recurse into another refresh.
const refreshClient = axios.create({ baseURL: '/api', headers: CLIENT_HEADERS })

const REFRESH_LOCK = 'bkt-auth-refresh'
let inflightRefresh: Promise<string> | null = null

const statusOf = (err: unknown): number | undefined =>
  axios.isAxiosError(err) ? err.response?.status : undefined

// Exchanges the refresh cookie for a new access token. Empty body: the
// browser sends the cookie (same-origin; withCredentials for good measure),
// and the response's Set-Cookie carries the rotated refresh token.
async function exchangeRefreshCookie(): Promise<string> {
  const { data } = await refreshClient.post<{ token: string }>('/auth/refresh', undefined, {
    withCredentials: true,
  })
  if (!data?.token) {
    throw new Error('Malformed refresh response')
  }
  return data.token
}

// A token another tab stored that we can use instead of refreshing ourselves.
const adoptable = (candidate: string | null, stale: string | null): candidate is string =>
  !!candidate && candidate !== stale && !isJwtExpired(candidate, 5_000)

// Runs under the cross-tab lock. `staleToken` is the access token the caller
// found wanting (the one a request was rejected with, or the one about to
// expire).
async function performRefresh(staleToken: string | null): Promise<string> {
  if (!session) throw new Error('Auth session not initialised')

  // Read the freshest token: another tab may have refreshed while we waited
  // for the lock (its storage event may not have reached this tab yet).
  const latest = session.persisted() ?? session.current()
  if (!latest) throw new Error('Not signed in')

  // Someone already replaced the stale token — adopt theirs instead of
  // rotating again. All tabs share one cookie, so a second rotation right
  // behind theirs is wasted at best.
  if (adoptable(latest, staleToken)) {
    session.update(latest)
    return latest
  }

  try {
    const token = await exchangeRefreshCookie()
    session.update(token) // persist before releasing the lock
    return token
  } catch (err) {
    // Without Web Locks another tab can still race us and rotate the cookie
    // under our feet. If it has already stored a fresh access token, use it;
    // otherwise try exactly once more — the browser now sends the latest
    // cookie.
    if (statusOf(err) === 401) {
      const newer = session.persisted()
      if (adoptable(newer, latest)) {
        session.update(newer)
        return newer
      }
      const token = await exchangeRefreshCookie()
      session.update(token)
      return token
    }
    throw err
  }
}

/**
 * Obtain a fresh access token. Single-flight within the tab (concurrent
 * callers share one request) and serialised across tabs, so the same refresh
 * cookie is never presented twice (the server treats a replayed, already
 * rotated refresh token as theft). Rejects if the session can't be renewed.
 */
export function refreshAccessToken(staleToken: string | null): Promise<string> {
  if (!inflightRefresh) {
    inflightRefresh = withCrossTabLock(REFRESH_LOCK, () => performRefresh(staleToken)).finally(() => {
      inflightRefresh = null
    })
  }
  return inflightRefresh
}

/** Refresh failed for good: drop the session and go to the sign-in page. */
export function handleSessionExpired(): void {
  session?.expire()
  const path = window.location.pathname
  if (!path.includes('/login') && !path.includes('/register') && !path.includes('/callback')) {
    window.location.href = '/login'
  }
}

// Every /auth/* endpoint is either public (login, register, refresh, SSO) or
// the logout call itself: a 401 there must never trigger a refresh.
const isAuthEndpoint = (url: string | undefined): boolean =>
  !!url && /^\/?auth\//.test(url.replace(/^\/api\//, ''))

const bearerOf = (config: InternalAxiosRequestConfig): string | null => {
  const header = config.headers?.Authorization
  return typeof header === 'string' && header.startsWith('Bearer ') ? header.slice(7) : null
}

type RetriableRequest = InternalAxiosRequestConfig & { _authRetried?: boolean }

declare module 'axios' {
  interface AxiosRequestConfig {
    /** Carries its own credentials: a 401 is returned to the caller as-is (no refresh, no logout). */
    skipAuthRefresh?: boolean
  }
}

// Request interceptor to add auth token and set Content-Type
api.interceptors.request.use((config) => {
  // A caller may pass an explicit token (SSO callback, before the session is
  // stored); otherwise use the store's.
  const token = session?.current()
  if (token && !config.headers.Authorization) {
    config.headers.Authorization = `Bearer ${token}`
  }

  // Set Content-Type to application/json for non-FormData requests
  // For FormData, axios will automatically set multipart/form-data with boundary
  if (!(config.data instanceof FormData)) {
    config.headers['Content-Type'] = 'application/json'
  }

  return config
})

// Response interceptor: on a 401 from an authenticated call, renew the access
// token once (silently) and replay the request; log out only if that fails.
api.interceptors.response.use(
  (response) => response,
  async (error: unknown) => {
    if (!axios.isAxiosError(error) || error.response?.status !== 401 || !error.config) {
      return Promise.reject(error)
    }
    const config = error.config as RetriableRequest
    if (config.skipAuthRefresh || isAuthEndpoint(config.url)) {
      return Promise.reject(error)
    }

    const usedToken = bearerOf(config)
    if (config._authRetried || !usedToken) {
      // Already replayed with a fresh token, or we weren't signed in at all.
      handleSessionExpired()
      return Promise.reject(error)
    }
    config._authRetried = true

    try {
      // A refresh may already have landed while this request was in flight:
      // then just replay with the current token.
      const current = session?.current()
      if (!current || current === usedToken || isJwtExpired(current, 5_000)) {
        await refreshAccessToken(usedToken)
      }
    } catch (refreshError) {
      console.warn('Session refresh failed:', refreshError)
      handleSessionExpired()
      return Promise.reject(error)
    }

    const fresh = session?.current()
    if (fresh) {
      config.headers.Authorization = `Bearer ${fresh}`
    }
    return api(config)
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

  // Revokes the access token and the refresh token server-side (the latter
  // arrives as the bkt_refresh cookie), and makes the server clear the cookie.
  logout: async (): Promise<void> => {
    await api.post('/auth/logout', {}, { withCredentials: true })
  },
}

// User API
export const userApi = {
  // `accessToken` overrides the stored session token (used by the SSO
  // callback to look the user up before committing the session).
  getCurrentUser: async (accessToken?: string): Promise<User> => {
    const { data } = await api.get<User>('/users/me', accessToken
      ? { headers: { Authorization: `Bearer ${accessToken}` }, skipAuthRefresh: true }
      : undefined)
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

  presignObject: async (bucketName: string, key: string, expiresIn: number): Promise<{ url: string; expires_at: string; capped_by_key: boolean; signing_key_name?: string; endpoint_derived?: boolean }> => {
    const { data } = await api.post<{ url: string; expires_at: string; capped_by_key: boolean; signing_key_name?: string; endpoint_derived?: boolean }>(`/buckets/${bucketName}/objects/presign`, {
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
