import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Database, AlertCircle } from 'lucide-react';
import { useAuthStore } from '../store/authStore';
import { userApi } from '../services/api';

export default function VaultCallback() {
  const navigate = useNavigate();
  const { setAuth } = useAuthStore();
  const [error, setError] = useState('');
  const [processing, setProcessing] = useState(true);

  useEffect(() => {
    const handleCallback = async () => {
      // Get data from URL fragment (hash)
      const hash = window.location.hash.substring(1); // Remove the #
      const params = new URLSearchParams(hash);

      // Check for error from backend
      const errorCode = params.get('error');
      const errorDesc = params.get('error_description');
      if (errorCode) {
        setError(errorDesc || errorCode || 'Authentication failed');
        setProcessing(false);
        setTimeout(() => navigate('/login'), 3000);
        return;
      }

      const token = params.get('token');
      const refreshToken = params.get('refresh_token');

      if (!token || !refreshToken) {
        setError('Authentication failed - missing tokens');
        setProcessing(false);
        setTimeout(() => navigate('/login'), 3000);
        return;
      }

      // Clear the hash from URL for security
      window.history.replaceState(null, '', window.location.pathname);

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
        setTimeout(() => navigate('/login'), 3000);
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
              <div className="spinner !w-8 !h-8" />
              <div>
                <p className="text-base font-semibold text-dark-text">Completing sign in...</p>
                <p className="text-sm text-dark-textSecondary mt-1">
                  Please wait while we authenticate you with Vault
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
              <p className="text-center text-xs text-dark-textMuted">
                Redirecting to login...
              </p>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
