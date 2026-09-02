# Authentication API

The authentication API handles user registration, login, token refresh, and logout operations.

## Base URL

```
https://localhost:9443/api/auth
```

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
  "password": "string"        // Min 8 characters, required
}
```

**Success Response (201 Created):** *(only when registration is enabled)*
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
- `400 Bad Request` - Invalid input
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

**Success Response (200 OK):**
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

**Authentication:** None required (refresh token in body)

**Request Body:**
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

**Rotation:** every successful refresh **rotates** the refresh token — the token you sent is revoked and the response contains a new pair. Always replace your stored refresh token with the one from the response.

**Reuse detection:** replaying a refresh token that was already rotated is treated as a theft indicator (per the OAuth security BCP): **all of that user's sessions are revoked** and the event is logged to the audit trail as `auth.refresh_reuse`. Replaying a token that was revoked by logout is simply rejected without revoking other sessions.

**Error Responses:**
- `400 Bad Request` - Missing or invalid refresh token
- `401 Unauthorized` - Expired, invalid, rotated, or revoked refresh token

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

Invalidate the current session. Logout revokes **both tokens**: the access token you present, and its sibling refresh token (whose ID is embedded in the access token, so the pair is revoked even when the client never sends the refresh token). You may also pass a refresh token explicitly in the body to blacklist it.

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
   - Never store tokens in localStorage (XSS vulnerable)
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
      const { token: newToken } = await refreshResponse.json();
      setAccessToken(newToken);

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
- Logout revokes both the access and refresh token
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

**Response (200 OK):**
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

### Generic OIDC (Browser SSO)

**Endpoints:** `GET /auth/vault/login` (initiate) and `GET /auth/vault/callback`

Browser-based OIDC login with PKCE. Despite the `VAULT_` prefix on the configuration variables, this is a **generic, standards-based OIDC flow**: the authorization, token, and JWKS endpoints are read from the provider's **discovery document**, so it works with any compliant OIDC identity provider. It is validated against HashiCorp Vault and **Keycloak**.

**Configuration:**

| Environment Variable | Description |
|---------------------|-------------|
| `VAULT_OIDC_ENABLED` | Enable the OIDC browser flow |
| `VAULT_OIDC_CLIENT_ID` | OIDC client ID |
| `VAULT_OIDC_PROVIDER_URL` | Provider/issuer URL (discovery document is fetched from here) |
| `VAULT_OIDC_REDIRECT_URL` | Callback URL (default `https://localhost:9443/api/auth/vault/callback`) |
| `VAULT_OIDC_SCOPES` | Requested scopes (default `openid profile`) |

**Flow:**
1. The browser hits `GET /api/auth/vault/login` and is redirected to the IdP's authorization endpoint.
2. After authentication, the IdP redirects to the callback, which creates or updates the user, syncs policies from a `policies` claim (same rules as Vault JWT login), and hands tokens to the frontend.

All SSO logins (Google, Vault JWT, and OIDC) are recorded in the audit log with provider metadata.

> See [SSO Setup Guide](../guides/sso-setup.md) for provider-specific setup, including Keycloak.

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
3. Returns tokens to the frontend

**With Google Workspace Integration:**

When `GOOGLE_WORKSPACE_ENABLED=true`, the system automatically syncs policies based on the user's Google Workspace group memberships. Group names are mapped to policy names using the configured sync mode.

| Environment Variable | Description |
|---------------------|-------------|
| `GOOGLE_WORKSPACE_ENABLED` | Enable group-based policy sync |
| `GOOGLE_SERVICE_ACCOUNT_KEY_FILE` | Path to service account JSON |
| `GOOGLE_WORKSPACE_ADMIN_EMAIL` | Admin email for delegation |
| `GOOGLE_POLICY_SYNC_MODE` | `direct` or `prefix` |
| `GOOGLE_POLICY_GROUP_PREFIX` | Filter groups by prefix |

> See [SSO Setup Guide](../guides/sso-setup.md) for complete Google Workspace configuration.

---

## Related Documentation

- [SSO Setup Guide](../guides/sso-setup.md) - Complete SSO configuration guide
- [Access Keys API](access-keys.md) - Alternative authentication method for API access
- [Admin Guide](../guides/admin-guide.md) - User management endpoints
- [Security Overview](../security/security-overview.md) - Comprehensive security documentation
