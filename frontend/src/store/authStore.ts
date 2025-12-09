import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import type { User, AuthResponse } from '../types'
import { authApi, userApi } from '../services/api'

interface AuthState {
  user: User | null
  token: string | null
  isAuthenticated: boolean
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

      setAuth: (data: AuthResponse) => {
        localStorage.setItem('token', data.token)
        if (data.refresh_token) {
          localStorage.setItem('refresh_token', data.refresh_token)
        }
        set({ user: data.user, token: data.token, isAuthenticated: true })
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
        set({ user: null, token: null, isAuthenticated: false })
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
          set({ user: null, token: null, isAuthenticated: false })
          return false
        }

        try {
          // Try to fetch current user to validate token
          const user = await userApi.getCurrentUser()
          set({ user })
          return true
        } catch (error) {
          // Token is invalid, clear everything
          console.error('Token validation failed:', error)
          localStorage.removeItem('token')
          localStorage.removeItem('refresh_token')
          localStorage.removeItem('auth-storage')
          set({ user: null, token: null, isAuthenticated: false })
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
