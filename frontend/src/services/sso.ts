import api from './api';

export interface SSOConfig {
  google_enabled: boolean;
  google_auth_url?: string;
  vault_enabled: boolean;
}

export interface SSOLoginResponse {
  token: string;
  refresh_token: string;
  user: {
    id: string;
    username: string;
    email: string;
    is_admin: boolean;
  };
  is_new_user: boolean;
}

/**
 * Get SSO configuration - which SSO methods are enabled
 */
export const getSSOConfig = async (): Promise<SSOConfig> => {
  const response = await api.get<SSOConfig>('/auth/sso/config');
  return response.data;
};

/**
 * Initiate Google OAuth login - redirects to Google
 */
export const loginWithGoogle = (): void => {
  // Use relative URL that will go through the Vite proxy
  window.location.href = `/api/auth/google/login`;
};

/**
 * Login with Vault JWT token
 */
export const loginWithVault = async (token: string): Promise<SSOLoginResponse> => {
  const response = await api.post<SSOLoginResponse>('/auth/vault/login', { token });
  return response.data;
};
