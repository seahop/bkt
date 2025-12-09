# Object Storage System Documentation

Welcome to the Object Storage System documentation. This S3-compatible object storage solution provides secure, scalable file storage with IAM-style policy enforcement.

## Documentation Structure

### 📚 API Reference
- [Authentication API](api/authentication.md) - User registration, login, and token management
- [Users API](api/users.md) - User account management
- [Access Keys API](api/access-keys.md) - API key generation and management
- [Policies API](api/policies.md) - IAM-style policy management
- [Buckets API](api/buckets.md) - Bucket creation and management
- [Objects API](api/objects.md) - Object upload, download, and management

### 📖 User Guides
- [Getting Started](guides/getting-started.md) - Quick start guide for new users
- [User Guide](guides/user-guide.md) - Guide for end users
- [Admin Guide](guides/admin-guide.md) - Guide for system administrators
- [Developer Guide](guides/developer-guide.md) - Guide for developers integrating with the API

### 🔒 Security
- [Security Overview](security/security-overview.md) - Comprehensive security documentation
- [TLS/SSL Setup](security/tls-setup.md) - Certificate generation and TLS configuration
- [Policy Enforcement](security/policy-enforcement.md) - How policies are evaluated

### 🚀 Deployment
- [Docker Deployment](deployment/docker-deployment.md) - Running with Docker Compose
- [Production Checklist](deployment/production-checklist.md) - Preparing for production
- [Environment Configuration](deployment/environment-config.md) - Environment variables and configuration

### 💡 Examples
- [cURL Examples](examples/curl-examples.md) - Command-line examples using cURL
- [Code Examples](examples/code-examples.md) - Client library examples in various languages
- [Policy Examples](examples/policy-examples.md) - Common policy document examples

## Quick Links

### For Users
- [Creating Your First Bucket](guides/getting-started.md#creating-a-bucket)
- [Uploading Files](guides/getting-started.md#uploading-objects)
- [Managing Access Keys](guides/user-guide.md#access-keys)

### For Administrators
- [User Management](guides/admin-guide.md#user-management)
- [Creating Policies](guides/admin-guide.md#policy-management)
- [Security Best Practices](security/security-overview.md#best-practices)

### For Developers
- [API Authentication](api/authentication.md)
- [SDK Integration](guides/developer-guide.md#sdk-integration)
- [Error Handling](guides/developer-guide.md#error-handling)

## System Requirements

- Docker 20.10+
- Docker Compose 2.0+
- Python 3.8+ (for certificate generation)
- 2GB RAM minimum
- 10GB disk space minimum

## Features

- ✅ **S3-Compatible API** - Compatible with S3 client libraries
- ✅ **User Authentication** - JWT-based authentication with refresh tokens
- ✅ **Access Keys** - Generate API access keys for programmatic access
- ✅ **IAM-Style Policies** - Fine-grained access control with deny-by-default
- ✅ **TLS/SSL Encryption** - All services encrypted in transit
- ✅ **Bucket Management** - Create, list, and delete buckets with per-bucket storage backends (local or S3)
- ✅ **Object Storage** - Upload, download, and delete objects with virtual folder support
- ✅ **Metadata Support** - Store custom metadata with objects
- ✅ **Content Integrity** - MD5 and SHA256 checksums for all objects
- ✅ **Folder Organization** - Virtual folders with breadcrumb navigation in web UI
- ✅ **Flexible Storage** - Per-bucket storage backend selection (local filesystem or S3)
- ✅ **Modern Web UI** - React-based dark mode interface with file management

## Support

For issues, questions, or contributions, please refer to the main repository README.

## License

See the main repository for license information.
