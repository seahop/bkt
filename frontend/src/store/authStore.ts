import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import type { User, AuthResponse } from '../types'
import { authApi, userApi } from '../services/api'

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
  validateToken: () => Promise<boolean>
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      user: null,
      token: null,
      isAuthenticated: false,
      lastAuthTime: null,

      setAuth: (data: AuthResponse) => {
        localStorage.setItem('token', data.token)
        if (data.refresh_token) {
          localStorage.setItem('refresh_token', data.refresh_token)
        }
        // Mark fresh authentication in sessionStorage (more reliable than zustand state for timing)
        sessionStorage.setItem('auth_timestamp', Date.now().toString())
        set({ user: data.user, token: data.token, isAuthenticated: true, lastAuthTime: Date.now() })
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
        try {
          await authApi.logout()
        } catch (error) {
          console.error('Logout error:', error)
        }
        // Clear all auth-related storage
        localStorage.removeItem('token')
        localStorage.removeItem('refresh_token')
        localStorage.removeItem('auth-storage') // Clear persisted Zustand state
        sessionStorage.removeItem('auth_timestamp')
        set({ user: null, token: null, isAuthenticated: false, lastAuthTime: null })
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
          localStorage.removeItem('token')
          localStorage.removeItem('refresh_token')
          localStorage.removeItem('auth-storage')
          sessionStorage.removeItem('auth_timestamp')
          set({ user: null, token: null, isAuthenticated: false, lastAuthTime: null })
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
          // Token is invalid, clear everything
          console.error('Token validation failed:', error)
          localStorage.removeItem('token')
          localStorage.removeItem('refresh_token')
          localStorage.removeItem('auth-storage')
          sessionStorage.removeItem('auth_timestamp')
          set({ user: null, token: null, isAuthenticated: false, lastAuthTime: null })
          return false
        }
      },
    }),
    {
      name: 'auth-storage',
      partialize: (state) => ({ user: state.user, token: state.token, isAuthenticated: state.isAuthenticated }),
    }
  )
)
