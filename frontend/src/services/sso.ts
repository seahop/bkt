import api from './api';
import type { User } from '../types';

export interface SSOConfig {
  google_enabled: boolean;
  google_auth_url?: string;
  vault_enabled: boolean;
  vault_auth_url?: string;
  /** Generic OIDC provider (OIDC_* settings): Keycloak, Okta, Entra ID, Authentik, ... */
  oidc_enabled: boolean;
  oidc_auth_url?: string;
  oidc_provider_name?: string;
  /** Self-service sign-up (ALLOW_REGISTRATION) is enabled on the server. */
  allow_registration?: boolean;
}

export interface SSOLoginResponse {
  token: string;
  /** Only for API clients; the console gets it as the httpOnly bkt_refresh cookie instead. */
  refresh_token?: string;
  /** The full user record (backend models.User), same shape as /auth/login. */
  user: User;
  is_new_user: boolean;
}

/**
 * Login-CSRF guard for the SSO callback. The backend finishes an SSO flow by
 * redirecting to /auth/<provider>/callback#token=…; without a check, anyone
 * could send a victim a link carrying the ATTACKER's tokens and silently log
 * the victim into the attacker's account (whatever they upload then lands
 * there). We record, per tab, that this browser actually started an SSO
 * login, and the callback accepts tokens only if that marker is present and
 * fresh.
 */
export const SSO_PENDING_KEY = 'sso_pending';
export const SSO_PENDING_MAX_AGE_MS = 10 * 60 * 1000;

const markSSOPending = (): void => {
  try {
    sessionStorage.setItem(SSO_PENDING_KEY, Date.now().toString());
  } catch {
    // Storage unavailable: the callback will refuse the tokens (fail closed).
  }
};

/**
 * Consumes the marker set by an SSO login button. Returns true only when this
 * tab started an SSO login within the last SSO_PENDING_MAX_AGE_MS. Always
 * clears the marker, so it authorizes exactly one callback.
 */
export const consumeSSOPending = (): boolean => {
  try {
    const raw = sessionStorage.getItem(SSO_PENDING_KEY);
    sessionStorage.removeItem(SSO_PENDING_KEY);
    if (!raw) return false;
    const startedAt = Number(raw);
    const age = Date.now() - startedAt;
    return Number.isFinite(startedAt) && age >= 0 && age < SSO_PENDING_MAX_AGE_MS;
  } catch {
    return false;
  }
};

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
  markSSOPending();
  // Use relative URL that will go through the Vite proxy
  window.location.href = `/api/auth/google/login`;
};

/**
 * Login with Vault JWT token (legacy method)
 */
export const loginWithVault = async (token: string): Promise<SSOLoginResponse> => {
  const response = await api.post<SSOLoginResponse>('/auth/vault/login', { token });
  return response.data;
};

/**
 * Initiate Vault OIDC login - redirects to Vault
 */
export const loginWithVaultOIDC = (): void => {
  markSSOPending();
  // Redirect to backend which will initiate OIDC flow with PKCE
  window.location.href = `/api/auth/vault/login`;
};

/**
 * Initiate generic OIDC login (authorization code + PKCE) - redirects to the IdP
 */
export const loginWithOIDC = (): void => {
  markSSOPending();
  window.location.href = `/api/auth/oidc/login`;
};
