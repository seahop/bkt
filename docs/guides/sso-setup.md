# Single Sign-On (SSO) Setup Guide

This guide covers configuring SSO authentication with automatic policy assignment for your object storage system.

## Overview

The system supports two SSO providers:

| Provider | Protocol | Policy Support | Use Case |
|----------|----------|----------------|----------|
| **HashiCorp Vault** | JWT/OIDC | Full (via claims) | Enterprise environments with Vault |
| **Google OAuth** | OAuth 2.0 | Full (via Workspace groups) | Google Workspace environments |
| **Google OAuth** | OAuth 2.0 | Manual only | Personal Gmail accounts |

### Key Features

- **Automatic User Provisioning**: Users are created on first SSO login
- **Policy Sync from JWT Claims**: Vault JWT can include policy names that auto-assign on login
- **SSO as Source of Truth**: Policies sync on every login (changes in SSO propagate immediately)
- **Hybrid Support**: SSO users and local users can coexist

---

## Policy Integration with SSO

### How It Works

1. **Admin creates policies** in the system (via UI or API)
2. **Configure SSO provider** to include policy names in JWT claims
3. **User logs in via SSO** → system reads `policies` claim from JWT
4. **Policies are synced** → user gets exactly the policies listed in JWT

### JWT Claims Structure

```json
{
  "sub": "user-unique-id",
  "email": "alice@company.com",
  "name": "Alice Smith",
  "groups": ["engineering", "devops"],
  "policies": ["team-engineering-access", "devops-readonly"]
}
```

The `policies` claim is an array of policy names that **exactly match** policy names in your system.

### Policy Matching Rules

- **Case-sensitive**: `team-engineering` ≠ `Team-Engineering`
- **Exact match required**: Policy names in JWT must exist in the system
- **Unknown policies are skipped**: If a policy doesn't exist, it's silently ignored
- **Replaces on each login**: SSO is the source of truth; manual policy changes are overwritten

### Multiple Policies

Users can have multiple policies. When evaluating access:

1. **Explicit Deny wins** - Any deny statement blocks access
2. **Union of Allows** - All allow statements are combined
3. **Default Deny** - If no policy allows the action, access is denied

**Example**: User with policies `["team-a-read", "project-x-write"]`:
- Gets read access from `team-a-read`
- Gets write access from `project-x-write`
- Combined: read + write access

---

## Vault JWT Configuration

### Environment Variables

```bash
# Enable Vault SSO
VAULT_SSO_ENABLED=true

# Vault server address
VAULT_ADDR=https://vault.company.com:8200

# JWT auth backend path
VAULT_JWT_PATH=jwt

# Role name for authentication
VAULT_JWT_ROLE=objectstore

# Expected audience claim (optional)
VAULT_JWT_AUDIENCE=objectstore
```

### Vault JWT Auth Method Setup

1. **Enable JWT auth method in Vault**:
```bash
vault auth enable jwt
```

2. **Configure the JWT auth method**:
```bash
vault write auth/jwt/config \
  oidc_discovery_url="https://your-idp.com/.well-known/openid-configuration" \
  default_role="objectstore"
```

3. **Create a role with policy claims**:
```bash
vault write auth/jwt/role/objectstore \
  role_type="jwt" \
  bound_audiences="objectstore" \
  user_claim="email" \
  groups_claim="groups" \
  claim_mappings='{
    "email": "email",
    "name": "name",
    "policies": "policies"
  }' \
  token_policies="default" \
  token_ttl="1h"
```

### JWT Token Requirements

Your JWT must include:

| Claim | Required | Description |
|-------|----------|-------------|
| `sub` | Yes | Unique user identifier |
| `email` | Yes | User's email address |
| `name` | No | Display name |
| `groups` | No | Group memberships |
| `policies` | No* | Policy names to assign |

*Required for automatic policy assignment

### Example JWT Payload

```json
{
  "iss": "https://vault.company.com:8200/v1/identity/oidc",
  "sub": "12345-abcde-67890",
  "aud": "objectstore",
  "exp": 1735500000,
  "iat": 1735496400,
  "email": "alice@company.com",
  "name": "Alice Smith",
  "groups": ["engineering", "platform-team"],
  "policies": [
    "team-engineering-access",
    "platform-buckets-admin"
  ]
}
```

### Login Flow

```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│   User/Client   │     │   Your IdP      │     │  Object Store   │
└────────┬────────┘     └────────┬────────┘     └────────┬────────┘
         │                       │                       │
         │  1. Authenticate      │                       │
         │──────────────────────>│                       │
         │                       │                       │
         │  2. JWT Token         │                       │
         │<──────────────────────│                       │
         │                       │                       │
         │  3. POST /api/auth/vault/login               │
         │  (with JWT token)     │                       │
         │──────────────────────────────────────────────>│
         │                       │                       │
         │                       │  4. Validate JWT      │
         │                       │  5. Extract claims    │
         │                       │  6. Create/update user│
         │                       │  7. Sync policies     │
         │                       │                       │
         │  8. Access token + user info                  │
         │<──────────────────────────────────────────────│
```

### API Endpoint

**POST** `/api/auth/vault/login`

**Request Body**:
```json
{
  "token": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9..."
}
```

**Response**:
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "refresh_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "user": {
    "id": "uuid",
    "username": "alice",
    "email": "alice@company.com",
    "is_admin": false,
    "sso_provider": "vault"
  }
}
```

---

## Google OAuth Configuration

### Basic Environment Variables

```bash
# Enable Google SSO
GOOGLE_SSO_ENABLED=true

# OAuth credentials from Google Cloud Console
GOOGLE_CLIENT_ID=your-client-id.apps.googleusercontent.com
GOOGLE_CLIENT_SECRET=your-client-secret

# Callback URL (must match Google Console)
GOOGLE_REDIRECT_URL=https://your-domain.com/api/auth/google/callback
```

### Google Cloud Console Setup

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create or select a project
3. Navigate to **APIs & Services** → **Credentials**
4. Click **Create Credentials** → **OAuth client ID**
5. Select **Web application**
6. Add authorized redirect URI: `https://your-domain.com/api/auth/google/callback`
7. Copy the Client ID and Client Secret

### Login Flow

1. User clicks "Sign in with Google"
2. Redirect to Google consent screen
3. User authorizes
4. Google redirects back with authorization code
5. System exchanges code for user info
6. User account created/updated
7. If Workspace enabled: policies synced from groups

---

## Google Workspace Integration (Automatic Policy Sync)

For automatic policy assignment with Google, enable Google Workspace integration. This uses the Admin SDK to fetch user group memberships and sync them to policies.

### Requirements

- Google Workspace account (not personal Gmail)
- Service account with domain-wide delegation
- Admin SDK API enabled

### Environment Variables

```bash
# Enable Google Workspace integration
GOOGLE_WORKSPACE_ENABLED=true

# Path to service account JSON key file
GOOGLE_SERVICE_ACCOUNT_KEY_FILE=/path/to/service-account.json

# Admin email for domain-wide delegation (must be a Workspace admin)
GOOGLE_WORKSPACE_ADMIN_EMAIL=admin@your-domain.com

# Policy sync mode: "direct" or "prefix"
GOOGLE_POLICY_SYNC_MODE=direct

# Optional: Only sync groups starting with this prefix
GOOGLE_POLICY_GROUP_PREFIX=bkt-
```

### Step 1: Create Service Account

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Select your project (same as OAuth credentials)
3. Navigate to **IAM & Admin** → **Service Accounts**
4. Click **Create Service Account**
5. Name it (e.g., "bkt-workspace-integration")
6. Click **Create and Continue**
7. Skip role assignment (not needed)
8. Click **Done**
9. Click on the new service account
10. Go to **Keys** tab → **Add Key** → **Create new key** → **JSON**
11. Save the downloaded JSON file securely

### Step 2: Enable Admin SDK API

1. In Google Cloud Console, go to **APIs & Services** → **Library**
2. Search for "Admin SDK API"
3. Click **Enable**

### Step 3: Configure Domain-Wide Delegation

1. In Google Cloud Console, go to **IAM & Admin** → **Service Accounts**
2. Click on your service account
3. Note the **Client ID** (numerical ID)
4. Go to [Google Workspace Admin Console](https://admin.google.com/)
5. Navigate to **Security** → **Access and data control** → **API controls**
6. Click **Manage Domain-wide Delegation**
7. Click **Add new**
8. Enter the **Client ID** from step 3
9. Add OAuth scope: `https://www.googleapis.com/auth/admin.directory.group.readonly`
10. Click **Authorize**

### Step 4: Create Groups and Policies

Create Google Workspace groups that match your policy names:

| Google Group | Policy Name | Access |
|--------------|-------------|--------|
| `engineering@company.com` | `engineering` | Team engineering buckets |
| `devops@company.com` | `devops` | DevOps buckets |
| `bkt-readonly@company.com` | `bkt-readonly` | Read-only access |

**With prefix mode (`GOOGLE_POLICY_SYNC_MODE=prefix`):**

| Google Group | Policy Name | Access |
|--------------|-------------|--------|
| `bkt-engineering@company.com` | `engineering` | Prefix stripped |
| `bkt-devops@company.com` | `devops` | Prefix stripped |

### Policy Sync Modes

**Direct Mode (default):**
- Group name = policy name
- `engineering@company.com` → policy `engineering`
- If `GOOGLE_POLICY_GROUP_PREFIX` is set, only groups with that prefix are synced

**Prefix Mode:**
- Strips the prefix from group name to get policy name
- `bkt-engineering@company.com` → policy `engineering`
- Only groups starting with the prefix are synced

### Example Setup

```bash
# .env configuration for Google Workspace
GOOGLE_SSO_ENABLED=true
GOOGLE_CLIENT_ID=123456789.apps.googleusercontent.com
GOOGLE_CLIENT_SECRET=your-client-secret
GOOGLE_REDIRECT_URL=https://storage.company.com/api/auth/google/callback

# Workspace integration
GOOGLE_WORKSPACE_ENABLED=true
GOOGLE_SERVICE_ACCOUNT_KEY_FILE=/etc/bkt/google-service-account.json
GOOGLE_WORKSPACE_ADMIN_EMAIL=admin@company.com
GOOGLE_POLICY_SYNC_MODE=direct
GOOGLE_POLICY_GROUP_PREFIX=bkt-
```

With this configuration:
- Users in `bkt-engineering@company.com` get policy `bkt-engineering`
- Users in `bkt-readonly@company.com` get policy `bkt-readonly`
- Users in `marketing@company.com` (no prefix) are ignored

### Login Flow with Workspace

```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│   User/Client   │     │     Google      │     │  Object Store   │
└────────┬────────┘     └────────┬────────┘     └────────┬────────┘
         │                       │                       │
         │  1. Click "Sign in with Google"              │
         │──────────────────────────────────────────────>│
         │                       │                       │
         │  2. Redirect to Google consent               │
         │<──────────────────────────────────────────────│
         │                       │                       │
         │  3. Authenticate      │                       │
         │──────────────────────>│                       │
         │                       │                       │
         │  4. Authorization code│                       │
         │<──────────────────────│                       │
         │                       │                       │
         │  5. Callback with code                       │
         │──────────────────────────────────────────────>│
         │                       │                       │
         │                       │  6. Fetch groups      │
         │                       │  (via Admin SDK)      │
         │                       │<──────────────────────│
         │                       │                       │
         │                       │  7. Groups list       │
         │                       │──────────────────────>│
         │                       │                       │
         │                       │  8. Create/update user│
         │                       │  9. Sync policies     │
         │                       │                       │
         │  10. Access token + user info                │
         │<──────────────────────────────────────────────│
```

### Manual Policy Assignment (Without Workspace)

If you don't have Google Workspace or prefer manual assignment:

1. User logs in via Google (account created with no policies)
2. Admin assigns policies via UI or API
3. User has access based on assigned policies

> **Tip**: For automatic policy assignment without Workspace, consider using Vault JWT SSO instead.

---

## Creating Policies for SSO

### Naming Conventions

Use consistent, descriptive names that work well with SSO:

| Pattern | Example | Use Case |
|---------|---------|----------|
| `team-{name}-{access}` | `team-engineering-full` | Team-based access |
| `role-{role}` | `role-developer` | Role-based access |
| `project-{name}-{access}` | `project-alpha-readonly` | Project-specific |
| `env-{env}-{access}` | `env-prod-readonly` | Environment-based |

### Example: Team-Based Setup

**1. Create policies in the system**:

```bash
# Policy: team-engineering-access
curl -k -X POST https://localhost:9443/api/policies \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "team-engineering-access",
    "description": "Engineering team bucket access",
    "document": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"s3:*\"],\"Resource\":[\"arn:aws:s3:::eng-*\",\"arn:aws:s3:::eng-*/*\"]}]}"
  }'

# Policy: team-devops-access
curl -k -X POST https://localhost:9443/api/policies \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "team-devops-access",
    "description": "DevOps team bucket access",
    "document": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"s3:*\"],\"Resource\":[\"arn:aws:s3:::devops-*\",\"arn:aws:s3:::devops-*/*\",\"arn:aws:s3:::backups-*\",\"arn:aws:s3:::backups-*/*\"]}]}"
  }'
```

**2. Configure SSO to include policy names**:

In Vault (or your IdP), configure users/groups to have these claims:

```json
// Engineering team member
{
  "policies": ["team-engineering-access"]
}

// DevOps team member
{
  "policies": ["team-devops-access"]
}

// Platform engineer (both teams)
{
  "policies": ["team-engineering-access", "team-devops-access"]
}
```

### Using the UI

1. Navigate to **Policies** page
2. Click **Create Policy**
3. Enter a name (e.g., `team-engineering-access`)
4. Select buckets this policy applies to
5. Choose permissions (Read, Write, etc.)
6. For advanced setups, use "Advanced (Per-bucket)" mode
7. Save

---

## Per-Bucket Permissions

For fine-grained control, you can set different permissions per bucket within a single policy.

### Simple Mode (Same for All Buckets)

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": ["s3:GetObject", "s3:ListBucket"],
    "Resource": [
      "arn:aws:s3:::bucket-a",
      "arn:aws:s3:::bucket-a/*",
      "arn:aws:s3:::bucket-b",
      "arn:aws:s3:::bucket-b/*"
    ]
  }]
}
```

### Advanced Mode (Per-Bucket Permissions)

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:ListBucket"],
      "Resource": ["arn:aws:s3:::bucket-a", "arn:aws:s3:::bucket-a/*"]
    },
    {
      "Effect": "Allow",
      "Action": ["s3:*"],
      "Resource": ["arn:aws:s3:::bucket-b", "arn:aws:s3:::bucket-b/*"]
    },
    {
      "Effect": "Deny",
      "Action": ["s3:DeleteObject"],
      "Resource": ["arn:aws:s3:::bucket-c", "arn:aws:s3:::bucket-c/*"]
    }
  ]
}
```

Result:
- `bucket-a`: Read-only
- `bucket-b`: Full access
- `bucket-c`: No delete allowed

---

## Troubleshooting

### User Logs In But Has No Access

**Symptoms**: User authenticates via SSO but gets "No permissions" error.

**Causes**:
1. JWT doesn't include `policies` claim
2. Policy names in JWT don't match system policy names (case-sensitive)
3. Policies referenced in JWT don't exist in system

**Solutions**:
1. Check JWT payload: `echo $JWT | cut -d'.' -f2 | base64 -d | jq`
2. Verify policy names match exactly
3. Create missing policies in the system

### Policies Not Updating on Login

**Symptoms**: Changed SSO claims but user still has old policies.

**Cause**: User is logged in with cached token.

**Solution**: User must log out and log back in for policy sync.

### "Invalid token" Error on Vault Login

**Symptoms**: POST to `/api/auth/vault/login` returns 401.

**Causes**:
1. JWT is expired
2. Audience claim doesn't match `VAULT_JWT_AUDIENCE`
3. JWT signature validation failed

**Solutions**:
1. Check JWT expiration: `exp` claim
2. Verify audience matches configuration
3. Ensure Vault JWKS endpoint is accessible

### Google OAuth Redirect Error

**Symptoms**: "redirect_uri_mismatch" error from Google.

**Cause**: Callback URL in `.env` doesn't match Google Console.

**Solution**: Ensure `GOOGLE_REDIRECT_URL` exactly matches the authorized redirect URI in Google Cloud Console.

---

## Security Considerations

### JWT Validation

The system validates Vault JWTs for:
- Expiration (`exp` claim)
- Not-before time (`nbf` claim)
- Audience (`aud` claim, if configured)

> **Note**: Full cryptographic signature validation requires JWKS configuration (see Vault setup).

### Token Security

- Access tokens expire in 15 minutes
- Refresh tokens expire in 7 days
- Tokens are signed with HS256

### SSO Provider Security

- Always use HTTPS for SSO provider connections
- Rotate client secrets periodically
- Use short-lived JWTs (1 hour or less)
- Validate audience claims to prevent token reuse

---

## API Reference

### Check SSO Configuration

**GET** `/api/auth/sso/config`

Returns enabled SSO methods:

```json
{
  "google_enabled": true,
  "google_auth_url": "https://accounts.google.com/o/oauth2/v2/auth?...",
  "vault_enabled": true
}
```

### Vault JWT Login

**POST** `/api/auth/vault/login`

```json
{
  "token": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9..."
}
```

### Google OAuth

**GET** `/api/auth/google/login` - Initiates OAuth flow
**GET** `/api/auth/google/callback` - OAuth callback (handled automatically)

---

## Related Documentation

- [Policies API](../api/policies.md) - Policy CRUD operations
- [Authentication API](../api/authentication.md) - Token management
- [Security Overview](../security/security-overview.md) - Security architecture
- [Admin Guide](admin-guide.md) - User and policy management
