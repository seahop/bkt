# Authentication API

The authentication API handles user registration, login, token refresh, and logout operations.

## Base URL

```
https://localhost:9443/api/auth
```

## Console sessions vs. API clients

The same endpoints serve two kinds of clients, told apart by one request header:

| | Web console | API / script clients |
|---|---|---|
| Marker | sends `X-Bkt-Client: console` on every request | no `X-Bkt-Client` header |
| Access token | JSON body (`token`) | JSON body (`token`) |
| Refresh token | **only** in the `bkt_refresh` cookie; never in a JSON body or redirect URL | JSON body (`refresh_token`), as before |
| Refresh | `POST /auth/refresh` with an empty body; the browser sends the cookie | `POST /auth/refresh` with `{"refresh_token": "…"}` |

Every successful login, registration, token refresh and SSO completion (OIDC, Google, Vault OIDC callback, Vault JWT login) sets the refresh token in the `bkt_refresh` cookie:

```
Set-Cookie: bkt_refresh=<refresh token>; Path=/api/auth; Max-Age=<REFRESH_TOKEN_EXPIRY in seconds>; HttpOnly; Secure; SameSite=Strict
```

- `HttpOnly`: page script cannot read it, so an XSS bug cannot exfiltrate the long-lived credential (the short-lived access token is still held by the console).
- `SameSite=Strict`, `Path=/api/auth`, no `Domain` (host-only): only sent on same-site requests to the auth endpoints.
- `Secure` whenever `TLS_ENABLED=true` or `TLS_TERMINATED_UPSTREAM=true` (or the request arrived over TLS).
- Refreshing **with the cookie** additionally requires the `X-Bkt-Client: console` header (`403` otherwise). A cross-origin page can only send that custom header after a CORS preflight, which the `CORS_ALLOWED_ORIGINS` allowlist refuses — defence in depth on top of `SameSite=Strict`.
- Logout revokes the cookie's refresh token and always clears the cookie (`Max-Age=0`).

API clients can ignore the cookie; nothing changed for them.

## Endpoints
### Register New User

Create a new user account.

**Endpoint:** `POST /auth/register`

**Authentication:** None required

**⚠️ IMPORTANT:** Registration is **disabled by default** (`ALLOW_REGISTRATION=false`).

When disabled, this endpoint returns:
```json
{
  "error": "Registration disabled",
  "message": "Public registration is disabled. Please contact an administrator."
}
```

**To enable registration** (not recommended for production):
- Set `ALLOW_REGISTRATION=true` in `.env`
- Restart the backend service

**Request Body:**
```json
{
  "username": "string",      // 3-50 characters, required
  "email": "string",          // Valid email, required
  "password": "string"        // 8-72 bytes (bcrypt limit), required
}
```

**Success Response (201 Created):** *(only when registration is enabled; `refresh_token` is omitted for the console — see [Console sessions](#console-sessions-vs-api-clients))*
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "refresh_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "user": {
    "id": "uuid",
    "username": "string",
    "email": "string",
    "is_admin": false,
    "created_at": "2025-12-08T21:27:16Z",
    "updated_at": "2025-12-08T21:27:16Z"
  }
}
```

**Error Responses:**
- `403 Forbidden` - Registration is disabled (default)
- `400 Bad Request` - Invalid input, or a password longer than 72 bytes (`"Password too long"`; bcrypt's limit — multi-byte characters count several bytes). The same limit applies to `POST /api/users` and to password changes via `PUT /api/users/me`.
- `409 Conflict` - Username or email already exists

**Example:**
```bash
curl -k -X POST https://localhost:9443/api/auth/register \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "johndoe",
    "email": "john@example.com",
    "password": "SecurePass123"
  }'
```

**Recommended Approach:**

Instead of enabling public registration, **admins should create users** via the Users API:

```bash
# Admin creates a new user
curl -k -X POST https://localhost:9443/api/users \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "johndoe",
    "email": "john@example.com",
    "password": "SecurePass123",
    "is_admin": false
  }'
```

See [User Management](#) for more details.

---



### Login

Authenticate and receive access tokens.

**Endpoint:** `POST /auth/login`

**Authentication:** None required

**Request Body:**
```json
{
  "username": "string",
  "password": "string"
}
```

**Success Response (200 OK):** *(the refresh token is also set in the `bkt_refresh` cookie; with `X-Bkt-Client: console`, `refresh_token` is omitted from the body)*
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "refresh_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "user": {
    "id": "uuid",
    "username": "string",
    "email": "string",
    "is_admin": false,
    "created_at": "timestamp",
    "updated_at": "timestamp"
  }
}
```

**Token Expiration:**
- **Access Token:** 15 minutes
- **Refresh Token:** 7 days

**Error Responses:**
- `400 Bad Request` - Invalid request format
- `401 Unauthorized` - Invalid credentials
- `429 Too Many Requests` - Too many failed attempts. Lockout is per **username + client IP** (10 failures in 15 minutes locks that source for 15 minutes, without checking the password), so failures from one address cannot lock the account for everyone. Beyond 100 failures per username across all addresses a per-username throttle (exponential backoff, max 1 minute) activates. The throttle never blocks the **correct** password: the password is still checked, a correct one logs in and a wrong one gets `429`; while it is active each source may fail only 3 times before its (username, IP) pair is hard-locked. An address the user has successfully logged in from during the last 30 days is exempt from the throttle. A successful login does not reset the per-username counter (it ages out after 15 minutes without failures).
- `400 Bad Request` - `username` longer than 255 or `password` longer than 1024 characters, or a request body over 64 KiB (applies to login, register, refresh and logout).

**Example:**
```bash
curl -k -X POST https://localhost:9443/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "johndoe",
    "password": "SecurePass123"
  }'
```

---

### Refresh Token

Get a new token pair using a refresh token.

**Endpoint:** `POST /auth/refresh`

**Authentication:** None required — the refresh token comes from the JSON body (API clients) or, when the body has none, from the `bkt_refresh` cookie (web console; requires the `X-Bkt-Client: console` header).

**Request Body (API clients):**
```json
{
  "refresh_token": "string"
}
```

**Success Response (200 OK):**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "refresh_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
}
```
With `X-Bkt-Client: console` the body is just `{"token": "…"}`; the rotated refresh token arrives only in the `Set-Cookie: bkt_refresh=…` header (it is set for every successful refresh).

**Rotation:** every successful refresh **rotates** the refresh token — the token you sent is revoked and the response contains a new pair. Always replace your stored refresh token with the one from the response (the console's cookie is replaced by the browser automatically).

**Reuse detection:** replaying a refresh token that was already rotated is treated as a theft indicator (per the OAuth security BCP): **all of that user's sessions are revoked** and the event is logged to the audit trail as `auth.refresh_reuse`. Replaying a token that was revoked by logout is simply rejected without revoking other sessions.

**Error Responses:**
- `400 Bad Request` - No refresh token (neither body nor cookie), or a malformed body
- `401 Unauthorized` - Expired, invalid, rotated, or revoked refresh token
- `403 Forbidden` - Refresh token taken from the cookie without the `X-Bkt-Client: console` header

**Example:**
```bash
curl -k -X POST https://localhost:9443/api/auth/refresh \
  -H 'Content-Type: application/json' \
  -d '{
    "refresh_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
  }'
```

---

### Logout

Invalidate the current session. Logout revokes **both tokens**: the access token you present, and its sibling refresh token (whose ID is embedded in the access token, so the pair is revoked even when the client never sends the refresh token). It also revokes a refresh token passed in the body and the one in the `bkt_refresh` cookie (the console's — after rotations it may no longer be the access token's sibling), and always clears that cookie (`Set-Cookie: bkt_refresh=; Path=/api/auth; Max-Age=0`).

**Endpoint:** `POST /auth/logout`

**Authentication:** Required (Bearer token)

**Headers:**
```
Authorization: Bearer <access_token>
```

**Request Body (optional):**
```json
{
  "refresh_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
}
```

**Success Response (200 OK):**
```json
{
  "message": "Successfully logged out"
}
```

**Error Responses:**
- `401 Unauthorized` - Invalid or expired token

**Example:**
```bash
curl -k -X POST https://localhost:9443/api/auth/logout \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...'
```

---

## Using Access Tokens

All authenticated endpoints require the access token in the `Authorization` header:

```bash
curl -k -X GET https://localhost:9443/api/users/me \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...'
```

## Token Management Best Practices

1. **Store Securely**
   - Keep long-lived credentials out of browser script storage (localStorage is readable by any XSS). The bkt console follows this: its refresh token is the httpOnly `bkt_refresh` cookie, and only the 15-minute access token is kept in localStorage
   - Use httpOnly cookies or secure storage mechanisms
   - Never commit tokens to version control

2. **Handle Expiration**
   - Implement automatic token refresh before expiration
   - Handle 401 responses by refreshing tokens
   - Clear tokens on logout

3. **Refresh Strategy**
   - Refresh tokens proactively (e.g., at 14 min for 15 min tokens)
   - Store refresh token securely and **replace it after every refresh** (tokens rotate; reusing an old one revokes all sessions)
   - Never retry a refresh with the same token after a success — that replay trips reuse detection
   - Implement exponential backoff on refresh failures

## Example Token Refresh Flow

For an API client holding the refresh token itself (a browser app on the console's origin would instead send `X-Bkt-Client: console` and an empty body, and let the cookie carry the token):

```javascript
async function apiRequest(url, options = {}) {
  let token = getAccessToken();

  // Try request with current token
  let response = await fetch(url, {
    ...options,
    headers: {
      ...options.headers,
      'Authorization': `Bearer ${token}`
    }
  });

  // If unauthorized, try refreshing token
  if (response.status === 401) {
    const refreshToken = getRefreshToken();
    const refreshResponse = await fetch('/api/auth/refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: refreshToken })
    });

    if (refreshResponse.ok) {
      const { token: newToken, refresh_token: newRefresh } = await refreshResponse.json();
      setAccessToken(newToken);
      setRefreshToken(newRefresh); // tokens rotate: the old one is now revoked

      // Retry original request
      response = await fetch(url, {
        ...options,
        headers: {
          ...options.headers,
          'Authorization': `Bearer ${newToken}`
        }
      });
    } else {
      // Refresh failed, redirect to login
      redirectToLogin();
    }
  }

  return response;
}
```

## Security Considerations

- All authentication endpoints use HTTPS
- Passwords are hashed with bcrypt (cost factor 12)
- JWT tokens are signed with HS256
- Tokens include user ID, username, and admin status
- Refresh tokens have longer expiration for better UX, rotate on every use, and carry reuse detection (replay of a rotated token revokes all sessions)
- Logout revokes both the access and refresh token (and clears the console's `bkt_refresh` cookie)
- The web console never exposes its refresh token to JavaScript: it lives in an httpOnly, `SameSite=Strict` cookie scoped to `/api/auth`
- Login attempts are rate-limited per IP (`AUTH_RATE_LIMIT`, default 5/min)
- All logins — including SSO logins, with provider metadata — are recorded in the audit log

---

## Single Sign-On (SSO)

### SSO Configuration

**Endpoint:** `GET /auth/sso/config`

**Authentication:** None required

Check which SSO providers are enabled:

```bash
curl -k https://localhost:9443/api/auth/sso/config
```

**Response:**
```json
{
  "google_enabled": true,
  "google_auth_url": "https://accounts.google.com/o/oauth2/v2/auth?...",
  "vault_enabled": true
}
```

---

### Vault JWT Login

**Endpoint:** `POST /auth/vault/login`

**Authentication:** None required (JWT in body)

Login using a JWT token from HashiCorp Vault with automatic policy sync.

**Request Body:**
```json
{
  "token": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9..."
}
```

**JWT Claims for Policy Sync:**

Your JWT can include a `policies` claim with an array of policy names:

```json
{
  "sub": "user-12345",
  "email": "alice@company.com",
  "name": "Alice Smith",
  "policies": ["team-engineering-access", "project-x-readonly"]
}
```

**Response (200 OK):** *(the refresh token is also set in the `bkt_refresh` cookie; with `X-Bkt-Client: console`, `refresh_token` is omitted from the body)*
```json
{
  "token": "eyJhbGciOiJIUzI1NiIs...",
  "refresh_token": "eyJhbGciOiJIUzI1NiIs...",
  "user": {
    "id": "uuid",
    "username": "alice",
    "email": "alice@company.com",
    "is_admin": false,
    "sso_provider": "vault"
  }
}
```

**Policy Sync Rules:**
- Policy names must match exactly (case-sensitive)
- Unknown policies are silently ignored
- SSO is the source of truth - policies sync on every login
- Changes in SSO propagate immediately on next login

**Example:**
```bash
curl -k -X POST https://localhost:9443/api/auth/vault/login \
  -H 'Content-Type: application/json' \
  -d '{"token": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9..."}'
```

> See [SSO Setup Guide](../guides/sso-setup.md) for complete Vault configuration.

---

### OIDC (Browser SSO)

**Endpoints:** `GET /auth/oidc/login` (initiate) and `GET /auth/oidc/callback`

Standards-based OpenID Connect **authorization code flow with PKCE (S256)**
against any compliant identity provider (Keycloak, Okta, Entra ID, Auth0,
Authentik, Kanidm, Vault, …). Endpoints are discovered from
`OIDC_ISSUER_URL/.well-known/openid-configuration`; the ID token is verified
against the provider's JWKS (RS256/384/512, ES256/384/512), with issuer,
audience, expiry and nonce checks. Profile and group claims are merged from the
ID token and the UserInfo endpoint.

**Configuration:**

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `OIDC_ISSUER_URL` | — | Issuer URL (discovery document is fetched from here) |
| `OIDC_CLIENT_ID` | — | OIDC client ID |
| `OIDC_CLIENT_SECRET` | — | Optional client secret (confidential client). PKCE is used either way |
| `OIDC_REDIRECT_URL` | `https://localhost:9443/api/auth/oidc/callback` | Backend callback registered at the IdP |
| `OIDC_SCOPES` | `openid profile email` | Requested scopes (`openid` is always added) |
| `OIDC_PROVIDER_NAME` | `SSO` | Login button label |
| `OIDC_ENABLED` | implied | `false` keeps a configured provider off |
| `OIDC_USERNAME_CLAIM` | — | Claim for the username at first login (default `preferred_username`, then email local part) |
| `OIDC_GROUPS_CLAIM` | `groups` | Claim carrying group names |
| `OIDC_ADMIN_GROUP` | — | Members become admins; re-evaluated on every login |
| `OIDC_USER_GROUP` | — | If set, non-admins must be members or login is denied |
| `OIDC_POLICIES_CLAIM` | `policies` | Claim listing bkt policy names to sync (a present claim — even empty — replaces the user's policies) |
| `OIDC_POLICIES_AUTHORITATIVE` | `true` if `OIDC_POLICIES_CLAIM` set | A missing policies claim also clears policies |
| `OIDC_LINK_BY_EMAIL` | `false` | Link new subjects to an existing OIDC account whose IdP-asserted (`sso_email`) verified address matches exactly one account; re-linking revokes that account's sessions and access keys |

The discovery `issuer` must equal `OIDC_ISSUER_URL`; UserInfo is used only when its `sub` equals the ID token's.

**Flow:**
1. The browser hits `GET /api/auth/oidc/login`. bkt mints a PKCE verifier, `state` and `nonce` (HttpOnly, `SameSite=Lax` cookies, 10 min) and redirects to the IdP's authorization endpoint with `code_challenge_method=S256`.
2. The IdP redirects to `GET /api/auth/oidc/callback?code=…&state=…`. bkt checks `state`, exchanges the code with the `code_verifier` (and client secret, if configured), verifies the ID token, calls UserInfo, resolves role and policies from claims, creates or updates the user, sets the refresh token in the httpOnly `bkt_refresh` cookie, and redirects to `FRONTEND_URL/auth/oidc/callback#token=…` — only the access token travels in the URL fragment; the refresh token never appears in a URL.
3. Failures redirect to the same page with `#error=<code>&error_description=…`. Codes include `invalid_state`, `authentication_failed`, `access_denied_no_groups`, `access_denied_group`, `account_locked`, `user_error`.

Successful and denied logins are recorded in the audit log with `provider`, `subject` and `groups` metadata.

**SSO config response fields:** `oidc_enabled`, `oidc_auth_url`, `oidc_provider_name`.

---

### Vault OIDC (legacy slot)

**Endpoints:** `GET /auth/vault/login` and `GET /auth/vault/callback`

The same implementation driven by the older `VAULT_OIDC_*` variables (`VAULT_OIDC_ENABLED`, `VAULT_OIDC_CLIENT_ID`, `VAULT_OIDC_PROVIDER_URL`, `VAULT_OIDC_REDIRECT_URL`, `VAULT_OIDC_SCOPES`). Kept for existing deployments; it has no claims mapping beyond the `policies` claim. New integrations should use `OIDC_*`.

> See [SSO Setup Guide](../guides/sso-setup.md) for provider-specific setup (Keycloak, Okta, Entra ID, Authentik, Kanidm).

---

### Google OAuth

**Endpoint:** `GET /auth/google/login`

Initiates Google OAuth flow. Redirects browser to Google consent screen.

```
https://localhost:9443/api/auth/google/login
```

After authentication, Google redirects to `/api/auth/google/callback` which:
1. Creates user account on first login
2. If Google Workspace is enabled: fetches user's groups and syncs policies
3. Sets the refresh token in the httpOnly `bkt_refresh` cookie and redirects to `FRONTEND_URL/auth/google/callback#token=…` (access token only)

**With Google Workspace Integration:**

When `GOOGLE_WORKSPACE_ENABLED=true`, the system automatically syncs policies based on the user's Google Workspace group memberships. Group names are mapped to policy names using the configured sync mode.

| Environment Variable | Description |
|---------------------|-------------|
| `GOOGLE_WORKSPACE_ENABLED` | Enable group-based policy sync |
| `GOOGLE_SERVICE_ACCOUNT_KEY_FILE` | Path to service account JSON |
| `GOOGLE_WORKSPACE_ADMIN_EMAIL` | Admin email for delegation |
| `GOOGLE_POLICY_SYNC_MODE` | `direct` or `prefix` |
| `GOOGLE_POLICY_GROUP_PREFIX` | Filter groups by prefix |
| `GOOGLE_ALLOWED_DOMAINS` | Comma-separated Workspace domains allowed to sign in. Unset: new users are provisioned only with `ALLOW_REGISTRATION=true` |

With Workspace enabled, the mapped policies always replace the user's policies (an empty result removes them) and a failed group lookup fails the login.

> See [SSO Setup Guide](../guides/sso-setup.md) for complete Google Workspace configuration.

---

## Related Documentation

- [SSO Setup Guide](../guides/sso-setup.md) - Complete SSO configuration guide
- [Access Keys API](access-keys.md) - Alternative authentication method for API access
- [Admin Guide](../guides/admin-guide.md) - User management endpoints
- [Security Overview](../security/security-overview.md) - Comprehensive security documentation
