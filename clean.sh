#!/bin/bash

# bkt - Clean Installation Script
# This script removes all data and allows for a fresh installation

set -e

echo "=========================================="
echo "bkt - Clean Installation"
echo "=========================================="
echo ""
echo "This will:"
echo "  • Stop all containers"
echo "  • Remove all volumes"
echo "  • Clean PostgreSQL data directory"
echo "  • Remove bucket storage data"
echo ""
read -p "Are you sure you want to continue? (yes/no): " confirm

if [ "$confirm" != "yes" ]; then
    echo "Cancelled."
    exit 0
fi

echo ""
echo "Stopping containers..."
docker compose down -v

echo "Cleaning PostgreSQL data..."
if [ -d "data/postgres" ]; then
    docker run --rm -v "$(pwd)/data/postgres:/data" alpine sh -c "rm -rf /data/* /data/.*" 2>/dev/null || true
    echo "✓ PostgreSQL data cleaned"
fi

echo "Cleaning bucket storage..."
if [ -d "data/buckets" ]; then
    docker run --rm -v "$(pwd)/data/buckets:/data" alpine sh -c "rm -rf /data/*" 2>/dev/null || true
    echo "✓ Bucket storage cleaned"
fi

echo ""
echo "=========================================="
echo "✓ Clean completed successfully!"
echo "=========================================="
echo ""
echo "Next steps:"
echo "  1. Run: python3 setup.py"
echo "  2. Run: docker compose up -d"
echo ""
