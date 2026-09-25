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
 * completes the provider exchange and redirects here with either #token=…
 * (the access token) or #error=…&error_description=… in the fragment. The
 * refresh token never appears in the URL: the backend set it as the httpOnly
 * bkt_refresh cookie on the same response.
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

      if (!token) {
        setError('Authentication failed - missing token');
        setProcessing(false);
        return;
      }

      if (!startedHere) {
        // Tokens we did not ask for (e.g. a crafted link): discard them.
        // The marker lives in per-origin sessionStorage, so the common benign
        // cause is the SSO flow landing on a different origin than the one
        // the login was started from (FRONTEND_URL mismatch: localhost vs
        // 127.0.0.1, dev port 8443 vs 5173, http vs https).
        const origin = window.location.origin;
        console.warn(
          `[bkt] SSO callback rejected: no pending sign-in marker for ${origin}. ` +
            'If you just started a sign-in, the server\'s FRONTEND_URL probably points at a different origin ' +
            'than the one you started from — FRONTEND_URL must match the address you browse bkt at exactly ' +
            '(scheme, host and port).',
        );
        setError(
          `This sign-in could not be matched to a login started in this browser tab at ${origin}, ` +
            'or it took longer than 10 minutes, so for your safety it was ignored. ' +
            `If you did just click a sign-in button, you probably started on a different address than ${origin} ` +
            '(for example localhost vs 127.0.0.1, or another port): open bkt at the address configured as the ' +
            "server's FRONTEND_URL and sign in again there, or ask an administrator to set FRONTEND_URL to " +
            `${origin}.`,
        );
        setProcessing(false);
        return;
      }

      try {
        // Look the user up with the new access token directly; the session
        // is committed to the auth store only once that succeeds (the
        // refresh token is already in the httpOnly cookie).
        const user = await userApi.getCurrentUser(token);

        // Update auth store with full auth data
        setAuth({ token, user });

        // Redirect to home
        navigate('/');
      } catch (err) {
        console.error('Failed to fetch user info:', err);
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
