import { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuthStore } from '../store/authStore'
import { Database } from 'lucide-react'
import GoogleSignInButton from '../components/GoogleSignInButton'
import VaultLoginModal from '../components/VaultLoginModal'
import { getSSOConfig, SSOConfig } from '../services/sso'

export default function Login() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [ssoConfig, setSsoConfig] = useState<SSOConfig | null>(null)
  const [vaultModalOpen, setVaultModalOpen] = useState(false)
  const { login } = useAuthStore()
  const navigate = useNavigate()

  // Fetch SSO configuration on mount
  useEffect(() => {
    const fetchSSOConfig = async () => {
      try {
        const config = await getSSOConfig()
        setSsoConfig(config)
      } catch (err) {
        console.error('Failed to fetch SSO config:', err)
        // Gracefully degrade - SSO just won't be available
      }
    }
    fetchSSOConfig()
  }, [])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)

    try {
      await login(username, password)
      navigate('/')
    } catch (err: any) {
      setError(err.response?.data?.message || 'Invalid credentials')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen bg-dark-bg flex items-center justify-center p-4">
      <div className="w-full max-w-md">
        <div className="text-center mb-8">
          <div className="inline-flex items-center gap-2 mb-4">
            <Database className="w-12 h-12 text-blue-500" />
            <h1 className="text-3xl font-bold text-dark-text">bkt</h1>
          </div>
          <p className="text-dark-textSecondary">Sign in to your account</p>
        </div>

        <div className="bg-dark-surface rounded-lg p-8 border border-dark-border">
          <form onSubmit={handleSubmit} className="space-y-6">
            {error && (
              <div className="bg-red-500/10 border border-red-500 text-red-500 px-4 py-3 rounded-lg text-sm">
                {error}
              </div>
            )}

            <div>
              <label htmlFor="username" className="block text-sm font-medium text-dark-text mb-2">
                Username
              </label>
              <input
                id="username"
                type="text"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                className="w-full px-4 py-3 bg-dark-bg border border-dark-border rounded-lg text-dark-text placeholder-dark-textSecondary focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent"
                placeholder="Enter your username"
                required
              />
            </div>

            <div>
              <label htmlFor="password" className="block text-sm font-medium text-dark-text mb-2">
                Password
              </label>
              <input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full px-4 py-3 bg-dark-bg border border-dark-border rounded-lg text-dark-text placeholder-dark-textSecondary focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent"
                placeholder="Enter your password"
                required
              />
            </div>

            <button
              type="submit"
              disabled={loading}
              className="w-full bg-blue-600 hover:bg-blue-700 text-white font-medium py-3 px-4 rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {loading ? 'Signing in...' : 'Sign In'}
            </button>
          </form>

          {/* SSO Options */}
          {ssoConfig && (ssoConfig.google_enabled || ssoConfig.vault_enabled) && (
            <>
              <div className="relative my-6">
                <div className="absolute inset-0 flex items-center">
                  <div className="w-full border-t border-dark-border"></div>
                </div>
                <div className="relative flex justify-center text-sm">
                  <span className="px-2 bg-dark-surface text-dark-textSecondary">Or continue with</span>
                </div>
              </div>

              <div className="space-y-3">
                {ssoConfig.google_enabled && (
                  <GoogleSignInButton disabled={loading} />
                )}

                {ssoConfig.vault_enabled && (
                  <button
                    onClick={() => setVaultModalOpen(true)}
                    disabled={loading}
                    className="w-full flex items-center justify-center gap-3 px-4 py-2 border border-dark-border rounded-md shadow-sm bg-dark-bg hover:bg-dark-bg/80 text-dark-text focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-blue-500 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    <svg className="w-5 h-5" viewBox="0 0 24 24" fill="none">
                      <path d="M12 2L2 7V12C2 17.55 5.84 22.54 11 23.84C16.16 22.54 20 17.55 20 12V7L12 2Z" fill="#FFD814" stroke="#000" strokeWidth="1.5"/>
                      <path d="M12 7V12L15 14" stroke="#000" strokeWidth="1.5" strokeLinecap="round"/>
                    </svg>
                    <span className="text-sm font-medium">Sign in with Vault</span>
                  </button>
                )}
              </div>
            </>
          )}

          <div className="mt-6 text-center">
            <p className="text-dark-textSecondary text-sm">
              Contact your administrator for access
            </p>
          </div>
        </div>

        {/* Vault Login Modal */}
        <VaultLoginModal
          isOpen={vaultModalOpen}
          onClose={() => setVaultModalOpen(false)}
        />
      </div>
    </div>
  )
}
