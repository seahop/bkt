#!/bin/bash
set -e

# PostgreSQL SSL Certificate Setup Script
# This script copies SSL certificates and sets proper permissions

echo "Setting up SSL certificates for PostgreSQL..."

# Create certs directory if it doesn't exist
mkdir -p /var/lib/postgresql/certs

# Copy certificates from mounted volume to internal directory
if [ -d "/ssl-certs" ]; then
    cp /ssl-certs/server.crt /var/lib/postgresql/certs/server.crt
    cp /ssl-certs/server.key /var/lib/postgresql/certs/server.key
    cp /ssl-certs/ca.crt /var/lib/postgresql/certs/ca.crt

    # Set proper ownership and permissions
    chown postgres:postgres /var/lib/postgresql/certs/server.crt
    chown postgres:postgres /var/lib/postgresql/certs/server.key
    chown postgres:postgres /var/lib/postgresql/certs/ca.crt

    chmod 600 /var/lib/postgresql/certs/server.key
    chmod 644 /var/lib/postgresql/certs/server.crt
    chmod 644 /var/lib/postgresql/certs/ca.crt

    echo "SSL certificates configured successfully"
else
    echo "Warning: /ssl-certs directory not found"
fi

# Execute the original PostgreSQL entrypoint
exec docker-entrypoint.sh "$@"
