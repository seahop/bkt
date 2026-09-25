import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import type { User, AuthResponse } from '../types'
import {
  authApi,
  userApi,
  registerAuthSession,
  refreshAccessToken,
  handleSessionExpired,
} from '../services/api'
import { isJwtExpired, readJwtTimes } from '../utils/jwt'

// The store is the single source of truth for the session. It is persisted
// (zustand/persist, synchronously) under this localStorage key, which is also
// how tabs share it: see the 'storage' listener at the bottom.
//
// Only the short-lived access token is kept here. The refresh token is the
// httpOnly bkt_refresh cookie set by the backend: script (and therefore any
// XSS) can neither read nor exfiltrate it.
const STORAGE_KEY = 'auth-storage'
// v1: refreshToken is no longer persisted (dropped from older state in migrate).
const STORAGE_VERSION = 1
// Keys written by older builds that kept the access token outside the store.
const LEGACY_TOKEN_KEYS = ['token', 'refresh_token']

interface AuthState {
  user: User | null
  token: string | null
  isAuthenticated: boolean
  lastAuthTime: number | null  // Timestamp when auth was last set (to avoid redundant validation)
  login: (username: string, password: string) => Promise<void>
  register: (username: string, email: string, password: string) => Promise<void>
  logout: () => Promise<void>
  refreshUser: () => Promise<void>
  setAuth: (data: AuthResponse) => void
  /** Replace the access token (silent refresh) without touching the user. */
  setToken: (token: string) => void
  /** Forget the session locally, without calling the server. */
  clearSession: () => void
  validateToken: () => Promise<boolean>
}

/** Copy of a persisted state object without the legacy refreshToken field. */
function withoutRefreshToken(state: unknown): unknown {
  if (!state || typeof state !== 'object') return state
  const rest: Record<string, unknown> = { ...(state as Record<string, unknown>) }
  delete rest.refreshToken
  return rest
}

const SIGNED_OUT = {
  user: null,
  token: null,
  isAuthenticated: false,
  lastAuthTime: null,
} satisfies Partial<AuthState>

export const useAuthStore = create<AuthState>()(
  persist(
    (set, get) => ({
      ...SIGNED_OUT,

      setAuth: (data: AuthResponse) => {
        // Mark fresh authentication in sessionStorage (more reliable than zustand state for timing)
        sessionStorage.setItem('auth_timestamp', Date.now().toString())
        // data.refresh_token is ignored: the console's refresh token is the
        // httpOnly cookie (the backend omits it from console responses).
        set({
          user: data.user,
          token: data.token,
          isAuthenticated: true,
          lastAuthTime: Date.now(),
        })
      },

      setToken: (token: string) => {
        set({ token })
      },

      clearSession: () => {
        LEGACY_TOKEN_KEYS.forEach((k) => localStorage.removeItem(k))
        sessionStorage.removeItem('auth_timestamp')
        set(SIGNED_OUT)
      },

      login: async (username: string, password: string) => {
        const data = await authApi.login(username, password)
        useAuthStore.getState().setAuth(data)
      },

      register: async (username: string, email: string, password: string) => {
        const data = await authApi.register(username, email, password)
        useAuthStore.getState().setAuth(data)
      },

      logout: async () => {
        // Revoke server-side FIRST (access token + the refresh cookie, which
        // the server also clears), then forget locally. /auth/logout needs a
        // live access token, so renew an expired one first — otherwise the
        // refresh token would stay valid.
        const { token } = get()
        if (token) {
          try {
            if (isJwtExpired(token, 5_000)) {
              await refreshAccessToken(token)
            }
            await authApi.logout()
          } catch (error) {
            console.error('Logout error:', error)
          }
        }
        get().clearSession()
      },

      refreshUser: async () => {
        try {
          const user = await userApi.getCurrentUser()
          set({ user })
        } catch (error) {
          console.error('Failed to refresh user:', error)
          // If refresh fails, logout
          useAuthStore.getState().logout()
        }
      },

      validateToken: async () => {
        const state = useAuthStore.getState()

        // If no token or not authenticated, clear state
        if (!state.token || !state.isAuthenticated) {
          state.clearSession()
          return false
        }

        // Skip validation if auth was just set (within last 10 seconds)
        // This prevents redundant API calls after SSO callbacks
        // Check both zustand state and sessionStorage for reliability
        const authTimestamp = sessionStorage.getItem('auth_timestamp')
        const isRecentAuth = (state.lastAuthTime && Date.now() - state.lastAuthTime < 10000) ||
                            (authTimestamp && Date.now() - parseInt(authTimestamp, 10) < 10000)

        if (isRecentAuth) {
          return true
        }

        try {
          // Try to fetch current user to validate token
          const user = await userApi.getCurrentUser()
          // Only update state if user data actually changed (prevents unnecessary re-renders)
          const currentUser = useAuthStore.getState().user
          if (!currentUser || currentUser.id !== user.id || currentUser.username !== user.username ||
              currentUser.email !== user.email || currentUser.is_admin !== user.is_admin) {
            set({ user })
          }
          return true
        } catch (error) {
          // A 401 has already been through the silent refresh and, if that
          // failed, cleared the session (see services/api.ts). Other failures
          // (network, 5xx) are transient: keep the session rather than
          // signing the user out because the backend blinked.
          console.error('Token validation failed:', error)
          return false
        }
      },
    }),
    {
      name: STORAGE_KEY,
      version: STORAGE_VERSION,
      // Older builds persisted the refresh token here: drop it.
      migrate: (persisted) => withoutRefreshToken(persisted) as Pick<AuthState, 'user' | 'token' | 'isAuthenticated'>,
      partialize: (state) => ({
        user: state.user,
        token: state.token,
        isAuthenticated: state.isAuthenticated,
      }),
    }
  )
)

// ---------------------------------------------------------------------------
// Wiring: token access for the API client, cross-tab sync, proactive refresh.
// ---------------------------------------------------------------------------

type PersistedAuth = Pick<AuthState, 'user' | 'token' | 'isAuthenticated'>

function parsePersisted(raw: string | null): PersistedAuth | null {
  if (!raw) return null
  try {
    const state = (JSON.parse(raw) as { state?: Partial<PersistedAuth> } | null)?.state
    if (!state || state.isAuthenticated !== true || typeof state.token !== 'string') return null
    return {
      user: state.user ?? null,
      token: state.token,
      isAuthenticated: true,
    }
  } catch {
    return null
  }
}

function readPersisted(): PersistedAuth | null {
  try {
    return parsePersisted(localStorage.getItem(STORAGE_KEY))
  } catch {
    return null
  }
}

registerAuthSession({
  current: () => useAuthStore.getState().token,
  persisted: () => readPersisted()?.token ?? null,
  update: (token) => useAuthStore.getState().setToken(token),
  expire: () => useAuthStore.getState().clearSession(),
})

// Another tab signed in/out or refreshed the access token: mirror it here, so
// this tab uses the newest token instead of refreshing (and rotating the
// shared refresh cookie) again.
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (event) => {
    if (event.key !== STORAGE_KEY && event.key !== null) return // null: storage.clear()
    const next = parsePersisted(event.key === null ? null : event.newValue)
    const current = useAuthStore.getState()
    if (!next) {
      if (current.isAuthenticated || current.token) {
        sessionStorage.removeItem('auth_timestamp')
        useAuthStore.setState(SIGNED_OUT)
      }
      return
    }
    if (
      next.token !== current.token ||
      !current.isAuthenticated ||
      next.user?.id !== current.user?.id
    ) {
      useAuthStore.setState({
        user: next.user,
        token: next.token,
        isAuthenticated: true,
      })
    }
  })
}

// Proactive refresh: renew the access token shortly before it expires so
// active users don't hit the 401 → refresh → retry path at all. The reactive
// path in services/api.ts still covers sleeps, throttled timers and clock skew.
const MIN_REFRESH_DELAY_MS = 30_000 // also bounds the rate if the client clock is far off
let refreshTimer: ReturnType<typeof setTimeout> | undefined

function scheduleProactiveRefresh(token: string | null): void {
  clearTimeout(refreshTimer)
  refreshTimer = undefined
  const times = readJwtTimes(token)
  if (!token || !times) return

  const lifetime = times.issuedAt ? times.expiresAt - times.issuedAt : 15 * 60_000
  const lead = Math.min(60_000, lifetime / 5)
  const delay = Math.min(
    Math.max(times.expiresAt - lead - Date.now(), MIN_REFRESH_DELAY_MS),
    2 ** 31 - 1, // setTimeout's maximum
  )

  refreshTimer = setTimeout(() => {
    const state = useAuthStore.getState()
    if (!state.isAuthenticated || state.token !== token) return
    refreshAccessToken(token).catch((error: unknown) => {
      const status = (error as { response?: { status?: number } } | null)?.response?.status
      if (status === 401) {
        handleSessionExpired() // refresh token revoked/expired: the session is over
      }
      // Anything else (offline, 5xx): leave it to the reactive path.
    })
  }, delay)
}

useAuthStore.subscribe((state, prev) => {
  if (state.token !== prev.token || state.isAuthenticated !== prev.isAuthenticated) {
    scheduleProactiveRefresh(state.isAuthenticated ? state.token : null)
  }
})
scheduleProactiveRefresh(useAuthStore.getState().isAuthenticated ? useAuthStore.getState().token : null)

// Drop refresh tokens left behind by older builds: the loose keys, and the
// refreshToken field inside the persisted store (also removed by `migrate`;
// this catches a copy re-written by a tab still running an old build).
try {
  LEGACY_TOKEN_KEYS.forEach((k) => localStorage.removeItem(k))
  const raw = localStorage.getItem(STORAGE_KEY)
  if (raw && raw.includes('"refreshToken"')) {
    const parsed = JSON.parse(raw) as { state?: unknown; version?: number }
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ ...parsed, state: withoutRefreshToken(parsed.state) }))
  }
} catch {
  // storage unavailable or unparseable
}
