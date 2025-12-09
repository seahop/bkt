# cURL Examples

Complete examples for interacting with the Object Storage API using cURL.

## Authentication

### Admin Login

```bash
# Login with the admin account created by setup.py
curl -k -X POST https://localhost:9443/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "testadmin",
    "password": "YOUR_PASSWORD_FROM_SETUP"
  }' | jq -r '.token' > token.txt

# Use the token
export TOKEN=$(cat token.txt)
```

**Note:** The admin password is in your `.env` file. Check with: `grep ADMIN_PASSWORD .env`

### Regular User Login

```bash
# After an admin creates your account
curl -k -X POST https://localhost:9443/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "youruser",
    "password": "yourpassword"
  }' | jq -r '.token' > token.txt

# Use the token
export TOKEN=$(cat token.txt)
```

### Refresh Token

```bash
# Save refresh token from login
REFRESH_TOKEN="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."

curl -k -X POST https://localhost:9443/api/auth/refresh \
  -H 'Content-Type: application/json' \
  -d "{\"refresh_token\":\"$REFRESH_TOKEN\"}"
```

### Self-Registration (If Enabled)

**Note:** Registration is disabled by default (`ALLOW_REGISTRATION=false`). Only admins can create users.

If registration is enabled in production (not recommended):

```bash
curl -k -X POST https://localhost:9443/api/auth/register \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "johndoe",
    "email": "john@example.com",
    "password": "SecurePass123"
  }'
```


## Admin - User Management

### Create User (Admin Only)

```bash
# Admins can create new users
curl -k -X POST https://localhost:9443/api/users \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "newuser",
    "email": "newuser@example.com",
    "password": "SecurePassword123",
    "is_admin": false
  }'
```

### Create Admin User (Admin Only)

```bash
curl -k -X POST https://localhost:9443/api/users \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "newadmin",
    "email": "newadmin@example.com",
    "password": "SecureAdminPass123",
    "is_admin": true
  }'
```

### Delete User (Admin Only)

```bash
curl -k -X DELETE https://localhost:9443/api/users/{user_id} \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

