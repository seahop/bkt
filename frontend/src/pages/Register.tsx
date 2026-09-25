import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useAuthStore } from '../store/authStore'
import { Database } from 'lucide-react'
import { getSSOConfig } from '../services/sso'
import { getErrorMessage, getErrorStatus } from '../utils/errors'

const REGISTRATION_DISABLED_NOTICE =
  'Self-service registration is disabled on this server. Ask an administrator for an account.'

// Mirrors the backend's RegisterRequest binding (username 3-50 chars, valid
// email, password >= 8) plus bcrypt's 72-byte input limit.
const USERNAME_MIN = 3
const USERNAME_MAX = 50
const PASSWORD_MIN = 8
const PASSWORD_MAX_BYTES = 72

export default function Register() {
  const [username, setUsername] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  // null until the server says whether registration is open
  const [registrationOpen, setRegistrationOpen] = useState<boolean | null>(null)
  const { register } = useAuthStore()
  const navigate = useNavigate()

  const leaveBecauseDisabled = () =>
    navigate('/login', { replace: true, state: { notice: REGISTRATION_DISABLED_NOTICE } })

  useEffect(() => {
    let cancelled = false
    getSSOConfig()
      .then((config) => {
        if (cancelled) return
        if (config.allow_registration) {
          setRegistrationOpen(true)
        } else {
          navigate('/login', { replace: true, state: { notice: REGISTRATION_DISABLED_NOTICE } })
        }
      })
      .catch((err) => {
        // Can't tell: show the form; the server still enforces the setting.
        console.error('Failed to fetch server config:', err)
        if (!cancelled) setRegistrationOpen(true)
      })
    return () => {
      cancelled = true
    }
  }, [navigate])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')

    const name = username.trim()
    if (name.length < USERNAME_MIN || name.length > USERNAME_MAX) {
      setError(`Username must be ${USERNAME_MIN}-${USERNAME_MAX} characters`)
      return
    }

    if (password !== confirmPassword) {
      setError('Passwords do not match')
      return
    }

    if (password.length < PASSWORD_MIN) {
      setError(`Password must be at least ${PASSWORD_MIN} characters`)
      return
    }

    if (new TextEncoder().encode(password).length > PASSWORD_MAX_BYTES) {
      setError(`Password must be at most ${PASSWORD_MAX_BYTES} bytes`)
      return
    }

    setLoading(true)

    try {
      await register(name, email.trim(), password)
      navigate('/')
    } catch (err) {
      switch (getErrorStatus(err)) {
        case 403:
          leaveBecauseDisabled()
          return
        case 409:
          setError('That username or email is already taken. Choose another, or sign in.')
          break
        case 400:
          // The backend's binding errors are validator dumps; summarise.
          setError(
            `Please check your details: username ${USERNAME_MIN}-${USERNAME_MAX} characters, ` +
              `a valid email address, and a password of at least ${PASSWORD_MIN} characters.`
          )
          break
        default:
          setError(getErrorMessage(err, 'Registration failed'))
      }
    } finally {
      setLoading(false)
    }
  }

  if (registrationOpen === null) {
    return (
      <div className="min-h-screen bg-dark-bg flex items-center justify-center">
        <div className="spinner" />
      </div>
    )
  }

  return (
    <div className="relative min-h-screen bg-dark-bg flex items-center justify-center p-4 overflow-hidden">
      {/* Subtle ambient glow behind the card */}
      <div
        aria-hidden="true"
        className="pointer-events-none absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 w-xl h-144 bg-blue-600/10 blur-3xl rounded-full"
      />

      <div className="relative w-full max-w-md">
        <div className="flex flex-col items-center text-center mb-8">
          <span className="flex items-center justify-center w-12 h-12 rounded-xl bg-blue-600/15 mb-4">
            <Database className="w-6 h-6 text-blue-500" />
          </span>
          <h1 className="text-2xl font-semibold text-dark-text tracking-tight">bkt</h1>
          <p className="text-sm text-dark-textSecondary mt-1">Create your account</p>
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
                placeholder="Choose a username"
                required
                minLength={USERNAME_MIN}
                maxLength={USERNAME_MAX}
                autoComplete="username"
              />
            </div>

            <div>
              <label htmlFor="email" className="label">
                Email
              </label>
              <input
                id="email"
                type="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className="input"
                placeholder="Enter your email"
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
                placeholder="Choose a password"
                required
                minLength={PASSWORD_MIN}
                autoComplete="new-password"
              />
            </div>

            <div>
              <label htmlFor="confirmPassword" className="label">
                Confirm Password
              </label>
              <input
                id="confirmPassword"
                type="password"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                className="input"
                placeholder="Confirm your password"
                required
              />
            </div>

            <button
              type="submit"
              disabled={loading}
              className="btn-primary w-full py-2.5"
            >
              {loading && <span className="spinner w-4! h-4! border-white/30! border-t-white!" />}
              {loading ? 'Creating account...' : 'Sign Up'}
            </button>
          </form>

          <p className="mt-6 text-center text-xs text-dark-textMuted">
            Already have an account?{' '}
            <Link to="/login" className="text-blue-500 hover:text-blue-400 font-medium">
              Sign in
            </Link>
          </p>
        </div>
      </div>
    </div>
  )
}
