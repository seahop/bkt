# Mounting BKT Buckets with s3fs-fuse

This guide explains how to mount your BKT buckets as local filesystems using s3fs-fuse.

## Prerequisites

1. **Install s3fs-fuse**

   On Ubuntu/Debian:
   ```bash
   sudo apt-get update
   sudo apt-get install s3fs
   ```

   On CentOS/RHEL:
   ```bash
   sudo yum install epel-release
   sudo yum install s3fs-fuse
   ```

   On macOS:
   ```bash
   brew install --cask macfuse
   brew install s3fs
   ```

2. **Have BKT access credentials**
   - You need an access key and secret key from your BKT installation
   - Generate these in the BKT web UI under "Access Keys"

## Setup

### 1. Create Credentials File

Create a credentials file at `~/.bkt` with your access key and secret key:

```bash
# Create the file
echo "YOUR_ACCESS_KEY:YOUR_SECRET_KEY" > ~/.bkt

# Set restrictive permissions (required by s3fs)
chmod 600 ~/.bkt
```

Replace `YOUR_ACCESS_KEY` and `YOUR_SECRET_KEY` with the actual values from the BKT web UI.

Example:
```bash
echo "AKxyz123abc:SKabc456def789" > ~/.bkt
chmod 600 ~/.bkt
```

### 2. Create Mount Point

Create a directory where you want to mount your bucket:

```bash
mkdir -p ~/bkt-mounts/my-bucket
```

### 3. Mount the Bucket

Mount your bucket using s3fs:

```bash
s3fs my-bucket ~/bkt-mounts/my-bucket \
  -o url=https://localhost:9443 \
  -o use_path_request_style \
  -o passwd_file=~/.bkt \
  -o ssl_verify_hostname=0 \
  -o no_check_certificate \
  -o allow_other \
  -o uid=$(id -u) \
  -o gid=$(id -g) \
  -o umask=0022
```

**If mounting with sudo** (required for some mount points like `/mnt`):

```bash
sudo s3fs my-bucket /mnt/my-bucket \
  -o url=https://localhost:9443 \
  -o use_path_request_style \
  -o passwd_file=/home/<your-username>/.bkt \
  -o ssl_verify_hostname=0 \
  -o no_check_certificate \
  -o allow_other \
  -o uid=$(id -u) \
  -o gid=$(id -g) \
  -o umask=0022
```

**Important:** When using `sudo`, use the full path to your credentials file (e.g., `/home/<your-username>/.bkt`) instead of `~/.bkt`, as `~` expands to root's home directory.

**Important Options:**
- `my-bucket`: Replace with your actual bucket name
- `-o url=https://localhost:9443`: Your BKT server URL
- `-o passwd_file=~/.bkt`: Path to your credentials file
- `-o use_path_request_style`: Required for BKT compatibility
- `-o ssl_verify_hostname=0`: Disable SSL hostname verification (for self-signed certs)
- `-o no_check_certificate`: Disable certificate verification (for self-signed certs)
- `-o allow_other`: Allow other users to access the mount (optional)
- `-o uid=$(id -u)`: Set file owner to your user ID
- `-o gid=$(id -g)`: Set file group to your group ID
- `-o umask=0022`: Set permissions (755 for directories, 644 for files)

### 4. Verify Mount

Check that your bucket is mounted:

```bash
df -h | grep my-bucket
ls -la ~/bkt-mounts/my-bucket
```

You should now be able to access your bucket files through the mount point!

## Usage

Once mounted, you can use the bucket like any other directory:

```bash
# List files
ls ~/bkt-mounts/my-bucket

# Copy files to bucket
cp /path/to/file.txt ~/bkt-mounts/my-bucket/

# Create directories
mkdir ~/bkt-mounts/my-bucket/subfolder

# Remove files
rm ~/bkt-mounts/my-bucket/file.txt
```

## Unmounting

To unmount the bucket:

```bash
fusermount -u ~/bkt-mounts/my-bucket
```

Or on macOS:
```bash
umount ~/bkt-mounts/my-bucket
```

## Auto-Mount on Boot

To automatically mount your bucket at boot, add an entry to `/etc/fstab`:

1. Edit `/etc/fstab`:
   ```bash
   sudo nano /etc/fstab
   ```

2. Add this line (adjust paths and options as needed):
   ```
   my-bucket /home/<your-username>/bkt-mounts/my-bucket fuse.s3fs _netdev,allow_other,use_path_request_style,url=https://localhost:9443,passwd_file=/home/<your-username>/.bkt,ssl_verify_hostname=0,no_check_certificate,uid=1000,gid=1000,umask=0022 0 0
   ```

   Replace `<your-username>` with your actual username and `uid=1000,gid=1000` with your actual user/group IDs (find them with `id -u` and `id -g`).

3. Test the fstab entry:
   ```bash
   sudo mount -a
   ```

## Troubleshooting

### Permission Denied

If you get "Permission Denied" errors:

**When accessing files/folders in the mount:**
- Check file ownership with `ls -la /path/to/mount/`
- If files are owned by `root`, you likely mounted with `sudo` without the `uid`/`gid` options
- Remount with proper ownership options:
  ```bash
  sudo fusermount -u /mnt/my-bucket
  sudo s3fs my-bucket /mnt/my-bucket \
    -o url=https://localhost:9443 \
    -o passwd_file=/home/<your-username>/.bkt \
    -o use_path_request_style \
    -o ssl_verify_hostname=0 \
    -o no_check_certificate \
    -o allow_other \
    -o uid=$(id -u) \
    -o gid=$(id -g) \
    -o umask=0022
  ```

**When mounting fails with permission denied:**
- Verify your credentials file permissions: `ls -l ~/.bkt` (should be `-rw-------`)
- Check that your access key and secret key are correct
- Verify that your user has the necessary policies attached in BKT
- When using `sudo`, use full path to credentials file (not `~/.bkt`)

### Transport endpoint is not connected

If the mount point becomes unresponsive:
```bash
fusermount -uz ~/bkt-mounts/my-bucket
# Wait a moment, then remount
s3fs my-bucket ~/bkt-mounts/my-bucket -o [options...]
```

### Cannot access bucket

- Ensure the bucket exists in BKT
- Verify you have read/write permissions on the bucket
- Check that your access key is active (not revoked)
- Verify the BKT server URL is correct and accessible

### SSL Certificate Errors

If you're using self-signed certificates (common in development):
- Use `-o no_check_certificate` and `-o ssl_verify_hostname=0`
- For production, consider using proper SSL certificates

### Debug Mode

To see detailed error messages, add the debug flag:
```bash
s3fs my-bucket ~/bkt-mounts/my-bucket \
  -o url=https://localhost:9443 \
  -o passwd_file=~/.bkt \
  -o use_path_request_style \
  -o dbglevel=info \
  -f
```

The `-f` flag runs s3fs in foreground mode so you can see log output.

## Performance Tips

For better performance:

1. **Enable caching**:
   ```bash
   s3fs my-bucket ~/bkt-mounts/my-bucket \
     -o url=https://localhost:9443 \
     -o passwd_file=~/.bkt \
     -o use_path_request_style \
     -o use_cache=/tmp/s3fs-cache \
     -o ensure_diskfree=10240
   ```

2. **Adjust timeout settings**:
   ```bash
   -o connect_timeout=10 \
   -o readwrite_timeout=30
   ```

## Security Best Practices

1. **Protect your credentials file**:
   - Always set `chmod 600 ~/.bkt`
   - Never share your secret key
   - Rotate access keys regularly

2. **Use dedicated access keys**:
   - Create separate access keys for different machines
   - Revoke access keys when no longer needed

3. **Limit permissions**:
   - Use BKT policies to restrict access to specific buckets
   - Follow principle of least privilege

4. **Use SSL in production**:
   - Always use HTTPS (never HTTP)
   - Use valid SSL certificates in production environments

## Example: Mount Multiple Buckets

Create a script to mount multiple buckets:

```bash
#!/bin/bash
# mount-bkt-buckets.sh

BUCKETS=("data" "backups" "media")
MOUNT_BASE=~/bkt-mounts
BKT_URL="https://localhost:9443"

for bucket in "${BUCKETS[@]}"; do
    echo "Mounting $bucket..."
    mkdir -p "$MOUNT_BASE/$bucket"

    s3fs "$bucket" "$MOUNT_BASE/$bucket" \
        -o url="$BKT_URL" \
        -o passwd_file=~/.bkt \
        -o use_path_request_style \
        -o ssl_verify_hostname=0 \
        -o no_check_certificate \
        -o allow_other \
        -o uid=$(id -u) \
        -o gid=$(id -g) \
        -o umask=0022

    if [ $? -eq 0 ]; then
        echo "Mounted $bucket successfully"
    else
        echo "Failed to mount $bucket"
    fi
done
```

Make it executable:
```bash
chmod +x mount-bkt-buckets.sh
./mount-bkt-buckets.sh
```

## Additional Resources

- s3fs-fuse documentation: https://github.com/s3fs-fuse/s3fs-fuse
- BKT policy documentation: See POLICIES.md in the docs folder
- BKT access key management: Access through the web UI

## Support

If you encounter issues:
1. Check the troubleshooting section above
2. Review BKT backend logs: `docker logs bkt-backend`
3. Run s3fs in debug mode (see Debug Mode section)
4. Verify your access key has the correct policies attached
