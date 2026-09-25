import { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Database, AlertCircle } from 'lucide-react';
import { useAuthStore } from '../store/authStore';
import { userApi } from '../services/api';
import { consumeSSOPending } from '../services/sso';

// Friendlier copy for the denial codes the backend's OIDC flow can emit.
const ERROR_HINTS: Record<string, string> = {
  access_denied_no_groups: 'Your account did not include group membership. Ask an administrator to add the groups claim to the identity provider token.',
  access_denied_group: 'Your account is not in a group that grants access to bkt. Ask an administrator to add you to the appropriate group.',
  invalid_state: 'The sign-in attempt expired or was tampered with. Please try again.',
  missing_verifier: 'The sign-in attempt expired. Please try again.',
  missing_nonce: 'The sign-in attempt expired. Please try again.',
  account_locked: 'This account is locked. Contact an administrator.',
};

/**
 * Shared landing page for every browser-based SSO provider. The backend
 * completes the provider exchange and redirects here with either
 * #token=…&refresh_token=… or #error=…&error_description=… in the fragment.
 */
export default function SSOCallback({ provider }: { provider: string }) {
  const navigate = useNavigate();
  const { setAuth } = useAuthStore();
  const [error, setError] = useState('');
  const [processing, setProcessing] = useState(true);
  const handled = useRef(false);

  useEffect(() => {
    // Run once per page load: the fragment and the SSO marker are single-use.
    if (handled.current) return;
    handled.current = true;

    const handleCallback = async () => {
      // Get data from URL fragment (hash)
      const hash = window.location.hash.substring(1); // Remove the #
      const params = new URLSearchParams(hash);

      // Remove tokens from the address bar / history right away, whatever
      // happens next.
      window.history.replaceState(null, '', window.location.pathname);

      // Login-CSRF guard: only accept a callback for an SSO login that THIS
      // tab started (see consumeSSOPending). Consumed on every path.
      const startedHere = consumeSSOPending();

      // Check for error from backend
      const errorCode = params.get('error');
      const errorDesc = params.get('error_description');
      if (errorCode) {
        setError(ERROR_HINTS[errorCode] || errorDesc || errorCode || 'Authentication failed');
        setProcessing(false);
        return;
      }

      const token = params.get('token');
      const refreshToken = params.get('refresh_token');

      if (!token || !refreshToken) {
        setError('Authentication failed - missing tokens');
        setProcessing(false);
        return;
      }

      if (!startedHere) {
        // Tokens we did not ask for (e.g. a crafted link): discard them.
        setError('This sign-in was not started from this browser tab, or it took longer than 10 minutes. For your safety it was ignored — please sign in again.');
        setProcessing(false);
        return;
      }

      try {
        // Temporarily store token so we can make authenticated API call.
        // The refresh token is intentionally NOT persisted (see authStore) —
        // nothing reads it back, so storing it would only widen exposure.
        localStorage.setItem('token', token);

        // Fetch user info
        const user = await userApi.getCurrentUser();

        // Update auth store with full auth data
        setAuth({
          token,
          refresh_token: refreshToken,
          user
        });

        // Redirect to home
        navigate('/');
      } catch (err: any) {
        console.error('Failed to fetch user info:', err);
        // Clear invalid tokens
        localStorage.removeItem('token');
        localStorage.removeItem('refresh_token');
        setError('Failed to complete authentication');
        setProcessing(false);
      }
    };

    handleCallback();
  }, [navigate, setAuth]);

  return (
    <div className="min-h-screen bg-dark-bg flex items-center justify-center p-4">
      <div className="w-full max-w-md">
        <div className="flex flex-col items-center text-center mb-8">
          <span className="flex items-center justify-center w-12 h-12 rounded-xl bg-blue-600/15 mb-4">
            <Database className="w-6 h-6 text-blue-500" />
          </span>
          <h1 className="text-2xl font-semibold text-dark-text tracking-tight">bkt</h1>
        </div>

        <div className="card p-8">
          {processing ? (
            <div className="flex flex-col items-center text-center gap-3">
              <div className="spinner w-8! h-8!" />
              <div>
                <p className="text-base font-semibold text-dark-text">Completing sign in...</p>
                <p className="text-sm text-dark-textSecondary mt-1">
                  Please wait while we authenticate you with {provider}
                </p>
              </div>
            </div>
          ) : (
            <div className="space-y-5">
              <div className="alert-error">
                <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                <div>
                  <p className="font-medium">Authentication failed</p>
                  <p className="mt-0.5">{error}</p>
                </div>
              </div>
              <button
                onClick={() => navigate('/login')}
                className="btn-secondary w-full"
              >
                Back to sign in
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
