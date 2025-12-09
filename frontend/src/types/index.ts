export interface User {
  id: string
  username: string
  email: string
  is_admin: boolean
  created_at: string
  updated_at: string
}

export interface AuthResponse {
  token: string
  refresh_token?: string
  user: User
}

export interface S3Configuration {
  id: string
  name: string
  endpoint: string
  region: string
  access_key_id: string
  bucket_prefix?: string
  use_ssl: boolean
  force_path_style: boolean
  is_default: boolean
  created_at: string
  updated_at: string
}

export interface Bucket {
  id: string
  name: string
  owner_id: string
  is_public: boolean
  region: string
  storage_backend: string
  s3_config_id?: string
  created_at: string
  updated_at: string
  owner?: User
  s3_config?: S3Configuration
}

export interface Object {
  id: string
  bucket_id: string
  key: string
  size: number
  content_type: string
  etag: string
  metadata?: Record<string, any>
  created_at: string
  updated_at: string
}

export interface AccessKey {
  id: string
  user_id: string
  access_key: string
  is_active: boolean
  last_used_at?: string
  created_at: string
}

export interface AccessKeyResponse {
  access_key: string
  secret_key: string
  created_at: string
}

export interface Policy {
  id: string
  name: string
  document: string
  created_at: string
  updated_at: string
}

export interface ApiError {
  error: string
  message?: string
}
