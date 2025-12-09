import { useEffect, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Database } from 'lucide-react';

export default function GoogleCallback() {
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const [error, setError] = useState('');
  const [processing, setProcessing] = useState(true);

  useEffect(() => {
    const handleCallback = async () => {
      // Check for error from Google
      const errorParam = searchParams.get('error');
      if (errorParam) {
        setError('Google authentication was cancelled or failed');
        setProcessing(false);
        setTimeout(() => navigate('/login'), 3000);
        return;
      }

      // Get authorization code
      const code = searchParams.get('code');
      const state = searchParams.get('state');

      if (!code || !state) {
        setError('Invalid callback parameters');
        setProcessing(false);
        setTimeout(() => navigate('/login'), 3000);
        return;
      }

      try {
        // The backend handles the full OAuth flow
        // Exchange code for token by calling the callback endpoint
        const response = await fetch(
          `${import.meta.env.VITE_API_URL}/auth/google/callback?code=${code}&state=${state}`,
          {
            method: 'GET',
            credentials: 'include', // Important for cookies
          }
        );

        if (!response.ok) {
          const data = await response.json();
          throw new Error(data.message || 'Failed to authenticate with Google');
        }

        const data = await response.json();

        // Store tokens
        localStorage.setItem('token', data.token);
        localStorage.setItem('refreshToken', data.refresh_token);

        // Redirect to home
        navigate('/');
      } catch (err: any) {
        console.error('Google callback error:', err);
        setError(err.message || 'Failed to complete Google authentication');
        setProcessing(false);
        setTimeout(() => navigate('/login'), 3000);
      }
    };

    handleCallback();
  }, [searchParams, navigate]);

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
              <p className="text-dark-textSecondary text-sm">Please wait while we authenticate you with Google</p>
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
