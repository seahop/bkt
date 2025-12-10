# Documentation Index

Complete index of all documentation files for bkt.

## Quick Reference

- **Getting Started:** [docs/guides/getting-started.md](guides/getting-started.md)
- **Full API Reference:** [docs/api/API.md](api/API.md)
- **S3fs Mounting:** [docs/guides/MOUNTING.md](guides/MOUNTING.md)
- **Security:** [docs/security/security-overview.md](security/security-overview.md)
- **Production:** [docs/deployment/production-checklist.md](deployment/production-checklist.md)

## Documentation Files

### API Reference
- [docs/api/API.md](api/API.md) - **Complete API reference** (all 60+ endpoints)
- [docs/api/authentication.md](api/authentication.md) - User registration, login, tokens
- [docs/api/access-keys.md](api/access-keys.md) - API access key management
- [docs/api/policies.md](api/policies.md) - IAM-style policy management
- [docs/api/buckets-and-objects.md](api/buckets-and-objects.md) - Bucket and object operations

### User Guides
- [docs/guides/getting-started.md](guides/getting-started.md) - Quick start guide
- [docs/guides/admin-guide.md](guides/admin-guide.md) - Administrator's guide
- [docs/guides/MOUNTING.md](guides/MOUNTING.md) - S3fs mounting guide

### Security
- [docs/security/security-overview.md](security/security-overview.md) - Comprehensive security documentation
- [TLS_SETUP.md](../TLS_SETUP.md) - TLS/SSL configuration (root directory)
- [PHASE3_COMPLETE.md](../PHASE3_COMPLETE.md) - Phase 3 security features (root directory)

### Deployment
- [docs/deployment/production-checklist.md](deployment/production-checklist.md) - Production deployment checklist

### Examples
- [docs/examples/curl-examples.md](examples/curl-examples.md) - cURL command examples

## By User Type

### For New Users
1. [Getting Started](guides/getting-started.md)
2. [Authentication API](api/authentication.md)
3. [Buckets and Objects API](api/buckets-and-objects.md)
4. [cURL Examples](examples/curl-examples.md)

### For Administrators
1. [Admin Guide](guides/admin-guide.md)
2. [Policies API](api/policies.md)
3. [Security Overview](security/security-overview.md)
4. [Production Checklist](deployment/production-checklist.md)

### For Developers
1. [Full API Reference](api/API.md)
2. [cURL Examples](examples/curl-examples.md)
3. [S3fs Mounting](guides/MOUNTING.md)
4. [Security Overview](security/security-overview.md)

### For Security Teams
1. [Security Overview](security/security-overview.md)
2. [TLS Setup](../TLS_SETUP.md)
3. [Phase 3 Security](../PHASE3_COMPLETE.md)
4. [Production Checklist](deployment/production-checklist.md)

## Topics

### Authentication & Authorization
- [Authentication API](api/authentication.md) - JWT tokens
- [Access Keys API](api/access-keys.md) - Access/secret keys
- [Policies API](api/policies.md) - IAM policies
- [Security Overview](security/security-overview.md) - Complete security model

### Storage Operations
- [Buckets and Objects API](api/buckets-and-objects.md) - All bucket/object operations
- [cURL Examples](examples/curl-examples.md) - Command-line examples

### Administration
- [Admin Guide](guides/admin-guide.md) - User and policy management
- [Production Checklist](deployment/production-checklist.md) - Deployment preparation

### Security & Compliance
- [Security Overview](security/security-overview.md) - Security architecture
- [TLS Setup](../TLS_SETUP.md) - Certificate management
- [Phase 3 Complete](../PHASE3_COMPLETE.md) - Access control features

## File Organization

```
docs/
├── DOCUMENTATION_INDEX.md             # This file
├── api/                               # API reference
│   ├── API.md                         # Complete API reference (all endpoints)
│   ├── authentication.md
│   ├── access-keys.md
│   ├── policies.md
│   └── buckets-and-objects.md
├── guides/                            # User guides
│   ├── getting-started.md
│   ├── admin-guide.md
│   └── MOUNTING.md                    # S3fs mounting guide
├── security/                          # Security docs
│   └── security-overview.md
├── deployment/                        # Deployment guides
│   └── production-checklist.md
└── examples/                          # Code examples
    └── curl-examples.md

Root directory:
├── TLS_SETUP.md                       # TLS certificate setup
└── PHASE3_COMPLETE.md                 # Phase 3 completion summary
```

## Contributing to Documentation

When adding new documentation:

1. **Choose the right directory:**
   - API reference → `docs/api/`
   - User guides → `docs/guides/`
   - Security → `docs/security/`
   - Deployment → `docs/deployment/`
   - Examples → `docs/examples/`

2. **Follow the template:**
   - Use markdown format
   - Include table of contents for long docs
   - Provide code examples
   - Link to related documentation

3. **Update indexes:**
   - Add to `docs/README.md`
   - Add to this index file
   - Update related documentation links

4. **Best practices:**
   - Keep examples current
   - Test all code samples
   - Use clear, concise language
   - Include error responses
   - Provide security notes

## Documentation Standards

- **Format:** Markdown (.md)
- **Line Length:** 120 characters max
- **Code Blocks:** Always specify language
- **Links:** Use relative paths
- **Examples:** Test before committing
- **Updates:** Date stamp major changes

## Feedback

Found an issue or want to improve documentation?
- Open an issue on GitHub
- Submit a pull request
- Contact the maintainers

---

**Last Updated:** 2025-12-10
