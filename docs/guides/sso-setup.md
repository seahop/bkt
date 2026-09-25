# Single Sign-On (SSO) Setup Guide

This guide covers configuring SSO authentication with automatic policy assignment for your object storage system.

## Overview

The system supports two SSO integrations:

| Provider | Protocol | Policy Support | Use Case |
|----------|----------|----------------|----------|
| **OIDC** (Keycloak, Okta, Entra ID, Auth0, Authentik, Kanidm, …) | OIDC authorization code + PKCE | Full (`policies` claim) + admin/user groups | Any OpenID Connect identity provider — **recommended** |
| **Vault OIDC** (legacy slot) | OIDC + PKCE | Full (via `policies` claim) | Existing `VAULT_OIDC_*` deployments |
| **HashiCorp Vault** (legacy) | JWT | Full (via claims) | Direct JWT login against Vault's JWT auth |
| **Google OAuth** | OAuth 2.0 | Full (via Workspace groups) | Google Workspace environments |
| **Google OAuth** | OAuth 2.0 | Manual only | Personal Gmail accounts |

The browser-based OIDC flow is **generic**: the authorization, token, UserInfo
and JWKS endpoints are all taken from the provider's discovery document
(`<issuer>/.well-known/openid-configuration`), so **any standard OIDC IdP
works**. Configure it with the `OIDC_*` variables (below). The older
`VAULT_OIDC_*` variables drive the same code and keep working for existing
deployments, but they lack the claims mapping (admin/user groups, username
claim, account linking) that `OIDC_*` adds.

### Key Features

- **Automatic User Provisioning**: Users are created on first SSO login
- **Policy Sync from Token Claims**: the ID token can include a `policies` claim (JSON array of policy names) that auto-assigns on login
- **SSO as Source of Truth**: Policies sync on every login (changes in SSO propagate immediately)
- **Hybrid Support**: SSO users and local users can coexist
- **Audited**: every SSO login is recorded in the audit log with provider metadata

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
- **What gets replaced depends on the provider** (see each section):
  - **OIDC** (`OIDC_*`): a present claim replaces the user's direct policies
    (empty/unmatched removes all); with `OIDC_POLICIES_AUTHORITATIVE=true` a
    missing claim removes them too.
  - **Vault** (JWT and `VAULT_OIDC_*`): by default a claim replaces them only
    when it names **at least one existing bkt policy**; a claim with only
    Vault-side names (or `[]`) keeps admin-assigned policies. Set
    `VAULT_POLICIES_AUTHORITATIVE=true` for strict replacement.
  - **Google Workspace**: only *Workspace-mapped* policies (those some
    Workspace group maps to) are added/removed; manually assigned policies
    are kept.

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

## OIDC Configuration (any IdP)

bkt is an OpenID Connect **relying party** using the **authorization code flow
with PKCE (S256)** — the flow every current IdP requires for browser clients.
PKCE is always sent; a client secret is optional. State and nonce protect
against CSRF and token injection, the ID token is verified against the
provider's JWKS (RS256/384/512 and ES256/384/512), and profile/group claims are
read from the ID token and the UserInfo endpoint.

### Environment variables

```bash
OIDC_ISSUER_URL=https://idp.example.com/realms/myrealm   # must serve /.well-known/openid-configuration
OIDC_CLIENT_ID=bkt
OIDC_CLIENT_SECRET=                                      # optional — confidential client. Empty = public client (PKCE only)
OIDC_REDIRECT_URL=https://bkt.example.com/api/auth/oidc/callback
OIDC_SCOPES=openid profile email
OIDC_PROVIDER_NAME=Okta                                  # label on the login button
FRONTEND_URL=https://bkt.example.com

# Claims mapping (all optional)
OIDC_USERNAME_CLAIM=          # username at first login; default preferred_username → email local part
OIDC_GROUPS_CLAIM=groups      # claim carrying group names
OIDC_ADMIN_GROUP=bkt-admins   # members are bkt admins; re-evaluated every login. Empty = SSO never grants admin
OIDC_USER_GROUP=bkt-users     # if set, non-admins must be members or login is denied
OIDC_POLICIES_CLAIM=policies  # claim listing bkt policy names to sync on every login
OIDC_POLICIES_AUTHORITATIVE=  # true: a *missing* policies claim also clears policies (default: true iff OIDC_POLICIES_CLAIM is set)
OIDC_LINK_BY_EMAIL=false      # link a new subject to an existing OIDC account with the same *verified* email
```

The discovery document's `issuer` must equal `OIDC_ISSUER_URL` (a trailing
slash is ignored); otherwise login fails and the startup log says so
(`discovery issuer X != configured OIDC_ISSUER_URL Y; ... set
OIDC_EXPECTED_ISSUER=X`). When bkt legitimately reaches the provider under a
different URL than its public issuer — e.g. Keycloak via an internal hostname
(`http://keycloak:8080/realms/myrealm`) while `KC_HOSTNAME` makes it advertise
`https://sso.example.com/realms/myrealm` — set
`OIDC_EXPECTED_ISSUER=https://sso.example.com/realms/myrealm`. Discovery and
every ID token's `iss` must then equal that value exactly; note the endpoints
(token, JWKS, UserInfo) are still taken from the discovery document, so they
must be reachable from the backend (Keycloak: `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true`).
The check runs once at startup (logged as `discovery OK` or `ERROR: ...
discovery check failed`) and on every discovery refresh. UserInfo responses
are only used when they carry a `sub` equal to the ID token's subject.

`OIDC_ENABLED` is implied when the issuer and client ID are both set; set
`OIDC_ENABLED=false` to keep a configured provider switched off.

### How users and roles are resolved

- **Identity** is the ID token's `sub`; it is stored as the user's SSO ID and is
  the only thing matched on later logins. Renaming a user in the IdP never
  creates a duplicate.
- **Username** is chosen only when the account is first created: the
  `OIDC_USERNAME_CLAIM` claim if set, else `preferred_username`, `name`, the
  email local part, then `sub`. It is sanitized to `A-Z a-z 0-9 . _ -` and
  suffixed with a number if it collides with any existing account, so an IdP
  can never take over a local user.
- **Email** must be unique. If the address already belongs to a local (or
  other-provider) account, login fails with a clear message rather than
  silently creating a second identity.
- **Admin** is granted to members of `OIDC_ADMIN_GROUP` and revoked from
  everyone else *on every login*, so group changes in the IdP take effect at
  once. Leave it empty to manage admins in bkt only (SSO never touches
  `is_admin`).
- **Access gating**: when `OIDC_USER_GROUP` is set, users who are in neither
  group are denied with one of two distinct errors — *no groups claim at all*
  (a mapper is missing) or *not a member* — and the denial is audit-logged.
- **Policies**: the `OIDC_POLICIES_CLAIM` claim (JSON array, or a space/comma
  separated string) is synced to the user's bkt policies on every login; the
  IdP is the source of truth (unchanged in this release). Names must match
  bkt policies exactly. Whenever the claim is present its contents *replace*
  the user's direct policies — an empty claim (or one naming no existing
  policy) removes them all, **including policies an administrator assigned
  in bkt**, so offboarding in the IdP takes effect at the next login. If your
  IdP omits the claim entirely when a user has no policies, set
  `OIDC_POLICIES_AUTHORITATIVE=true` (the default when `OIDC_POLICIES_CLAIM`
  is set explicitly) so a missing claim also clears them. With neither, a
  missing claim leaves admin-assigned policies untouched. If you want to
  manage some users' policies by hand in bkt, don't emit the claim for them
  and leave `OIDC_POLICIES_CLAIM`/`OIDC_POLICIES_AUTHORITATIVE` unset.
- **Account linking** (`OIDC_LINK_BY_EMAIL=true`) is for IdP migrations where
  every user receives a new `sub`: an unknown subject whose `email_verified`
  address matches the **IdP-asserted** address (`sso_email`, never the
  user-editable email) of exactly one existing **OIDC** account of this
  provider is attached to that account. Linking ends the account's existing
  sessions and temporary credentials and deactivates its long-lived access
  keys (the new identity must mint its own). If more than one account matches,
  login is refused. Local password accounts are never linked automatically.
- SSO accounts cannot change their email or set a local password in bkt;
  both are managed by the identity provider.

### Example: Keycloak

1. Create a client in your realm: type **OpenID Connect**, client
   authentication **on** (confidential) or **off** (public, PKCE only — both
   work), *Proof Key for Code Exchange* method **S256**.
2. Valid redirect URI: `https://bkt.example.com/api/auth/oidc/callback`.
3. Client scopes: make sure `email` and `profile` are assigned. For groups, add
   a **Group Membership** mapper named `groups` (uncheck *Full group path* so
   names come through as `bkt-admins`, not `/bkt-admins`).

```bash
OIDC_ISSUER_URL=https://kc.example.com/realms/myrealm
OIDC_CLIENT_ID=bkt
OIDC_CLIENT_SECRET=<from Credentials tab, or empty for a public client>
OIDC_REDIRECT_URL=https://bkt.example.com/api/auth/oidc/callback
OIDC_PROVIDER_NAME=Keycloak
OIDC_ADMIN_GROUP=bkt-admins
OIDC_USER_GROUP=bkt-users
FRONTEND_URL=https://bkt.example.com
```

### Example: Okta

Create an **OIDC – Web Application**, grant *Authorization Code*, add the
sign-in redirect URI. To send groups, add a `groups` claim to the ID token
(Security → API → your authorization server → Claims: name `groups`, include
in ID token, value type *Groups*, filter *Matches regex* `.*`).

```bash
OIDC_ISSUER_URL=https://<tenant>.okta.com/oauth2/default
OIDC_CLIENT_ID=<client id>
OIDC_CLIENT_SECRET=<client secret>
OIDC_PROVIDER_NAME=Okta
OIDC_ADMIN_GROUP=bkt-admins
```

### Example: Microsoft Entra ID (Azure AD)

Register an app (Web platform, redirect URI as above). Under *Token
configuration* add the **groups** claim (or use app roles) and the **email**
and **upn** optional claims. Entra emits group *object IDs* by default; either
switch the claim to group names for security groups or set `OIDC_ADMIN_GROUP`
to the admin group's object ID.

```bash
OIDC_ISSUER_URL=https://login.microsoftonline.com/<tenant-id>/v2.0
OIDC_CLIENT_ID=<application (client) id>
OIDC_CLIENT_SECRET=<client secret>
OIDC_USERNAME_CLAIM=upn
OIDC_PROVIDER_NAME=Microsoft
OIDC_ADMIN_GROUP=<group name or object id>
```

### Example: Authentik / Kanidm / Auth0

All three are standard: use the issuer URL from the provider (Authentik:
`https://auth.example.com/application/o/<slug>/`; Kanidm:
`https://idm.example.com/oauth2/openid/<client>`; Auth0:
`https://<tenant>.auth0.com/`), register the redirect URI, and expose a
`groups` claim. Kanidm signs ID tokens with **ES256** by default — supported.

### Troubleshooting

| Symptom | Cause / fix |
|---|---|
| Redirected back with `invalid_state` | The 10-minute login window expired, or cookies were blocked. Start the login again from the bkt login page. |
| `token exchange failed (400) … PKCE` | The IdP client is configured for a different PKCE method. Use S256 (bkt does not support `plain`). |
| `token exchange failed (401) invalid_client` | Wrong or missing `OIDC_CLIENT_SECRET` for a confidential client, or the client is public but a secret was set. |
| `ID token verification failed: unexpected signing method` | The client is configured to sign ID tokens with HS256. Switch it to RS256 or ES256. |
| `token response contained no id_token` | The `openid` scope is not granted to the client. |
| `access_denied_no_groups` on the login page | The token has no groups claim: add a groups mapper/claim in the IdP. |
| Username is `jane_doe1` | `jane_doe` already existed (local or another provider); the SSO account was created with a suffix instead of taking over the existing one. |
| Behind a reverse proxy the callback fails silently | bkt sets `Secure` cookies when it sees TLS or `X-Forwarded-Proto: https`; make sure the proxy forwards that header. |

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

# Expected audience claim (required; tokens without it are rejected)
VAULT_JWT_AUDIENCE=objectstore

# Expected issuer claim (optional; enforced when set)
VAULT_JWT_ISSUER=https://vault.company.com:8200/v1/identity/oidc
```

Vault policy sync (JWT login and the `VAULT_OIDC_*` slot) is
**non-authoritative by default**: the documented Vault token template always
emits a `policies` claim, usually listing Vault-side policy names or `[]`. A
claim therefore replaces the user's bkt policies **only when at least one of
its names matches an existing bkt policy**; otherwise the policies an
administrator assigned in bkt are kept. A token without the claim also leaves
them untouched. Set `VAULT_POLICIES_AUTHORITATIVE=true` to make Vault the
source of truth: a present claim then always replaces the user's policies
(an empty or unmatched claim removes them all — use this when offboarding in
Vault must revoke bkt access).

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
# Enable Google OIDC
GOOGLE_OIDC_ENABLED=true

# OAuth credentials from Google Cloud Console
GOOGLE_CLIENT_ID=your-client-id.apps.googleusercontent.com
GOOGLE_CLIENT_SECRET=your-client-secret

# Callback URL (must match Google Console)
GOOGLE_REDIRECT_URL=https://your-domain.com/api/auth/google/callback
```

### Restricting who can sign in

```bash
# Comma-separated Google Workspace domain(s) allowed to sign in.
GOOGLE_ALLOWED_DOMAINS=example.com,example.org
```

Both the account's Workspace hosted domain (`hd`) and its email domain must be
in the list, and the email must be verified; consumer (`@gmail.com`) accounts
have no `hd` and are rejected. **If `GOOGLE_ALLOWED_DOMAINS` is unset, new
Google users are only auto-provisioned when `ALLOW_REGISTRATION=true`**
(otherwise only already-linked accounts can sign in); bkt logs a warning at
startup either way. Usernames are derived from the email local part,
sanitized, and suffixed with a number on collision.

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

Once enabled, Workspace manages **only the mapped policies** — the bkt
policies that some Workspace group in the domain maps to (per
`GOOGLE_POLICY_SYNC_MODE` / `GOOGLE_POLICY_GROUP_PREFIX`). On every login they
are added or removed to match the user's current groups (leaving a group
revokes its policy); policies an administrator assigned by hand that no group
maps to are **left untouched**. (In `direct` mode without a prefix, a bkt
policy that has the same name as any Workspace group counts as mapped.) The
domain's group list is fetched with the same Directory API scope and cached
for 10 minutes.

If the Directory API lookup fails (outage, revoked delegation), **bkt admins**
still log in (their access does not depend on groups; no sync happens and an
`auth.policy_sync` failure is audit-logged). Everyone else is refused with
`policy_sync_failed` rather than keeping stale, possibly revoked policies.

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
GOOGLE_OIDC_ENABLED=true
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

The system validates SSO tokens for:
- **Cryptographic signature** — verified against the provider's JWKS (fetched
  from the discovery document for OIDC, or from Vault's JWT auth mount for
  legacy JWT login; key rotation is picked up automatically)
- Expiration (`exp` claim)
- Not-before time (`nbf` claim)
- Audience (`aud` claim, if configured)

### Token Security

- Access tokens expire in 15 minutes
- Refresh tokens expire in 7 days and are **rotated on every refresh**; reusing
  a rotated refresh token revokes all of that user's sessions (reuse detection)
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
