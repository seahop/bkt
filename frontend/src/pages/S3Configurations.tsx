import { useState, useEffect } from 'react';
import { Database, Plus, Trash2, Edit2, AlertCircle, Check, X, Server } from 'lucide-react';
import { s3ConfigApi } from '../services/api';
import type { S3Configuration } from '../types';
import { useAuthStore } from '../store/authStore';

export default function S3Configurations() {
  const [configs, setConfigs] = useState<S3Configuration[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [showCreateModal, setShowCreateModal] = useState(false);
  const [editingConfig, setEditingConfig] = useState<S3Configuration | null>(null);
  const { user } = useAuthStore();

  useEffect(() => {
    fetchConfigs();
  }, []);

  const fetchConfigs = async () => {
    try {
      setLoading(true);
      const data = await s3ConfigApi.listS3Configs();
      setConfigs(data || []);
      setError('');
    } catch (err: any) {
      console.error('Failed to fetch S3 configurations:', err);
      setError(err.response?.data?.message || 'Failed to load S3 configurations');
    } finally {
      setLoading(false);
    }
  };

  const handleDeleteConfig = async (id: string) => {
    if (!confirm('Are you sure you want to delete this S3 configuration? This will fail if any buckets are using it.')) return;

    try {
      await s3ConfigApi.deleteS3Config(id);
      await fetchConfigs();
    } catch (err: any) {
      alert(err.response?.data?.message || 'Failed to delete S3 configuration');
    }
  };

  if (!user?.is_admin) {
    return (
      <div className="p-8">
        <div className="bg-red-500/10 border border-red-500 text-red-500 px-4 py-3 rounded-lg flex items-center gap-2">
          <AlertCircle className="w-5 h-5" />
          Only administrators can manage S3 configurations
        </div>
      </div>
    );
  }

  return (
    <div className="p-8">
      <div className="mb-8 flex justify-between items-center">
        <div>
          <h1 className="text-3xl font-bold text-dark-text mb-2">S3 Configurations</h1>
          <p className="text-dark-textSecondary">Manage S3-compatible storage backend configurations</p>
        </div>
        <button
          onClick={() => setShowCreateModal(true)}
          className="flex items-center gap-2 px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded-lg transition-colors"
        >
          <Plus className="w-5 h-5" />
          Add Configuration
        </button>
      </div>

      {error && (
        <div className="mb-6 bg-red-500/10 border border-red-500 text-red-500 px-4 py-3 rounded-lg flex items-center gap-2">
          <AlertCircle className="w-5 h-5" />
          {error}
        </div>
      )}

      {loading ? (
        <div className="bg-dark-surface border border-dark-border rounded-lg p-12 text-center">
          <Database className="w-16 h-16 text-dark-textSecondary mx-auto mb-4 opacity-50 animate-pulse" />
          <p className="text-dark-textSecondary">Loading configurations...</p>
        </div>
      ) : configs.length === 0 ? (
        <div className="bg-dark-surface border border-dark-border rounded-lg p-12 text-center">
          <Database className="w-16 h-16 text-dark-textSecondary mx-auto mb-4 opacity-50" />
          <h2 className="text-xl font-semibold text-dark-text mb-2">No S3 Configurations</h2>
          <p className="text-dark-textSecondary mb-4">
            Add S3 configurations to enable buckets to use different S3-compatible storage backends
          </p>
          <button
            onClick={() => setShowCreateModal(true)}
            className="inline-flex items-center gap-2 px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded-lg transition-colors"
          >
            <Plus className="w-5 h-5" />
            Add First Configuration
          </button>
        </div>
      ) : (
        <div className="grid gap-4">
          {configs.map((config) => (
            <div
              key={config.id}
              className="bg-dark-surface border border-dark-border rounded-lg p-6 hover:border-blue-500/50 transition-colors"
            >
              <div className="flex items-start justify-between">
                <div className="flex-1">
                  <div className="flex items-center gap-3 mb-2">
                    <Server className="w-5 h-5 text-blue-500" />
                    <h3 className="text-lg font-semibold text-dark-text">{config.name}</h3>
                    {config.is_default && (
                      <span className="text-xs bg-green-500/10 text-green-500 px-2 py-1 rounded">
                        Default
                      </span>
                    )}
                  </div>
                  <div className="grid grid-cols-2 gap-4 text-sm">
                    <div>
                      <span className="text-dark-textSecondary">Endpoint: </span>
                      <span className="text-dark-text">{config.endpoint}</span>
                    </div>
                    <div>
                      <span className="text-dark-textSecondary">Region: </span>
                      <span className="text-dark-text">{config.region}</span>
                    </div>
                    <div>
                      <span className="text-dark-textSecondary">Access Key: </span>
                      <span className="text-dark-text font-mono text-xs">{config.access_key_id}</span>
                    </div>
                    <div>
                      <span className="text-dark-textSecondary">Bucket Prefix: </span>
                      <span className="text-dark-text">{config.bucket_prefix || 'None'}</span>
                    </div>
                    <div>
                      <span className="text-dark-textSecondary">SSL: </span>
                      {config.use_ssl ? (
                        <Check className="w-4 h-4 inline text-green-500" />
                      ) : (
                        <X className="w-4 h-4 inline text-red-500" />
                      )}
                    </div>
                    <div>
                      <span className="text-dark-textSecondary">Path Style: </span>
                      {config.force_path_style ? (
                        <Check className="w-4 h-4 inline text-green-500" />
                      ) : (
                        <X className="w-4 h-4 inline text-red-500" />
                      )}
                    </div>
                  </div>
                  <div className="mt-4 text-xs text-dark-textSecondary">
                    Created: {new Date(config.created_at).toLocaleString()}
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <button
                    onClick={() => setEditingConfig(config)}
                    className="p-2 hover:bg-dark-bg rounded-lg transition-colors text-dark-textSecondary hover:text-blue-500"
                    title="Edit configuration"
                  >
                    <Edit2 className="w-5 h-5" />
                  </button>
                  <button
                    onClick={() => handleDeleteConfig(config.id)}
                    className="p-2 hover:bg-dark-bg rounded-lg transition-colors text-dark-textSecondary hover:text-red-500"
                    title="Delete configuration"
                  >
                    <Trash2 className="w-5 h-5" />
                  </button>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Create/Edit Modal */}
      {(showCreateModal || editingConfig) && (
        <S3ConfigModal
          config={editingConfig}
          onClose={() => {
            setShowCreateModal(false);
            setEditingConfig(null);
          }}
          onSuccess={() => {
            setShowCreateModal(false);
            setEditingConfig(null);
            fetchConfigs();
          }}
        />
      )}
    </div>
  );
}

interface S3ConfigModalProps {
  config: S3Configuration | null;
  onClose: () => void;
  onSuccess: () => void;
}

function S3ConfigModal({ config, onClose, onSuccess }: S3ConfigModalProps) {
  const [formData, setFormData] = useState({
    name: config?.name || '',
    endpoint: config?.endpoint || '',
    region: config?.region || '',
    access_key_id: config?.access_key_id || '',
    secret_access_key: '',
    bucket_prefix: config?.bucket_prefix || '',
    use_ssl: config?.use_ssl ?? true,
    force_path_style: config?.force_path_style ?? false,
    is_default: config?.is_default ?? false,
  });
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setSubmitting(true);

    try {
      if (config) {
        // Update existing config
        const updateData: any = { ...formData };
        // Only send secret if it's been changed
        if (!formData.secret_access_key) {
          delete updateData.secret_access_key;
        }
        await s3ConfigApi.updateS3Config(config.id, updateData);
      } else {
        // Create new config
        if (!formData.secret_access_key) {
          setError('Secret access key is required for new configurations');
          setSubmitting(false);
          return;
        }
        await s3ConfigApi.createS3Config(formData);
      }
      onSuccess();
    } catch (err: any) {
      setError(err.response?.data?.message || `Failed to ${config ? 'update' : 'create'} configuration`);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
      <div className="bg-dark-surface border border-dark-border rounded-lg max-w-2xl w-full max-h-[90vh] overflow-y-auto">
        <div className="p-6 border-b border-dark-border">
          <h2 className="text-2xl font-bold text-dark-text">
            {config ? 'Edit S3 Configuration' : 'Add S3 Configuration'}
          </h2>
        </div>

        <form onSubmit={handleSubmit} className="p-6 space-y-4">
          {error && (
            <div className="bg-red-500/10 border border-red-500 text-red-500 px-4 py-3 rounded-lg flex items-center gap-2">
              <AlertCircle className="w-5 h-5" />
              {error}
            </div>
          )}

          <div>
            <label className="block text-sm font-medium text-dark-text mb-2">
              Configuration Name *
            </label>
            <input
              type="text"
              required
              value={formData.name}
              onChange={(e) => setFormData({ ...formData, name: e.target.value })}
              className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
              placeholder="e.g., AWS S3 Production, MinIO Development"
            />
          </div>

          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="block text-sm font-medium text-dark-text mb-2">
                Endpoint *
              </label>
              <input
                type="text"
                required
                value={formData.endpoint}
                onChange={(e) => setFormData({ ...formData, endpoint: e.target.value })}
                className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
                placeholder="s3.amazonaws.com"
              />
            </div>

            <div>
              <label className="block text-sm font-medium text-dark-text mb-2">
                Region *
              </label>
              <input
                type="text"
                required
                value={formData.region}
                onChange={(e) => setFormData({ ...formData, region: e.target.value })}
                className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
                placeholder="us-east-1"
              />
            </div>
          </div>

          <div>
            <label className="block text-sm font-medium text-dark-text mb-2">
              Access Key ID *
            </label>
            <input
              type="text"
              required
              value={formData.access_key_id}
              onChange={(e) => setFormData({ ...formData, access_key_id: e.target.value })}
              className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500 font-mono text-sm"
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-dark-text mb-2">
              Secret Access Key {config && '(leave blank to keep current)'}
            </label>
            <input
              type="password"
              required={!config}
              value={formData.secret_access_key}
              onChange={(e) => setFormData({ ...formData, secret_access_key: e.target.value })}
              className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500 font-mono text-sm"
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-dark-text mb-2">
              Bucket Prefix (optional)
            </label>
            <input
              type="text"
              value={formData.bucket_prefix}
              onChange={(e) => setFormData({ ...formData, bucket_prefix: e.target.value })}
              className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
              placeholder="objectstore-"
            />
          </div>

          <div className="grid grid-cols-2 gap-4">
            <label className="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={formData.use_ssl}
                onChange={(e) => setFormData({ ...formData, use_ssl: e.target.checked })}
                className="w-4 h-4 rounded border-dark-border bg-dark-bg text-blue-600 focus:ring-2 focus:ring-blue-500"
              />
              <span className="text-sm text-dark-text">Use SSL/TLS</span>
            </label>

            <label className="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={formData.force_path_style}
                onChange={(e) => setFormData({ ...formData, force_path_style: e.target.checked })}
                className="w-4 h-4 rounded border-dark-border bg-dark-bg text-blue-600 focus:ring-2 focus:ring-blue-500"
              />
              <span className="text-sm text-dark-text">Force Path Style</span>
            </label>
          </div>

          <label className="flex items-center gap-2 cursor-pointer">
            <input
              type="checkbox"
              checked={formData.is_default}
              onChange={(e) => setFormData({ ...formData, is_default: e.target.checked })}
              className="w-4 h-4 rounded border-dark-border bg-dark-bg text-blue-600 focus:ring-2 focus:ring-blue-500"
            />
            <span className="text-sm text-dark-text">Set as default configuration</span>
          </label>

          <div className="flex gap-3 pt-4">
            <button
              type="submit"
              disabled={submitting}
              className="flex-1 px-4 py-2 bg-blue-600 hover:bg-blue-700 disabled:bg-blue-600/50 text-white rounded-lg transition-colors"
            >
              {submitting ? 'Saving...' : config ? 'Update Configuration' : 'Create Configuration'}
            </button>
            <button
              type="button"
              onClick={onClose}
              className="flex-1 px-4 py-2 bg-dark-bg hover:bg-dark-border text-dark-text rounded-lg transition-colors"
            >
              Cancel
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
