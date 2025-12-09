#!/usr/bin/env python3
"""
Object Storage - Unified Setup Script
Handles initial setup including credentials, JWT secrets, and SSL certificates.
"""

import os
import subprocess
import sys
import secrets
import string
from pathlib import Path

# Colors for output
GREEN = '\033[92m'
YELLOW = '\033[93m'
BLUE = '\033[94m'
RED = '\033[91m'
RESET = '\033[0m'

def print_info(msg):
    print(f"{BLUE}[INFO]{RESET} {msg}")

def print_success(msg):
    print(f"{GREEN}[SUCCESS]{RESET} {msg}")

def print_warning(msg):
    print(f"{YELLOW}[WARNING]{RESET} {msg}")

def print_error(msg):
    print(f"{RED}[ERROR]{RESET} {msg}")

def run_command(cmd, description=""):
    """Run a shell command and handle errors"""
    if description:
        print_info(description)
    try:
        result = subprocess.run(cmd, shell=True, check=True, capture_output=True, text=True)
        return result.stdout
    except subprocess.CalledProcessError as e:
        print_error(f"Command failed: {cmd}")
        print_error(f"Error: {e.stderr}")
        sys.exit(1)

def check_openssl():
    """Check if OpenSSL is installed"""
    try:
        subprocess.run(['openssl', 'version'], check=True, capture_output=True)
        return True
    except (subprocess.CalledProcessError, FileNotFoundError):
        print_error("OpenSSL is not installed. Please install it first.")
        print_info("Ubuntu/Debian: sudo apt-get install openssl")
        print_info("macOS: brew install openssl")
        return False

def generate_random_string(length=20):
    """Generate a random alphanumeric string"""
    alphabet = string.ascii_letters + string.digits
    return ''.join(secrets.choice(alphabet) for _ in range(length))

def generate_credentials():
    """Generate admin credentials, database password, and JWT secret"""
    print_info("Generating admin credentials, database password, and JWT secret...")

    admin_password = generate_random_string(20)
    db_password = generate_random_string(32)
    jwt_secret = generate_random_string(32)

    return {
        'admin_username': 'admin',
        'admin_password': admin_password,
        'admin_email': 'admin@example.com',
        'db_password': db_password,
        'jwt_secret': jwt_secret
    }

def create_env_file(credentials):
    """Create or update .env file with all configuration"""
    print_info("Creating .env file...")

    env_content = f"""# Admin Credentials
ADMIN_USERNAME={credentials['admin_username']}
ADMIN_PASSWORD={credentials['admin_password']}
ADMIN_EMAIL={credentials['admin_email']}

# JWT Configuration
JWT_SECRET={credentials['jwt_secret']}
JWT_EXPIRATION=24h
REFRESH_TOKEN_EXPIRATION=168h

# Database Configuration
DB_HOST=postgres
DB_PORT=5432
DB_NAME=objectstore
DB_USER=objectstore
DB_PASSWORD={credentials['db_password']}
DB_SSL_MODE=require

# Storage Configuration
STORAGE_BACKEND=local
STORAGE_ROOT_PATH=/data/storage

# S3 Configuration (if using S3 backend)
S3_ENDPOINT=
S3_REGION=us-east-1
S3_ACCESS_KEY_ID=
S3_SECRET_ACCESS_KEY=
S3_BUCKET_PREFIX=
S3_USE_SSL=true
S3_FORCE_PATH_STYLE=false

# Server Configuration
SERVER_PORT=9443
SERVER_HOST=0.0.0.0

# TLS Configuration
TLS_ENABLED=true
TLS_CERT_FILE=/certs/backend.crt
TLS_KEY_FILE=/certs/backend.key
TLS_CA_FILE=/certs/ca.crt

# CORS Configuration
CORS_ALLOWED_ORIGINS=https://localhost:3000,http://localhost:3000

# Rate Limiting
RATE_LIMIT_ENABLED=true
RATE_LIMIT_REQUESTS_PER_MINUTE=100
"""

    with open('.env', 'w') as f:
        f.write(env_content)

    # Set proper permissions
    os.chmod('.env', 0o600)

    print_success("Created .env file with secure permissions (0600)")
    return credentials

def create_directory_structure():
    """Create certificate directory structure"""
    print_info("Creating certificate directory structure...")

    dirs = [
        'certs/ca',
        'certs/backend',
        'certs/frontend',
        'certs/postgres',
        'docker'
    ]

    for dir_path in dirs:
        Path(dir_path).mkdir(parents=True, exist_ok=True)

    print_success("Directory structure created")

def generate_ca_certificate():
    """Generate Certificate Authority (CA) certificate"""
    print_info("Generating Certificate Authority (CA)...")

    ca_key = 'certs/ca/ca.key'
    ca_cert = 'certs/ca/ca.crt'

    # Generate CA private key
    run_command(
        f'openssl genrsa -out {ca_key} 4096',
        "Generating CA private key..."
    )

    # Generate CA certificate
    run_command(
        f'openssl req -new -x509 -days 3650 -key {ca_key} -out {ca_cert} '
        f'-subj "/C=US/ST=State/L=City/O=ObjectStorage/CN=ObjectStorage-CA"',
        "Generating CA certificate..."
    )

    print_success(f"CA certificate created: {ca_cert}")

def generate_service_certificate(service_name, alt_names):
    """Generate certificate for a service with SANs"""
    print_info(f"Generating certificate for {service_name}...")

    service_dir = f'certs/{service_name}'
    key_file = f'{service_dir}/{service_name}.key'
    csr_file = f'{service_dir}/{service_name}.csr'
    cert_file = f'{service_dir}/{service_name}.crt'
    ext_file = f'{service_dir}/{service_name}.ext'

    # Generate private key
    run_command(
        f'openssl genrsa -out {key_file} 2048',
        f"Generating {service_name} private key..."
    )

    # Generate CSR
    run_command(
        f'openssl req -new -key {key_file} -out {csr_file} '
        f'-subj "/C=US/ST=State/L=City/O=ObjectStorage/CN={service_name}"',
        f"Generating {service_name} certificate signing request..."
    )

    # Create extensions file for SANs
    san_entries = ','.join([f'DNS:{name}' for name in alt_names['dns']] +
                           [f'IP:{ip}' for ip in alt_names['ip']])

    ext_content = f"""authorityKeyIdentifier=keyid,issuer
basicConstraints=CA:FALSE
keyUsage = digitalSignature, nonRepudiation, keyEncipherment, dataEncipherment
subjectAltName = {san_entries}
"""

    with open(ext_file, 'w') as f:
        f.write(ext_content)

    # Sign certificate with CA
    run_command(
        f'openssl x509 -req -in {csr_file} -CA certs/ca/ca.crt -CAkey certs/ca/ca.key '
        f'-CAcreateserial -out {cert_file} -days 825 -sha256 -extfile {ext_file}',
        f"Signing {service_name} certificate with CA..."
    )

    # Set proper permissions
    os.chmod(key_file, 0o600)

    # Clean up CSR and extension files
    os.remove(csr_file)
    os.remove(ext_file)

    print_success(f"{service_name} certificate created: {cert_file}")

def generate_postgres_certificates():
    """Generate PostgreSQL specific certificates"""
    print_info("Generating PostgreSQL certificates...")

    # PostgreSQL server certificate
    generate_service_certificate('postgres', {
        'dns': ['postgres', 'objectstore-db', 'localhost', 'db', 'database'],
        'ip': ['127.0.0.1', '0.0.0.0']
    })

    # Create server.crt and server.key (PostgreSQL expects these names)
    postgres_dir = 'certs/postgres'
    subprocess.run(f'cp {postgres_dir}/postgres.crt {postgres_dir}/server.crt', shell=True)
    subprocess.run(f'cp {postgres_dir}/postgres.key {postgres_dir}/server.key', shell=True)

    # Copy CA certificate to postgres directory
    subprocess.run(f'cp certs/ca/ca.crt {postgres_dir}/ca.crt', shell=True)

    os.chmod(f'{postgres_dir}/server.key', 0o600)

    print_success("PostgreSQL certificates configured")

def generate_all_certificates():
    """Generate all service certificates"""

    # Backend API certificates
    generate_service_certificate('backend', {
        'dns': ['backend', 'objectstore-backend', 'localhost', 'api', 'server'],
        'ip': ['127.0.0.1', '0.0.0.0']
    })

    # Frontend certificates
    generate_service_certificate('frontend', {
        'dns': ['frontend', 'objectstore-frontend', 'localhost', 'www'],
        'ip': ['127.0.0.1', '0.0.0.0']
    })

    # PostgreSQL certificates
    generate_postgres_certificates()

def create_certificate_readme():
    """Create README for certificates"""
    readme_content = """# SSL/TLS Certificates

## Development Certificates

These are self-signed certificates generated for **development and testing only**.

### Generated Certificates

- **CA Certificate**: `ca/ca.crt` - Certificate Authority (trust this in your browser for testing)
- **Backend**: `backend/backend.{crt,key}` - API server certificates
- **Frontend**: `frontend/frontend.{crt,key}` - Nginx/web server certificates
- **PostgreSQL**: `postgres/postgres.{crt,key}` and `postgres/server.{crt,key}` - Database certificates

### Subject Alternative Names (SANs)

All certificates include multiple SANs for flexibility:
- localhost
- 0.0.0.0
- 127.0.0.1
- Service-specific names (backend, frontend, postgres, etc.)

## Production Deployment

**⚠️ NEVER use these certificates in production!**

For production:

1. Obtain certificates from a trusted CA (Let's Encrypt, DigiCert, etc.)
2. Replace files in the respective directories
3. Update environment variables in `.env` files
4. Restart services: `docker compose restart`

## Trusting Self-Signed Certificates (Development)

### macOS
```bash
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain certs/ca/ca.crt
```

### Linux (Ubuntu/Debian)
```bash
sudo cp certs/ca/ca.crt /usr/local/share/ca-certificates/objectstore-ca.crt
sudo update-ca-certificates
```

### Browser (Chrome/Edge)
Settings → Privacy and Security → Security → Manage Certificates → Authorities → Import → Select `ca.crt`

### Browser (Firefox)
Settings → Privacy & Security → Certificates → View Certificates → Authorities → Import → Select `ca.crt`

## Regenerating Certificates

```bash
python3 setup.py
```

## Certificate Validity

- **CA Certificate**: 10 years (3650 days)
- **Service Certificates**: 825 days

## Security Notes

- All private keys are set to mode 0600 (owner read/write only)
- Certificates are excluded from git via .gitignore
- These certificates are for development only
- Use proper certificates from a trusted CA for production
"""

    with open('certs/README.md', 'w') as f:
        f.write(readme_content)

    print_success("Created certs/README.md")

def update_gitignore():
    """Update .gitignore to exclude certificates and secrets"""
    gitignore_entries = """
# SSL Certificates (DO NOT COMMIT)
certs/
*.key
*.crt
*.csr
*.pem
*.srl

# Environment files with secrets (DO NOT COMMIT)
.env
.env.local
.env.*.local
"""

    gitignore_path = '.gitignore'

    # Read existing gitignore
    existing_content = ""
    if os.path.exists(gitignore_path):
        with open(gitignore_path, 'r') as f:
            existing_content = f.read()

    # Only add if not already present
    if 'SSL Certificates' not in existing_content:
        with open(gitignore_path, 'a') as f:
            f.write(gitignore_entries)
        print_success("Updated .gitignore")
    else:
        print_info(".gitignore already contains certificate exclusions")

def print_summary(credentials):
    """Print summary and next steps"""
    print()
    print("=" * 70)
    print_success("Object Storage Setup Complete!")
    print("=" * 70)
    print()
    print(f"{BLUE}Admin Credentials:{RESET}")
    print(f"  Username: {credentials['admin_username']}")
    print(f"  Password: {credentials['admin_password']}")
    print(f"  Email:    {credentials['admin_email']}")
    print()
    print(f"{BLUE}Database Configuration:{RESET}")
    print(f"  User:     objectstore")
    print(f"  Password: {credentials['db_password']}")
    print(f"  Database: objectstore")
    print()
    print(f"{YELLOW}⚠️  IMPORTANT: Save these credentials securely!{RESET}")
    print()
    print(f"{BLUE}Generated Files:{RESET}")
    print(f"  • .env - Environment configuration")
    print(f"  • certs/ca/ca.crt - Certificate Authority")
    print(f"  • certs/backend/backend.{{crt,key}} - Backend certificates")
    print(f"  • certs/frontend/frontend.{{crt,key}} - Frontend certificates")
    print(f"  • certs/postgres/server.{{crt,key}} - PostgreSQL certificates")
    print()
    print(f"{BLUE}Next Steps:{RESET}")
    print(f"  1. Review the generated .env file")
    print(f"  2. Update docker-compose.yml if needed")
    print(f"  3. Start the services:")
    print(f"     {GREEN}docker compose up -d{RESET}")
    print()
    print(f"{YELLOW}To trust the CA certificate (for browser testing):{RESET}")
    print(f"  • See certs/README.md for platform-specific instructions")
    print()
    print(f"{YELLOW}For Production:{RESET}")
    print(f"  • Replace certificates with ones from a trusted CA")
    print(f"  • Update .env with production configuration")
    print(f"  • Never commit .env or certificate files to git")
    print()
    print("=" * 70)

def main():
    """Main execution"""
    print()
    print("=" * 70)
    print(f"{GREEN}Object Storage - Setup Script{RESET}")
    print("=" * 70)
    print()

    # Check dependencies
    if not check_openssl():
        sys.exit(1)

    # Create directory structure
    create_directory_structure()

    # Generate admin credentials and JWT secret
    credentials = generate_credentials()

    # Create .env file
    create_env_file(credentials)

    # Generate CA
    generate_ca_certificate()

    # Generate service certificates
    generate_all_certificates()

    # Create certificate README
    create_certificate_readme()

    # Update gitignore
    update_gitignore()

    # Print summary
    print_summary(credentials)

if __name__ == '__main__':
    main()
