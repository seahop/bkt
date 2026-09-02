import { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuthStore } from '../store/authStore'
import { Database } from 'lucide-react'
import GoogleSignInButton from '../components/GoogleSignInButton'
import VaultLoginModal from '../components/VaultLoginModal'
import { getSSOConfig, SSOConfig, loginWithVaultOIDC } from '../services/sso'
import { getErrorMessage } from '../utils/errors'

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
      setError(getErrorMessage(err, 'Invalid credentials'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="relative min-h-screen bg-dark-bg flex items-center justify-center p-4 overflow-hidden">
      {/* Subtle ambient glow behind the card */}
      <div
        aria-hidden="true"
        className="pointer-events-none absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 w-[36rem] h-[36rem] bg-blue-600/10 blur-3xl rounded-full"
      />

      <div className="relative w-full max-w-md">
        <div className="flex flex-col items-center text-center mb-8">
          <span className="flex items-center justify-center w-12 h-12 rounded-xl bg-blue-600/15 mb-4">
            <Database className="w-6 h-6 text-blue-500" />
          </span>
          <h1 className="text-2xl font-semibold text-dark-text tracking-tight">bkt</h1>
          <p className="text-sm text-dark-textSecondary mt-1">
            Sign in to your object storage
          </p>
        </div>

        <div className="card p-8">
          <form onSubmit={handleSubmit} className="space-y-5">
            {error && <div className="alert-error">{error}</div>}

            <div>
              <label htmlFor="username" className="label">
                Username
              </label>
              <input
                id="username"
                type="text"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                className="input"
                placeholder="Enter your username"
                required
              />
            </div>

            <div>
              <label htmlFor="password" className="label">
                Password
              </label>
              <input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="input"
                placeholder="Enter your password"
                required
              />
            </div>

            <button
              type="submit"
              disabled={loading}
              className="btn-primary w-full py-2.5"
            >
              {loading && <span className="spinner !w-4 !h-4 !border-white/30 !border-t-white" />}
              {loading ? 'Signing in...' : 'Sign In'}
            </button>
          </form>

          {/* SSO Options */}
          {ssoConfig && (ssoConfig.google_enabled || ssoConfig.vault_enabled) && (
            <>
              <div className="flex items-center gap-3 my-6" aria-hidden="true">
                <div className="flex-1 border-t border-dark-border" />
                <span className="text-xs uppercase tracking-wider text-dark-textMuted">
                  or continue with
                </span>
                <div className="flex-1 border-t border-dark-border" />
              </div>

              <div className="space-y-3">
                {ssoConfig.google_enabled && (
                  <GoogleSignInButton disabled={loading} />
                )}

                {ssoConfig.vault_enabled && (
                  <button
                    onClick={() => {
                      // Use OIDC redirect if available, otherwise fall back to JWT modal
                      if (ssoConfig.vault_auth_url) {
                        loginWithVaultOIDC();
                      } else {
                        setVaultModalOpen(true);
                      }
                    }}
                    disabled={loading}
                    className="w-full inline-flex items-center justify-center gap-2.5 px-4 py-2.5 rounded-lg
                      border border-dark-border bg-dark-inset hover:bg-dark-surfaceHover
                      text-sm font-medium text-dark-text transition-colors duration-150
                      disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    <svg className="w-5 h-5 shrink-0" viewBox="0 0 24 24" fill="none">
                      <path d="M12 2L2 7V12C2 17.55 5.84 22.54 11 23.84C16.16 22.54 20 17.55 20 12V7L12 2Z" fill="#FFD814" stroke="#000" strokeWidth="1.5"/>
                      <path d="M12 7V12L15 14" stroke="#000" strokeWidth="1.5" strokeLinecap="round"/>
                    </svg>
                    <span>Sign in with Vault</span>
                  </button>
                )}
              </div>
            </>
          )}

          <p className="mt-6 text-center text-xs text-dark-textMuted">
            Contact your administrator for access
          </p>
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
