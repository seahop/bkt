import { BrowserRouter as Router, Routes, Route, Navigate } from 'react-router-dom'
import { useEffect, useState } from 'react'
import { useAuthStore } from './store/authStore'
import Login from './pages/Login'
import GoogleCallback from './pages/GoogleCallback'
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

function App() {
  const { isAuthenticated, validateToken } = useAuthStore()
  const [isValidating, setIsValidating] = useState(true)

  useEffect(() => {
    // Validate token on app load
    const validate = async () => {
      if (isAuthenticated) {
        await validateToken()
      }
      setIsValidating(false)
    }
    validate()
  }, [])

  // Show loading state while validating token
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
        <Route path="/auth/google/callback" element={<GoogleCallback />} />

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
