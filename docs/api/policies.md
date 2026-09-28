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
- **Principal:** Optional. Used in **bucket policies** to scope a statement to specific users. Accepted forms: `"*"`, a bkt username, an array of usernames, or the AWS object form `{"AWS": ...}` whose value is `"*"`, a username, an IAM user ARN `arn:aws:iam::<account>:user/<username>` (the account id and any path are ignored — only the final `<username>` is matched against bkt usernames), or an array of those. `"*"` / `{"AWS": "*"}` means every bkt user (and, for Deny statements, anonymous public-read requests). **If omitted, the statement applies to all authenticated users** — so an Allow with no Principal grants everyone. Other principal types (`Service`, `Federated`, `CanonicalUser`) and ARNs that do not name a user (`:root`, `:role/...`, wildcards) are rejected with a message listing the supported forms. (Ignored on user/identity policies, which are already scoped to the user they're attached to.)
- **Action:** An action (`service:action` format) or an array of them — `"Action": "s3:GetObject"` is the same as `["s3:GetObject"]`
- **Resource:** A resource pattern or an array of them
- **Id:** Optional document identifier (informational only)

**Not supported (rejected):** `Condition`, `NotPrincipal`, `NotAction`,
`NotResource`, and any other unknown element. bkt does not evaluate them, so
accepting them would silently grant *more* than the document says (e.g. an
AWS "home folder" policy whose `s3:prefix` Condition would be dropped,
granting the whole bucket). A document containing a non-empty `Condition`
fails with `Condition is not supported yet`. Element names match
case-insensitively. This applies to user, group, and bucket policies.

Documents stored before this check are still evaluated, fail-safe: an `Allow`
statement that carries one of these elements never grants, and a `Deny`
statement that carries one is applied as if the element were absent (so it
denies at least as much as written).

### Matching

- **Actions** are matched **case-insensitively** and support `*` wildcards anywhere — e.g. `s3:*`, `s3:Get*`, `*`.
- **Resources** are matched **case-sensitively** (S3 object keys are case-sensitive) and support `*` wildcards anywhere — e.g. `arn:aws:s3:::bucket/*`, `arn:aws:s3:::bucket/photos/*`.

### Validation Rules

- Maximum policy size: 10KB
- Maximum statements: 20 per policy
- Actions must be in `service:action` format
- Resources cannot contain `..` (path traversal prevention)
- Statement must have at least one action and resource
- Principal (if present) must use one of the forms listed above
- No `Condition` / `NotPrincipal` / `NotAction` / `NotResource` / unknown elements

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

## Groups

Groups are named sets of users that policies can attach to, so a team's permissions are managed in one place instead of per user. All group endpoints are **admin only** and live under `https://localhost:9443/api/groups`.

**Effective policies:** a user's effective policies are the **union** of their directly attached policies and the policies of every group they belong to. All of them are evaluated together under the same rules as below — an explicit `Deny` in any of them (direct or group) still overrides every `Allow`.

### List Groups

**Endpoint:** `GET /groups`

**Success Response (200 OK):** array of groups, each with its members (`users`) and attached `policies`:
```json
[
  {
    "id": "uuid",
    "name": "engineering",
    "description": "Engineering team",
    "users": [ { "id": "uuid", "username": "alice", "...": "..." } ],
    "policies": [ { "id": "uuid", "name": "ReadOnlyPolicy", "...": "..." } ],
    "created_at": "timestamp",
    "updated_at": "timestamp"
  }
]
```

### Create Group

**Endpoint:** `POST /groups`

**Request Body:**
```json
{
  "name": "engineering",
  "description": "Engineering team"
}
```

- `name` (string, required) - 2-64 characters, unique
- `description` (string, optional)

**Success Response (201 Created):** the group object

**Error Responses:**
- `400 Bad Request` - Invalid name
- `409 Conflict` - Group already exists

**Example:**
```bash
curl -k -X POST https://localhost:9443/api/groups \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...' \
  -H 'Content-Type: application/json' \
  -d '{"name": "engineering", "description": "Engineering team"}'
```

### Delete Group

**Endpoint:** `DELETE /groups/:id`

Removes the group along with its memberships and policy attachments. The users and policies themselves are untouched.

**Success Response (200 OK):** `{"message": "Group deleted"}`

### Add Member

**Endpoint:** `POST /groups/:id/members`

**Request Body:**
```json
{ "user_id": "ece39642-19ac-4ea3-b5cb-e818ce0a9fb9" }
```

**Success Response (200 OK):** `{"message": "Member added"}` (adding an existing member is a no-op)

**Error Responses:**
- `400 Bad Request` - Invalid group or user ID
- `404 Not Found` - Group or user not found

### Remove Member

**Endpoint:** `DELETE /groups/:id/members/:user_id`

**Success Response (200 OK):** `{"message": "Member removed"}`

### Attach Policy to Group

**Endpoint:** `POST /groups/:id/policies`

**Request Body:**
```json
{ "policy_id": "b650551f-1059-4927-9d1c-1c4643fcbe75" }
```

**Success Response (200 OK):** `{"message": "Policy attached"}`

**Error Responses:**
- `400 Bad Request` - Invalid group or policy ID
- `404 Not Found` - Group or policy not found

### Detach Policy from Group

**Endpoint:** `DELETE /groups/:id/policies/:policy_id`

**Success Response (200 OK):** `{"message": "Policy detached"}`

> All group operations (create, delete, membership, and policy attachment changes) are recorded in the audit log.

---

## Policy Evaluation

### Evaluation Rules

1. **DENY-BY-DEFAULT**: Access is denied unless explicitly allowed
2. **EXPLICIT DENY WINS**: An explicit `Deny` overrides every `Allow` — across **both** the user's identity policies **and** the bucket (resource) policy. A user-policy Deny is honored even when a bucket policy allows the action.
3. **ADMIN BYPASS**: Admin users automatically pass all policy checks
4. **MULTIPLE POLICIES**: All of the user's effective policies (direct policies ∪ group policies) plus the bucket policy are evaluated; access is granted if any of them allows and none denies (union of permissions, minus any deny)
5. **PUBLIC-READ**: On a [public-read bucket](buckets-and-objects.md#public-read-buckets) (`is_public`), an object read — `s3:GetObject` (S3 GET/HEAD, console download, presign, copy source, replication source) — that no policy allows and none explicitly denies is **allowed** for every authenticated user, as in AWS where public-read applies to all principals. The console's `HEAD` (`s3:HeadObject`) is treated the same way and is also blocked by a Deny on `s3:GetObject`. Public-read grants nothing else: listing, object versions (`?versionId`, version listings), tagging, writes and deletes still need an Allow. It is withheld if any of the user's or the bucket's stored policies cannot be parsed.

In short: **explicit Deny > Allow > public-read (object reads on public buckets) > implicit deny.** A bucket-policy Deny applies to a user when its `Principal` names that user, is `"*"`, or is absent — so a `"*"` Deny on `arn:aws:s3:::bucket/private/*` keeps that prefix private from anonymous readers and signed-in users alike, while a Deny naming `alice` blocks only alice.

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
┌─────────────────────────────┐
│  Public bucket and action   │──Yes──> ALLOW
│  is an object read?         │
└────────┬────────────────────┘
         │ No
         ▼
      DENY (default)
```

### Action Matching

Actions support wildcards:

- `*` - All actions
- `s3:*` - All S3 actions
- `s3:GetObject` - Specific action

### Bucket-configuration actions

Bucket settings are authorized by policy, not by bucket ownership (the
creator of a bucket gets no implicit rights on it). Non-admins need the
action below on the bucket resource (`arn:aws:s3:::bucket`):

| Action | Gates |
|---|---|
| `s3:PutBucketVersioning` | enable/suspend versioning (console and S3 `?versioning`) |
| `s3:GetLifecycleConfiguration` | read the lifecycle rule (S3 `GET ?lifecycle`) |
| `s3:PutLifecycleConfiguration` | set or delete the lifecycle rule |
| `s3:PutReplicationConfiguration` | set/clear `replicate_to` |
| `s3:PutBucketNotification` | webhook URL, secret, and events |
| `s3:PutBucketObjectLockConfiguration` | WORM `retention_days` |
| `s3:PutBucketQuota` | `quota_bytes` (bkt extension) |

Reading a bucket's settings (`GET /api/buckets/{name}`, the console's
*Bucket settings*) needs any one of `s3:ListBucket`, `s3:GetBucketLocation`
or `s3:GetBucketPolicy`; the response's `permissions` tells the console which
of the settings above the caller may change.

`s3:*` covers all of them. Setting `replicate_to` additionally requires
`s3:GetObject` on every object of the source (`arn:aws:s3:::source/*`) and
`s3:PutObject` + `s3:DeleteObject` on every object of the target
(`arn:aws:s3:::target/*`); prefix-scoped grants are not sufficient. On a
public-read source bucket the `s3:GetObject` requirement is met by
public-read unless a policy denies it.
Replication then keeps acting as the configuring user: every sync re-checks
that user's current `s3:GetObject` (source key), `s3:PutObject` (target key)
and `s3:DeleteObject` (target key, for mirrored deletions) per object, so an
explicit Deny on a prefix or a single key — or a later revocation — is
honored. A deleted or locked configurer disables the replication.

### Bucket policies

A bucket policy is a resource policy attached to one bucket. It is evaluated
together with the caller's user and group policies (see the rules above: an
explicit Deny from either side wins; otherwise an Allow from either side
grants). Statements are scoped with `Principal` (bkt usernames or `"*"`); an
Allow for `"*"` grants every **signed-in** bkt user, never unsigned requests —
anonymous access exists only through the bucket's
[public read access](buckets-and-objects.md#public-read-buckets) flag, which is
a separate admin setting. A `"*"` Deny does apply to anonymous public reads.

| Operation | Console / REST | S3 API | Who |
|---|---|---|---|
| Read | *Bucket settings → Bucket policy*, `GET /api/buckets/{name}/policy` | `GET /{bucket}?policy` | admin, or `s3:GetBucketPolicy` on the bucket |
| Set / replace | *Bucket settings → Bucket policy → Edit*, `PUT /api/buckets/{name}/policy` | `PUT /{bucket}?policy` | admin only |
| Delete | *Bucket settings → Bucket policy → Delete policy*, `DELETE /api/buckets/{name}/policy` | `DELETE /{bucket}?policy` | admin only |
| Public status | *Bucket settings → Public read access* | `GET /{bucket}?policyStatus` | admin, or `s3:ListBucket` on the bucket |

Setting or deleting a bucket policy is admin-only on every path — a user
policy granting `s3:PutBucketPolicy` does **not** let a non-admin change it
(that could be used to grant themselves anything on the bucket). `s3:*`
covers `s3:GetBucketPolicy`. Changes are audit-logged as `bucket.policy.set` /
`bucket.policy.delete` with the new and previous documents and `via`
(`console` or `s3`).

The same strict validation applies everywhere (see
[Validation Rules](#validation-rules): no `Condition`/`Not*`, max 10 KB).
Documents are stored in PostgreSQL `jsonb`, so reading one back returns the
same JSON with normalized whitespace and key order.

#### Bucket policies over the S3 API

```bash
cat > policy.json <<'JSON'
{
  "Version": "2012-10-17",
  "Statement": [{
    "Sid": "DenySecret",
    "Effect": "Deny",
    "Principal": {"AWS": "*"},
    "Action": "s3:GetObject",
    "Resource": "arn:aws:s3:::my-bucket/secret/*"
  }]
}
JSON
aws --endpoint-url https://localhost:9000 s3api put-bucket-policy --bucket my-bucket --policy file://policy.json
aws --endpoint-url https://localhost:9000 s3api get-bucket-policy --bucket my-bucket
aws --endpoint-url https://localhost:9000 s3api get-bucket-policy-status --bucket my-bucket
aws --endpoint-url https://localhost:9000 s3api delete-bucket-policy --bucket my-bucket
```

- `GET ?policy` → `200` with the document as `application/json`, or `404 NoSuchBucketPolicy`.
- `PUT ?policy` → `204`. An invalid document → `400 MalformedPolicy` with the
  validator's message (e.g. `Condition is not supported yet`, or the list of
  supported `Principal` forms); a body over 20 KB → `400 MaxMessageLengthExceeded`.
  The body is covered by the SigV4 payload hash like any other.
- `DELETE ?policy` → `204`, also when there is no policy.
- `GET ?policyStatus` → `<PolicyStatus><IsPublic>true|false</IsPublic></PolicyStatus>`,
  where `IsPublic` is the bucket's public read access flag. Bucket-policy
  Allow statements never make a bucket public in bkt (they only grant bkt
  users), so they do not affect it.

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

### Unsupported Condition
```json
{
  "Condition": {"StringLike": {"s3:prefix": ["home/alice/*"]}}
}
```
**Error:** `Condition is not supported yet: remove the Condition block ...`

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
- [Admin Guide](../guides/admin-guide.md) - Policy management guide
- [cURL Examples](../examples/curl-examples.md) - Command-line examples
