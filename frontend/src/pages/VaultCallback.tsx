import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Database } from 'lucide-react';
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
        // Temporarily store token so we can make authenticated API call
        localStorage.setItem('token', token);
        localStorage.setItem('refresh_token', refreshToken);

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
        <div className="text-center mb-8">
          <div className="inline-flex items-center gap-2 mb-4">
            <Database className="w-12 h-12 text-blue-500" />
            <h1 className="text-3xl font-bold text-dark-text">bkt</h1>
          </div>
        </div>

        <div className="bg-dark-surface rounded-lg p-8 border border-dark-border text-center">
          {processing ? (
            <>
              <div className="mb-4">
                <svg
                  className="animate-spin h-12 w-12 mx-auto text-blue-500"
                  xmlns="http://www.w3.org/2000/svg"
                  fill="none"
                  viewBox="0 0 24 24"
                >
                  <circle
                    className="opacity-25"
                    cx="12"
                    cy="12"
                    r="10"
                    stroke="currentColor"
                    strokeWidth="4"
                  ></circle>
                  <path
                    className="opacity-75"
                    fill="currentColor"
                    d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                  ></path>
                </svg>
              </div>
              <p className="text-dark-text text-lg font-medium mb-2">Completing sign in...</p>
              <p className="text-dark-textSecondary text-sm">Please wait while we authenticate you with Vault</p>
            </>
          ) : (
            <>
              <div className="mb-4">
                <svg
                  className="h-12 w-12 mx-auto text-red-500"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                  />
                </svg>
              </div>
              <p className="text-dark-text text-lg font-medium mb-2">Authentication failed</p>
              <p className="text-dark-textSecondary text-sm mb-4">{error}</p>
              <p className="text-dark-textSecondary text-xs">Redirecting to login...</p>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
