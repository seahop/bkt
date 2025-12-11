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

Get a new access token using a refresh token.

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

**Error Responses:**
- `400 Bad Request` - Missing or invalid refresh token
- `401 Unauthorized` - Expired or invalid refresh token

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

Invalidate the current access token.

**Endpoint:** `POST /auth/logout`

**Authentication:** Required (Bearer token)

**Headers:**
```
Authorization: Bearer <access_token>
```

**Success Response (200 OK):**
```json
{
  "message": "Logged out successfully"
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
   - Store refresh token securely
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
- Refresh tokens have longer expiration for better UX
- Failed login attempts should be rate-limited (see Rate Limiting section)

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

### Google OAuth

**Endpoint:** `GET /auth/google/login`

Initiates Google OAuth flow. Redirects browser to Google consent screen.

```
https://localhost:9443/api/auth/google/login
```

After authentication, Google redirects to `/api/auth/google/callback` which:
1. Creates user account on first login
2. Returns tokens to the frontend

> **Note:** Google OAuth does not support automatic policy assignment. Use Vault JWT SSO for teams that need policy sync.

---

## Related Documentation

- [SSO Setup Guide](../guides/sso-setup.md) - Complete SSO configuration guide
- [Access Keys API](access-keys.md) - Alternative authentication method for API access
- [Users API](users.md) - User management endpoints
- [Security Overview](../security/security-overview.md) - Comprehensive security documentation
