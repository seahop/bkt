-- Enable UUID extension
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Note: This file is for reference only.
-- GORM will handle the actual table creation through AutoMigrate.

-- Users table
-- CREATE TABLE IF NOT EXISTS users (
--     id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
--     username VARCHAR(255) UNIQUE NOT NULL,
--     email VARCHAR(255) UNIQUE NOT NULL,
--     password VARCHAR(255),
--     is_admin BOOLEAN DEFAULT FALSE,
--     is_locked BOOLEAN DEFAULT FALSE,
--     sso_provider VARCHAR(50),
--     sso_id VARCHAR(255),
--     sso_email VARCHAR(255),
--     created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
--     updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
-- );

-- Access keys table
-- CREATE TABLE IF NOT EXISTS access_keys (
--     id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
--     user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
--     access_key VARCHAR(255) UNIQUE NOT NULL,
--     secret_key_hash VARCHAR(255) NOT NULL,
--     is_active BOOLEAN DEFAULT TRUE,
--     last_used_at TIMESTAMP,
--     created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
-- );

-- S3 configurations table
-- CREATE TABLE IF NOT EXISTS s3_configurations (
--     id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
--     name VARCHAR(255) UNIQUE NOT NULL,
--     endpoint VARCHAR(255) NOT NULL,
--     region VARCHAR(255) NOT NULL,
--     access_key_id VARCHAR(255) NOT NULL,
--     secret_access_key VARCHAR(255) NOT NULL,
--     bucket_prefix VARCHAR(255),
--     use_ssl BOOLEAN DEFAULT TRUE,
--     force_path_style BOOLEAN DEFAULT FALSE,
--     is_default BOOLEAN DEFAULT FALSE,
--     created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
--     updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
-- );

-- Buckets table
-- CREATE TABLE IF NOT EXISTS buckets (
--     id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
--     name VARCHAR(255) UNIQUE NOT NULL,
--     owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
--     is_public BOOLEAN DEFAULT FALSE,
--     region VARCHAR(50) DEFAULT 'us-east-1',
--     storage_backend VARCHAR(50) DEFAULT 'local',
--     s3_config_id UUID REFERENCES s3_configurations(id),
--     created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
--     updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
-- );

-- Objects table
-- CREATE TABLE IF NOT EXISTS objects (
--     id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
--     bucket_id UUID NOT NULL REFERENCES buckets(id) ON DELETE CASCADE,
--     key VARCHAR(1024) NOT NULL,
--     size BIGINT NOT NULL,
--     content_type VARCHAR(255),
--     etag VARCHAR(255),
--     sha256 VARCHAR(64),
--     storage_path VARCHAR(1024) NOT NULL,
--     metadata JSONB,
--     created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
--     updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
--     UNIQUE(bucket_id, key)
-- );

-- Policies table
-- CREATE TABLE IF NOT EXISTS policies (
--     id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
--     name VARCHAR(255) UNIQUE NOT NULL,
--     description TEXT,
--     document JSONB NOT NULL,
--     created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
--     updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
-- );

-- User policies junction table
-- CREATE TABLE IF NOT EXISTS user_policies (
--     user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
--     policy_id UUID NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
--     PRIMARY KEY (user_id, policy_id)
-- );

-- Bucket policies table
-- CREATE TABLE IF NOT EXISTS bucket_policies (
--     bucket_id UUID PRIMARY KEY REFERENCES buckets(id) ON DELETE CASCADE,
--     policy_document JSONB NOT NULL,
--     updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
-- );

-- Create indexes for better query performance
-- CREATE INDEX IF NOT EXISTS idx_users_sso_provider ON users(sso_provider);
-- CREATE INDEX IF NOT EXISTS idx_users_sso_id ON users(sso_id);
-- CREATE INDEX IF NOT EXISTS idx_access_keys_user_id ON access_keys(user_id);
-- CREATE INDEX IF NOT EXISTS idx_buckets_owner_id ON buckets(owner_id);
-- CREATE INDEX IF NOT EXISTS idx_objects_bucket_id ON objects(bucket_id);
-- CREATE INDEX IF NOT EXISTS idx_objects_bucket_key ON objects(bucket_id, key);
