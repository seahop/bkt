#!/usr/bin/env python3
"""
Object Storage - Unified Setup Script
Handles initial setup including credentials, JWT secrets, and SSL certificates.
"""

import argparse
import os
import subprocess
import sys
import secrets
import string
import time
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

ENV_FILE = '.env'

# Keys setup.py generates. JWT_SECRET / ENCRYPTION_KEY are 64 hex chars
# (openssl rand -hex 32 equivalent); the backend refuses secrets shorter than
# 32 characters, the old public dev default, or <placeholder> values.
GENERATED_KEYS = {
    'ADMIN_PASSWORD': lambda: generate_random_string(20),
    'DB_PASSWORD': lambda: generate_random_string(32),
    'JWT_SECRET': lambda: secrets.token_hex(32),
    'ENCRYPTION_KEY': lambda: secrets.token_hex(32),
}
# Non-secret keys added when missing.
DEFAULT_KEYS = {
    'ADMIN_USERNAME': 'admin',
    'ADMIN_EMAIL': 'admin@example.com',
}

def is_placeholder(value):
    return '<' in value or '>' in value

# The JWT secret older releases (and the old docker-compose.yml) defaulted to.
# Public, so the backend refuses it as JWT_SECRET or ENCRYPTION_KEY.
LEGACY_DEV_SECRET = 'dev_jwt_secret_change_in_production'
MIN_SECRET_LENGTH = 32
# Substrings (after lower-casing and dropping '-', '_', '.', ' ') that only occur
# in template values — mirrors the backend's ENCRYPTION_KEY placeholder check.
PLACEHOLDER_MARKERS = ('generatedbysetup', 'changeme', 'changeinproduction',
                       'replaceme', 'yourencryptionkey')

def secret_problem(key, value):
    """Why an existing JWT_SECRET / ENCRYPTION_KEY value must be replaced, or None.

    Mirrors the backend's startup checks (internal/config/config.go): these
    values are refused (or, for a short ENCRYPTION_KEY, warned about), so a
    deployment cannot run on them anyway.
    """
    if not value:
        return 'empty'
    if value == LEGACY_DEV_SECRET:
        return 'the old public development default'
    if is_placeholder(value):
        return 'a placeholder'
    norm = value.lower()
    for ch in '-_. ':
        norm = norm.replace(ch, '')
    if key == 'ENCRYPTION_KEY' and any(m in norm for m in PLACEHOLDER_MARKERS):
        return 'a placeholder'
    if len(value) < MIN_SECRET_LENGTH:
        return f'shorter than {MIN_SECRET_LENGTH} characters'
    return None

# Existing secret values that setup.py replaces are never discarded: data the
# old backend encrypted with them must stay decryptable. The old value moves to
# the backend's decrypt-only variable, and the backend re-encrypts stored
# credentials under the new ENCRYPTION_KEY at startup.
#   ENCRYPTION_KEY_PREVIOUS      comma-separated list of retired ENCRYPTION_KEYs
#   ENCRYPTION_LEGACY_JWT_SECRET a retired JWT_SECRET that encrypted credentials
#                                while no ENCRYPTION_KEY was set
DECRYPT_ONLY_PREVIOUS = 'ENCRYPTION_KEY_PREVIOUS'
DECRYPT_ONLY_LEGACY_JWT = 'ENCRYPTION_LEGACY_JWT_SECRET'

def parse_env_value(raw):
    """Best-effort dotenv value parsing: strip quotes / trailing comments."""
    raw = raw.strip()
    if len(raw) >= 2 and raw[0] == raw[-1] and raw[0] in ('"', "'"):
        return raw[1:-1]
    if ' #' in raw:
        raw = raw.split(' #', 1)[0].rstrip()
    return raw

def parse_env_lines(lines):
    """Map KEY -> (line index, value) for KEY=VALUE lines (last one wins)."""
    entries = {}
    for i, line in enumerate(lines):
        stripped = line.strip()
        if not stripped or stripped.startswith('#') or '=' not in stripped:
            continue
        key, value = stripped.split('=', 1)
        key = key.strip()
        if key.startswith('export '):
            key = key[len('export '):].strip()
        entries[key] = (i, parse_env_value(value))
    return entries

def new_env_content(values):
    return f"""# Generated by setup.py on {time.strftime('%Y-%m-%d %H:%M:%S')}.
# Re-running setup.py keeps every existing value and only fills in missing
# ones — valid secrets are never rotated (an unusable JWT_SECRET/ENCRYPTION_KEY
# is replaced, and its old value kept as a decrypt-only key). See .env.example
# for all options.

# ── Admin (ADMIN_PASSWORD is only used when the admin user is first created) ──
ADMIN_USERNAME={values['ADMIN_USERNAME']}
ADMIN_PASSWORD={values['ADMIN_PASSWORD']}
ADMIN_EMAIL={values['ADMIN_EMAIL']}
ALLOW_REGISTRATION=false

# ── Secrets — keep STABLE for the life of the deployment ──
# JWT_SECRET signs login tokens. ENCRYPTION_KEY encrypts stored S3
# credentials: back it up — a database backup without it cannot be fully
# restored (see docs/deployment/backup-restore.md).
JWT_SECRET={values['JWT_SECRET']}
ENCRYPTION_KEY={values['ENCRYPTION_KEY']}

# ── Database (Postgres is initialised with this password on first start;
#    changing it later requires resetting the Postgres volume) ──
DB_PASSWORD={values['DB_PASSWORD']}
DB_SSL_MODE=require

# ── Storage ──
STORAGE_BACKEND=local
STORAGE_ROOT=/data/buckets

# ── TLS (certificates generated by setup.py under certs/) ──
TLS_ENABLED=true

# Optional: CORS_ALLOWED_ORIGINS, TRUSTED_PROXIES, METRICS_TOKEN, FRONTEND_URL,
# S3_*, OIDC_*, GOOGLE_*, VAULT_OIDC_* — see .env.example.
"""

def write_env_atomically(content):
    tmp = ENV_FILE + '.tmp'
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w') as f:
        f.write(content)
    os.replace(tmp, ENV_FILE)
    os.chmod(ENV_FILE, 0o600)

def ensure_env_file():
    """Create .env, or complete an existing one WITHOUT rotating anything.

    Returns a dict of the values that were newly generated (for the summary).
    Rewriting an existing .env used to rotate DB_PASSWORD (the Postgres volume
    keeps the old one -> backend can't connect) and JWT_SECRET (stored S3
    credentials encrypted with it become unreadable), so existing values are
    always preserved.
    """
    generated = {}

    if not os.path.exists(ENV_FILE):
        print_info("Creating .env file...")
        values = dict(DEFAULT_KEYS)
        for key, gen in GENERATED_KEYS.items():
            values[key] = generated[key] = gen()
        write_env_atomically(new_env_content(values))
        print_success("Created .env with secure permissions (0600)")
        return generated

    print_info(".env exists — keeping all existing values, adding only what is missing")
    with open(ENV_FILE) as f:
        original = f.read()
    lines = original.splitlines()
    entries = parse_env_lines(lines)

    replaced = []
    appended = []
    notes = []
    # Decrypt-only values to write: KEY -> new value.
    decrypt_only = {}

    def current(key):
        if key in decrypt_only:
            return decrypt_only[key]
        return entries[key][1] if key in entries else ''

    def retire(old_value, source_key):
        """Keep a replaced secret usable for decryption (never discard it)."""
        if source_key == 'JWT_SECRET' and entries.get('ENCRYPTION_KEY', (0, ''))[1].strip():
            # The backend only encrypted with JWT_SECRET while ENCRYPTION_KEY
            # was unset; with a key present the old JWT_SECRET decrypts nothing.
            return True
        legacy = current(DECRYPT_ONLY_LEGACY_JWT).strip()
        previous = [p.strip() for p in current(DECRYPT_ONLY_PREVIOUS).split(',') if p.strip()]
        if old_value.strip() in previous or old_value.strip() == legacy:
            notes.append(f"the old {source_key} is already listed as a decrypt-only key")
            return True
        if source_key == 'JWT_SECRET' and not legacy:
            decrypt_only[DECRYPT_ONLY_LEGACY_JWT] = old_value
            notes.append(f"the old JWT_SECRET was moved to {DECRYPT_ONLY_LEGACY_JWT} (decrypt-only) — "
                         f"S3 credentials stored while no ENCRYPTION_KEY was set were encrypted with it")
            return True
        if ',' in old_value:
            print_error(f"The old {source_key} contains a ',' and cannot be listed in the comma-separated "
                        f"{DECRYPT_ONLY_PREVIOUS}. Not replacing it — rotate it by hand "
                        f"(see docs/deployment/configuration.md).")
            return False
        decrypt_only[DECRYPT_ONLY_PREVIOUS] = ','.join(previous + [old_value])
        notes.append(f"the old {source_key} was added to {DECRYPT_ONLY_PREVIOUS} (decrypt-only)")
        return True

    for key, gen in GENERATED_KEYS.items():
        old = entries[key][1] if key in entries else ''
        if key in ('JWT_SECRET', 'ENCRYPTION_KEY'):
            problem = secret_problem(key, old)
        else:
            problem = None if (old and not is_placeholder(old)) else 'empty or a placeholder'
        if problem is None:
            continue
        if key in ('JWT_SECRET', 'ENCRYPTION_KEY') and old:
            # The old backend accepted (or fell back to) this value and may have
            # encrypted stored S3 credentials with it: keep it for decryption.
            if not retire(old, key):
                continue
        value = gen()
        generated[key] = value
        if key in entries:
            lines[entries[key][0]] = f"{key}={value}"
            replaced.append((key, problem))
        else:
            appended.append(f"{key}={value}")
    if ('ENCRYPTION_KEY' in generated and not entries.get('ENCRYPTION_KEY', (0, ''))[1]
            and entries.get('JWT_SECRET', (0, ''))[1]):
        via = ("the retired JWT_SECRET kept as a decrypt-only key" if 'JWT_SECRET' in generated
               else "its JWT_SECRET fallback (keep JWT_SECRET unchanged until then)")
        notes.append("ENCRYPTION_KEY was not set, so stored S3 credentials were encrypted with JWT_SECRET; "
                     f"the backend still reads them through {via} and re-encrypts them under the new "
                     "ENCRYPTION_KEY at startup")
    for key, value in decrypt_only.items():
        if key in entries:
            lines[entries[key][0]] = f"{key}={value}"
            replaced.append((key, 'updated with a retired secret'))
        else:
            appended.append(f"{key}={value}")
    for key, value in DEFAULT_KEYS.items():
        if key not in entries:
            appended.append(f"{key}={value}")

    if not replaced and not appended:
        print_success(".env already complete — nothing changed")
        return generated

    backup = f"{ENV_FILE}.bak.{time.strftime('%Y%m%d%H%M%S')}"
    fd = os.open(backup, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as f:
        f.write(original)
    if appended:
        lines.append('')
        lines.append(f"# Added by setup.py on {time.strftime('%Y-%m-%d %H:%M:%S')}")
        lines.extend(appended)
    write_env_atomically('\n'.join(lines) + '\n')

    for key, problem in replaced:
        if key in (DECRYPT_ONLY_PREVIOUS, DECRYPT_ONLY_LEGACY_JWT):
            print_info(f"{key} {problem}")
        else:
            print_warning(f"{key} was {problem} — generated a new value")
    for note in notes:
        print_warning(f"Note: {note}.")
    if decrypt_only:
        print_warning("Keep the decrypt-only value(s) in .env until the backend's startup log reports that no "
                      "stored credential needs them any more (it re-encrypts them under the new ENCRYPTION_KEY). "
                      "An older bkt release that does not know these variables cannot read credentials "
                      f"re-encrypted by a newer one — restore {backup} to roll back.")
    if any(k == 'DB_PASSWORD' for k, _ in replaced):
        print_warning("DB_PASSWORD changed: if Postgres was already initialised with the old value, "
                      f"restore it from {backup} or reset the Postgres volume.")
    for line in appended:
        print_info(f"Added {line.split('=', 1)[0]}")
    print_success(f"Updated .env (previous version saved to {backup}, mode 0600)")
    return generated

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
python3 setup.py --regenerate-certs
```

(Plain `python3 setup.py` keeps existing certificates and never rotates the
secrets in `.env`.)

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
.env.bak.*
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

def certificates_present():
    return all(os.path.exists(p) for p in (
        'certs/ca/ca.crt', 'certs/ca/ca.key',
        'certs/backend/backend.crt', 'certs/backend/backend.key',
        'certs/frontend/frontend.crt', 'certs/frontend/frontend.key',
        'certs/postgres/server.crt', 'certs/postgres/server.key', 'certs/postgres/ca.crt',
    ))

def print_summary(generated, certs_generated):
    """Print summary and next steps"""
    print()
    print("=" * 70)
    print_success("Object Storage Setup Complete!")
    print("=" * 70)
    print()
    if 'ADMIN_PASSWORD' in generated:
        print(f"{BLUE}Admin Credentials (new):{RESET}")
        print(f"  Username: admin (ADMIN_USERNAME in .env)")
        print(f"  Password: {generated['ADMIN_PASSWORD']}")
        print()
        print(f"{YELLOW}⚠️  IMPORTANT: Save these credentials securely!{RESET}")
        print()
    if generated:
        print(f"{BLUE}Generated into .env:{RESET} {', '.join(sorted(generated))}")
    else:
        print(f"{BLUE}.env:{RESET} unchanged (existing secrets kept)")
    print(f"{YELLOW}Back up .env — ENCRYPTION_KEY is required to decrypt stored S3 credentials.{RESET}")
    print()
    print(f"{BLUE}Certificates:{RESET} {'generated' if certs_generated else 'existing ones kept (use --regenerate-certs to replace)'}")
    print(f"  • certs/ca/ca.crt - Certificate Authority")
    print(f"  • certs/backend/backend.{{crt,key}} - Backend certificates")
    print(f"  • certs/frontend/frontend.{{crt,key}} - Frontend certificates")
    print(f"  • certs/postgres/server.{{crt,key}} - PostgreSQL certificates")
    print()
    print(f"{BLUE}Next Steps:{RESET}")
    print(f"  1. Review the .env file")
    print(f"  2. Start the services:")
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
    parser = argparse.ArgumentParser(
        description="Create or complete .env (valid secrets are generated once and never rotated; "
                    "a placeholder/default/short JWT_SECRET or ENCRYPTION_KEY is replaced and its old "
                    "value kept as a decrypt-only key) "
                    "and generate development TLS certificates.")
    parser.add_argument('--regenerate-certs', action='store_true',
                        help="replace existing certificates under certs/ (new CA: clients must re-trust it)")
    args = parser.parse_args()

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

    # Create .env, or fill in only what an existing one is missing
    generated = ensure_env_file()

    certs_generated = False
    if args.regenerate_certs or not certificates_present():
        generate_ca_certificate()
        generate_all_certificates()
        certs_generated = True
    else:
        print_info("Certificates already exist — keeping them (use --regenerate-certs to replace)")

    # Create certificate README
    create_certificate_readme()

    # Update gitignore
    update_gitignore()

    # Print summary
    print_summary(generated, certs_generated)

if __name__ == '__main__':
    main()
