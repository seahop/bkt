# Policies API

IAM-style policies provide fine-grained access control for users. Policies use a deny-by-default model where explicit deny always wins over allow.

## Base URL

```
https://localhost:9443/api/policies
```

## Policy Document Format

Policies use AWS IAM-compatible JSON format:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "StatementID",
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject"
      ],
      "Resource": [
        "mybucket/*"
      ]
    }
  ]
}
```

### Policy Components

- **Version:** Must be `"2012-10-17"` (AWS IAM standard)
- **Statement:** Array of policy statements (max 20)
- **Sid:** Optional statement ID (alphanumeric, hyphens, underscores)
- **Effect:** Either `"Allow"` or `"Deny"`
- **Action:** Array of actions (service:action format)
- **Resource:** Array of resource patterns
- **Condition:** (Future) Conditional logic

### Validation Rules

- Maximum policy size: 10KB
- Maximum statements: 20 per policy
- Actions must be in `service:action` format
- Resources cannot contain `..` (path traversal prevention)
- Statement must have at least one action and resource

## Endpoints

### List Policies

List all policies (admin) or user's attached policies (regular user).

**Endpoint:** `GET /policies`

**Authentication:** Required (Bearer token)

**Authorization:**
- **Admins:** See all policies in the system
- **Users:** See only policies attached to their account

**Success Response (200 OK):**
```json
[
  {
    "id": "b650551f-1059-4927-9d1c-1c4643fcbe75",
    "name": "ReadOnlyPolicy",
    "description": "Allows read-only access to all buckets",
    "document": "{\"Version\":\"2012-10-17\",\"Statement\":[...]}",
    "created_at": "2025-12-08T21:30:32Z",
    "updated_at": "2025-12-08T21:30:32Z"
  }
]
```

**Example:**
```bash
curl -k -X GET https://localhost:9443/api/policies \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...'
```

---

### Create Policy

Create a new policy (admin only).

**Endpoint:** `POST /policies`

**Authentication:** Required (Bearer token)

**Authorization:** Admin only

**Request Body:**
```json
{
  "name": "ReadOnlyPolicy",
  "description": "Allows read-only access to all buckets",
  "document": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Sid\":\"ReadOnly\",\"Effect\":\"Allow\",\"Action\":[\"s3:GetObject\",\"s3:ListBucket\"],\"Resource\":[\"*\"]}]}"
}
```

**Success Response (201 Created):**
```json
{
  "id": "b650551f-1059-4927-9d1c-1c4643fcbe75",
  "name": "ReadOnlyPolicy",
  "description": "Allows read-only access to all buckets",
  "document": "{\"Version\":\"2012-10-17\",\"Statement\":[...]}",
  "created_at": "2025-12-08T21:30:32Z",
  "updated_at": "2025-12-08T21:30:32Z"
}
```

**Error Responses:**
- `400 Bad Request` - Invalid policy document
- `403 Forbidden` - Not an administrator
- `409 Conflict` - Policy name already exists

**Validation Errors:**
```json
{
  "error": "Invalid policy document",
  "message": "statement 0: statement must have at least one action"
}
```

**Example:**
```bash
curl -k -X POST https://localhost:9443/api/policies \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...' \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "ReadOnlyPolicy",
    "description": "Read-only access",
    "document": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"s3:GetObject\"],\"Resource\":[\"*\"]}]}"
  }'
```

---

### Get Policy

Get a specific policy by ID (admin only).

**Endpoint:** `GET /policies/:id`

**Authentication:** Required (Bearer token)

**Authorization:** Admin only

**Parameters:**
- `id` (path) - UUID of the policy

**Success Response (200 OK):**
```json
{
  "id": "b650551f-1059-4927-9d1c-1c4643fcbe75",
  "name": "ReadOnlyPolicy",
  "description": "Allows read-only access to all buckets",
  "document": "{\"Version\":\"2012-10-17\",\"Statement\":[...]}",
  "created_at": "2025-12-08T21:30:32Z",
  "updated_at": "2025-12-08T21:30:32Z"
}
```

**Error Responses:**
- `400 Bad Request` - Invalid policy ID format
- `403 Forbidden` - Not an administrator
- `404 Not Found` - Policy not found

**Example:**
```bash
curl -k -X GET https://localhost:9443/api/policies/b650551f-1059-4927-9d1c-1c4643fcbe75 \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...'
```

---

### Update Policy

Update an existing policy (admin only).

**Endpoint:** `PUT /policies/:id`

**Authentication:** Required (Bearer token)

**Authorization:** Admin only

**Parameters:**
- `id` (path) - UUID of the policy

**Request Body (all fields optional):**
```json
{
  "name": "UpdatedPolicyName",
  "description": "Updated description",
  "document": "{\"Version\":\"2012-10-17\",\"Statement\":[...]}"
}
```

**Success Response (200 OK):**
```json
{
  "id": "b650551f-1059-4927-9d1c-1c4643fcbe75",
  "name": "UpdatedPolicyName",
  "description": "Updated description",
  "document": "{\"Version\":\"2012-10-17\",\"Statement\":[...]}",
  "created_at": "2025-12-08T21:30:32Z",
  "updated_at": "2025-12-08T22:15:00Z"
}
```

**Error Responses:**
- `400 Bad Request` - Invalid policy ID or document
- `403 Forbidden` - Not an administrator
- `404 Not Found` - Policy not found

**Example:**
```bash
curl -k -X PUT https://localhost:9443/api/policies/b650551f-1059-4927-9d1c-1c4643fcbe75 \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...' \
  -H 'Content-Type: application/json' \
  -d '{
    "description": "Updated description"
  }'
```

---

### Delete Policy

Delete a policy (admin only).

**Endpoint:** `DELETE /policies/:id`

**Authentication:** Required (Bearer token)

**Authorization:** Admin only

**Parameters:**
- `id` (path) - UUID of the policy

**Success Response (200 OK):**
```json
{
  "message": "Policy deleted successfully"
}
```

**Error Responses:**
- `400 Bad Request` - Invalid policy ID format
- `403 Forbidden` - Not an administrator
- `404 Not Found` - Policy not found
- `409 Conflict` - Policy is attached to users

**Conflict Example:**
```json
{
  "error": "Cannot delete policy",
  "message": "Policy is attached to users. Detach it first."
}
```

**Example:**
```bash
curl -k -X DELETE https://localhost:9443/api/policies/b650551f-1059-4927-9d1c-1c4643fcbe75 \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...'
```

---

### Attach Policy to User

Attach a policy to a user account (admin only).

**Endpoint:** `POST /policies/users/:user_id/attach`

**Authentication:** Required (Bearer token)

**Authorization:** Admin only

**Parameters:**
- `user_id` (path) - UUID of the user

**Request Body:**
```json
{
  "policy_id": "b650551f-1059-4927-9d1c-1c4643fcbe75"
}
```

**Success Response (200 OK):**
```json
{
  "message": "Policy attached successfully"
}
```

**Error Responses:**
- `400 Bad Request` - Invalid user ID or policy ID
- `403 Forbidden` - Not an administrator
- `404 Not Found` - User or policy not found

**Example:**
```bash
curl -k -X POST https://localhost:9443/api/policies/users/ece39642-19ac-4ea3-b5cb-e818ce0a9fb9/attach \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...' \
  -H 'Content-Type: application/json' \
  -d '{
    "policy_id": "b650551f-1059-4927-9d1c-1c4643fcbe75"
  }'
```

---

### Detach Policy from User

Remove a policy from a user account (admin only).

**Endpoint:** `DELETE /policies/users/:user_id/detach/:policy_id`

**Authentication:** Required (Bearer token)

**Authorization:** Admin only

**Parameters:**
- `user_id` (path) - UUID of the user
- `policy_id` (path) - UUID of the policy

**Success Response (200 OK):**
```json
{
  "message": "Policy detached successfully"
}
```

**Error Responses:**
- `400 Bad Request` - Invalid user ID or policy ID
- `403 Forbidden` - Not an administrator
- `404 Not Found` - User or policy not found

**Example:**
```bash
curl -k -X DELETE https://localhost:9443/api/policies/users/ece39642-19ac-4ea3-b5cb-e818ce0a9fb9/detach/b650551f-1059-4927-9d1c-1c4643fcbe75 \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...'
```

---

## Policy Evaluation

### Evaluation Rules

1. **DENY-BY-DEFAULT**: Access is denied unless explicitly allowed
2. **EXPLICIT DENY WINS**: If any statement denies access, it overrides all allows
3. **ADMIN BYPASS**: Admin users automatically pass all policy checks
4. **MULTIPLE POLICIES**: All user policies are evaluated (union of permissions)

### Evaluation Flow

```
┌─────────────────┐
│  Is Admin?      │──Yes──> ALLOW
└────────┬────────┘
         │ No
         ▼
┌─────────────────┐
│  Check Denies   │──Found──> DENY
└────────┬────────┘
         │ None
         ▼
┌─────────────────┐
│  Check Allows   │──Found──> ALLOW
└────────┬────────┘
         │ None
         ▼
      DENY (default)
```

### Action Matching

Actions support wildcards:

- `*` - All actions
- `s3:*` - All S3 actions
- `s3:GetObject` - Specific action

### Resource Matching

Resources support wildcards:

- `*` - All resources
- `mybucket/*` - All objects in mybucket
- `mybucket/photos/*` - All objects under photos/ prefix

---

## Common Policy Examples

### Read-Only Access
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ReadOnly",
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:ListBucket",
        "objectstore:GetObject",
        "objectstore:ListBucket"
      ],
      "Resource": ["*"]
    }
  ]
}
```

### Bucket-Specific Write
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "AllowWriteToBucket",
      "Effect": "Allow",
      "Action": [
        "s3:PutObject",
        "s3:DeleteObject"
      ],
      "Resource": ["mybucket/*"]
    }
  ]
}
```

### Deny Delete
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "AllowRead",
      "Effect": "Allow",
      "Action": ["s3:GetObject"],
      "Resource": ["*"]
    },
    {
      "Sid": "DenyDelete",
      "Effect": "Deny",
      "Action": ["s3:DeleteObject"],
      "Resource": ["*"]
    }
  ]
}
```

### Multiple Buckets
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "AccessPublicBucket",
      "Effect": "Allow",
      "Action": ["s3:*"],
      "Resource": ["public/*"]
    },
    {
      "Sid": "AccessPrivateBucket",
      "Effect": "Allow",
      "Action": ["s3:GetObject"],
      "Resource": ["private/*"]
    }
  ]
}
```

---

## Security Validation

All policies are validated before storage:

### Path Traversal Prevention
```json
{
  "Resource": ["bucket/../../../etc/passwd"]
}
```
**Error:** `resource cannot contain '..'`

### Empty Actions
```json
{
  "Action": []
}
```
**Error:** `statement must have at least one action`

### Invalid Effect
```json
{
  "Effect": "Maybe"
}
```
**Error:** `effect must be 'Allow' or 'Deny'`

### Invalid Action Format
```json
{
  "Action": ["GetObject"]
}
```
**Error:** `action must be in format 'service:action'`

---

## Best Practices

1. **Least Privilege**: Grant minimum permissions needed
2. **Explicit Deny**: Use deny statements for critical restrictions
3. **Specific Resources**: Avoid `*` where possible
4. **Statement IDs**: Use descriptive Sid values
5. **Regular Review**: Audit policies regularly
6. **Testing**: Test policies in non-production first
7. **Documentation**: Document policy purpose and scope

---

## Related Documentation

- [Security Overview](../security/security-overview.md) - Policy security model
- [Policy Examples](../examples/policy-examples.md) - More policy examples
- [Admin Guide](../guides/admin-guide.md) - Policy management guide
