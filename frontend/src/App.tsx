import { BrowserRouter as Router, Routes, Route, Navigate } from 'react-router-dom'
import { useEffect, useState } from 'react'
import { useAuthStore } from './store/authStore'
import Login from './pages/Login'
import Register from './pages/Register'
import SSOCallback from './pages/SSOCallback'
import Dashboard from './pages/Dashboard'
import Buckets from './pages/Buckets'
import BucketDetails from './pages/BucketDetails'
import Profile from './pages/Profile'
import Policies from './pages/Policies'
import AdminPanel from './pages/AdminPanel'
import S3Configurations from './pages/S3Configurations'
import Layout from './components/Layout'

function PrivateRoute({ children }: { children: React.ReactNode }) {
  const { isAuthenticated } = useAuthStore()
  return isAuthenticated ? <>{children}</> : <Navigate to="/login" />
}

function AdminRoute({ children }: { children: React.ReactNode }) {
  const { isAuthenticated, user } = useAuthStore()
  return isAuthenticated && user?.is_admin ? <>{children}</> : <Navigate to="/" />
}

// True if this tab authenticated within the last 30s (e.g. an SSO callback
// that already fetched the user), so re-validating would be redundant.
function isRecentAuth(): boolean {
  const authTimestamp = sessionStorage.getItem('auth_timestamp')
  return !!authTimestamp && Date.now() - parseInt(authTimestamp, 10) < 30000
}

function App() {
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated)
  const validateToken = useAuthStore((s) => s.validateToken)
  // Decided once, at load: a persisted (possibly stale) session is validated
  // against the backend before the app renders.
  const [isValidating, setIsValidating] = useState(() => isAuthenticated && !isRecentAuth())

  useEffect(() => {
    if (!isValidating) return
    let cancelled = false
    validateToken().finally(() => {
      if (!cancelled) setIsValidating(false)
    })
    return () => {
      cancelled = true
    }
  }, [isValidating, validateToken])

  // Show loading state while validating token (only for stale sessions)
  if (isValidating && isAuthenticated) {
    return (
      <div className="flex items-center justify-center h-screen bg-dark-bg">
        <div className="text-dark-textSecondary">Validating session...</div>
      </div>
    )
  }

  return (
    <Router>
      <Routes>
        <Route path="/login" element={isAuthenticated ? <Navigate to="/" /> : <Login />} />
        <Route path="/register" element={isAuthenticated ? <Navigate to="/" /> : <Register />} />
        <Route path="/auth/google/callback" element={<SSOCallback provider="Google" />} />
        <Route path="/auth/vault/callback" element={<SSOCallback provider="Vault" />} />
        <Route path="/auth/oidc/callback" element={<SSOCallback provider="your identity provider" />} />

        <Route
          path="/"
          element={
            <PrivateRoute>
              <Layout />
            </PrivateRoute>
          }
        >
          <Route index element={<Dashboard />} />
          <Route path="buckets" element={<Buckets />} />
          <Route path="buckets/:bucketName" element={<BucketDetails />} />
          <Route path="profile" element={<Profile />} />
          <Route
            path="policies"
            element={
              <AdminRoute>
                <Policies />
              </AdminRoute>
            }
          />
          <Route
            path="admin"
            element={
              <AdminRoute>
                <AdminPanel />
              </AdminRoute>
            }
          />
          <Route
            path="s3-configs"
            element={
              <AdminRoute>
                <S3Configurations />
              </AdminRoute>
            }
          />
        </Route>
      </Routes>
    </Router>
  )
}

export default App
