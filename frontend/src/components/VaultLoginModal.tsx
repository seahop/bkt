import React, { useState } from 'react';
import { X } from 'lucide-react';
import { loginWithVault } from '../services/sso';
import { useNavigate } from 'react-router-dom';
import { getErrorMessage } from '../utils/errors';

interface VaultLoginModalProps {
  isOpen: boolean;
  onClose: () => void;
}

const VaultLoginModal: React.FC<VaultLoginModalProps> = ({ isOpen, onClose }) => {
  const [token, setToken] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const navigate = useNavigate();

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setLoading(true);

    try {
      const response = await loginWithVault(token);

      // Store the access token. We intentionally do NOT store a refresh token:
      // it is never used, and the old camelCase 'refreshToken' key was never
      // cleared on logout, leaving an orphaned secret behind.
      localStorage.setItem('token', response.token);

      // Close modal and redirect
      onClose();
      navigate('/');
    } catch (err: any) {
      console.error('Vault login error:', err);
      setError(getErrorMessage(err, 'Failed to login with Vault token'));
    } finally {
      setLoading(false);
    }
  };

  if (!isOpen) return null;

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal-panel !max-w-lg" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-5">
          <h2 className="modal-title">Sign in with Vault</h2>
          <button onClick={onClose} className="btn-icon">
            <span className="sr-only">Close</span>
            <X className="w-4 h-4" />
          </button>
        </div>

        <form onSubmit={handleSubmit}>
          <div>
            <label htmlFor="vault-token" className="label">
              Vault JWT Token
            </label>
            <textarea
              id="vault-token"
              rows={6}
              value={token}
              onChange={(e) => setToken(e.target.value)}
              className="input font-mono resize-none"
              placeholder="Paste your Vault JWT token here..."
              required
              autoComplete="off"
              spellCheck={false}
              data-lpignore="true"
              data-1p-ignore="true"
              style={{ WebkitTextSecurity: 'disc' } as React.CSSProperties}
            />
            <p className="help-text">
              Obtain this token from your Vault server using:{' '}
              <code className="kbd-mono">vault login -method=jwt</code>
            </p>
          </div>

          {error && <div className="alert-error mt-4">{error}</div>}

          <div className="flex justify-end gap-2 mt-6">
            <button type="button" onClick={onClose} className="btn-ghost">
              Cancel
            </button>
            <button
              type="submit"
              disabled={loading || !token.trim()}
              className="btn-primary"
            >
              {loading && <span className="spinner !w-4 !h-4 !border-white/30 !border-t-white" />}
              {loading ? 'Signing in...' : 'Sign in'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
};

export default VaultLoginModal;
