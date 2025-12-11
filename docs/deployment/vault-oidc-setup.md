# Vault OIDC SSO Setup Guide

This guide covers setting up HashiCorp Vault as an OIDC identity provider for bkt.

## Overview

bkt supports browser-based SSO using Vault's Identity OIDC provider with PKCE (Proof Key for Code Exchange). This allows users to sign in to bkt by authenticating through Vault - if they're already logged into Vault, they'll be signed in seamlessly.

## Prerequisites

- HashiCorp Vault with Identity secrets engine enabled
- Vault CLI access with permissions to configure OIDC
- Users must have Vault identity entities (created automatically on first login to Vault)

## Vault Configuration

### 1. Create an OIDC Key (if not exists)

```bash
vault write identity/oidc/key/default \
  algorithm="RS256" \
  allowed_client_ids="*" \
  rotation_period="24h" \
  verification_ttl="24h"
```

### 2. Create an OIDC Scope for Profile Information

```bash
vault write identity/oidc/scope/profile \
  description="User profile information" \
  template='{"username": {{identity.entity.name}}, "email": {{identity.entity.metadata.email}}, "groups": {{identity.entity.groups.names}}}'
```

### 3. Create an OIDC Assignment

This controls which users can use the OIDC client. Use `[*]` to allow all users:

```bash
vault write identity/oidc/assignment/allow_all \
  entity_ids="*" \
  group_ids="*"
```

### 4. Create an OIDC Provider

```bash
vault write identity/oidc/provider/default \
  allowed_client_ids="*" \
  scopes_supported="profile"
```

### 5. Create an OIDC Client for bkt

Replace `YOUR_REDIRECT_URL` with your actual backend callback URL:

```bash
vault write identity/oidc/client/bkt \
  redirect_uris="https://your-domain.com:9443/api/auth/vault/callback" \
  assignments="allow_all" \
  key="default" \
  id_token_ttl="24h" \
  access_token_ttl="24h" \
  client_type="public"
```

**Important**: The `client_type="public"` enables PKCE and removes the need for a client secret.

### 6. Retrieve the Client ID

```bash
vault read identity/oidc/client/bkt
```

Note the `client_id` value - you'll need this for bkt configuration.

### 7. Create a Policy for OIDC Users

Users need permission to access the OIDC authorize endpoint:

```bash
vault policy write oidc-user - <<EOF
path "identity/oidc/provider/+/authorize" {
  capabilities = ["read", "update"]
}
EOF
```

### 8. Assign the Policy to Users

For userpass auth:
```bash
vault write auth/userpass/users/USERNAME token_policies="oidc-user"
```

For LDAP auth:
```bash
vault write auth/ldap/groups/GROUPNAME policies="oidc-user"
```

## bkt Configuration

Add these environment variables to your `.env` file:

```bash
# Enable Vault SSO
VAULT_SSO_ENABLED=true

# Vault OIDC Configuration
VAULT_OIDC_ENABLED=true
VAULT_OIDC_CLIENT_ID=<client_id from step 6>
VAULT_OIDC_PROVIDER_URL=https://your-vault-server/v1/identity/oidc/provider/default
VAULT_OIDC_REDIRECT_URL=https://your-bkt-backend:9443/api/auth/vault/callback
VAULT_OIDC_SCOPES=openid profile

# Frontend URL (where users access the web UI)
FRONTEND_URL=https://your-bkt-frontend
```

### Configuration Values Explained

| Variable | Description | Example |
|----------|-------------|---------|
| `VAULT_SSO_ENABLED` | Enable Vault SSO button on login page | `true` |
| `VAULT_OIDC_ENABLED` | Enable OIDC flow (vs legacy JWT) | `true` |
| `VAULT_OIDC_CLIENT_ID` | Client ID from Vault OIDC client | `Your-Client-ID` |
| `VAULT_OIDC_PROVIDER_URL` | Vault OIDC provider URL (API path) | `https://vault.example.com/v1/identity/oidc/provider/default` |
| `VAULT_OIDC_REDIRECT_URL` | Backend callback URL (must match Vault client config) | `https://bkt.example.com:9443/api/auth/vault/callback` |
| `VAULT_OIDC_SCOPES` | OIDC scopes to request | `openid profile` |
| `FRONTEND_URL` | Frontend URL for post-auth redirect | `https://bkt.example.com` |

## Automatic Policy Sync from Vault Groups

bkt can automatically assign policies to users based on their Vault group memberships. This allows administrators to manage team access centrally in Vault rather than configuring permissions per-user in bkt.

### How It Works

1. When a user signs in via Vault SSO, bkt reads the `policies` claim from the ID token
2. The `policies` claim contains Vault group names the user belongs to
3. bkt matches these group names against its internal policies
4. Matching policies are automatically assigned to the user

**Example**: If a user is in Vault group "my-team" and bkt has a policy named "my-team", that user will automatically get the policy on SSO login.

### Step 1: Update the OIDC Scope Template

First, update the profile scope to include the `policies` claim (maps Vault groups to bkt policies):

```bash
vault write identity/oidc/scope/profile \
  description="Profile scope with policies" \
  template='{"username": {{identity.entity.name}}, "email": {{identity.entity.metadata.email}}, "name": {{identity.entity.name}}, "groups": {{identity.entity.groups.names}}, "policies": {{identity.entity.groups.names}}}'
```

### Step 2: Create a Vault Group (matching bkt policy name)

Create an internal group with a name that matches your bkt policy:

```bash
# Create a group (name must match bkt policy name exactly)
vault write identity/group name="<GROUP_NAME>" type="internal"
```

Note the group ID returned - you'll need it if adding members by ID.

### Step 3: Find the User's Entity ID

```bash
# List all entity names
vault list identity/entity/name

# Get details for a specific user
vault read identity/entity/name/<USERNAME>
```

Note the `id` field from the output.

### Step 4: Add User to the Group

```bash
# Add user's entity ID to the group
vault write identity/group/name/<GROUP_NAME> \
  member_entity_ids="<ENTITY_ID>"
```

To add multiple users:
```bash
vault write identity/group/name/<GROUP_NAME> \
  member_entity_ids="<ENTITY_ID_1>,<ENTITY_ID_2>,<ENTITY_ID_3>"
```

### Step 5: Verify Group Membership

```bash
vault read identity/group/name/<GROUP_NAME>
```

You should see `member_entity_ids` containing the user's entity ID.

### Step 6: Create the Matching Policy in bkt

In bkt's web UI:
1. Log in as admin
2. Go to **Settings** → **Policies**
3. Create a new policy with the **exact same name** as the Vault group
4. Configure the bucket access rules for this policy

### Testing Policy Sync

1. Have the user log out of bkt
2. Click "Sign in with Vault"
3. After successful login, check the user's profile - they should have the policy assigned

### Managing Multiple Teams

Create multiple groups and policies for different teams:

```bash
# Create groups in Vault
vault write identity/group name="<TEAM_A>" type="internal"
vault write identity/group name="<TEAM_B>" type="internal"
vault write identity/group name="<TEAM_C>" type="internal"

# Add users to appropriate groups
vault write identity/group/name/<TEAM_A> \
  member_entity_ids="<ENTITY_ID_1>,<ENTITY_ID_2>"

vault write identity/group/name/<TEAM_B> \
  member_entity_ids="<ENTITY_ID_3>,<ENTITY_ID_4>"
```

Then create matching policies in bkt with the same names.

### Troubleshooting Policy Sync

**Policies not being assigned:**
1. Verify the Vault group name exactly matches the bkt policy name (case-sensitive)
2. Check that the scope template includes the `policies` claim
3. Ensure the user is actually a member of the Vault group

**Check user's group memberships:**
```bash
vault read identity/entity/name/USERNAME
```
Look at `direct_group_ids` and `group_ids` fields.

**Check what's in the ID token:**
You can decode the ID token (it's a JWT) to see what claims are being sent. The `policies` array should contain the group names.

## Deployment Scenarios

### Development (localhost)

```bash
VAULT_OIDC_REDIRECT_URL=https://localhost:9443/api/auth/vault/callback
FRONTEND_URL=https://localhost
```

Vault client redirect_uris:
```bash
vault write identity/oidc/client/bkt \
  redirect_uris="https://localhost:9443/api/auth/vault/callback"
```

### Production (custom domain)

```bash
VAULT_OIDC_REDIRECT_URL=https://bkt.company.com/api/auth/vault/callback
FRONTEND_URL=https://bkt.company.com
```

Vault client redirect_uris:
```bash
vault write identity/oidc/client/bkt \
  redirect_uris="https://bkt.company.com/api/auth/vault/callback"
```

### With Reverse Proxy (single port)

If using a reverse proxy that routes `/api` to the backend:

```bash
VAULT_OIDC_REDIRECT_URL=https://bkt.company.com/api/auth/vault/callback
FRONTEND_URL=https://bkt.company.com
```

## Troubleshooting

### "Permission denied" on authorize endpoint

**Cause**: User doesn't have the `oidc-user` policy or isn't logged into Vault in the browser.

**Fix**:
1. Ensure the user has the `oidc-user` policy assigned
2. Log out and back into Vault UI in the same browser
3. Try the SSO flow again

### "Redirect URI mismatch"

**Cause**: The redirect URI in bkt config doesn't exactly match what's registered in Vault.

**Fix**: Update the Vault client with the correct redirect URI:
```bash
vault write identity/oidc/client/bkt \
  redirect_uris="https://exact-url-from-env/api/auth/vault/callback"
```

### "PKCE is required for public clients"

**Cause**: PKCE parameters aren't being sent. This shouldn't happen with bkt's implementation.

**Fix**: Ensure you're using bkt's Vault SSO button, not manually constructing URLs.

### "Invalid state" error

**Cause**: State cookie was lost or expired, or CSRF attack detected.

**Fix**:
1. Ensure cookies are enabled
2. Complete the flow within 10 minutes
3. Don't open multiple login flows simultaneously

### User not found / entity issues

**Cause**: User hasn't logged into Vault before (no identity entity exists).

**Fix**: Have the user log into Vault UI first to create their identity entity.

## Verifying Vault Configuration

Check your OIDC setup:

```bash
# List OIDC clients
vault list identity/oidc/client

# Read client config
vault read identity/oidc/client/bkt

# Check provider
vault read identity/oidc/provider/default

# Check assignments
vault read identity/oidc/assignment/allow_all

# Check key
vault read identity/oidc/key/default

# Check user's entity
vault list identity/entity/id
vault read identity/entity/id/<entity-id>
```

## Security Considerations

1. **PKCE**: bkt uses PKCE (S256 challenge) for secure authorization code exchange without a client secret
2. **State Parameter**: Random state is used for CSRF protection
3. **HttpOnly Cookies**: PKCE verifier and state are stored in secure, HttpOnly cookies
4. **Token Fragment**: Tokens are passed via URL fragment (#) to keep them out of server logs
5. **TLS Required**: All OAuth flows should use HTTPS in production
