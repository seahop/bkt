import { useState, useEffect } from 'react';
import { Plus, Trash2, Edit2, AlertCircle, X, Server } from 'lucide-react';
import { s3ConfigApi } from '../services/api';
import type { S3Configuration } from '../types';
import { useAuthStore } from '../store/authStore';
import { getErrorMessage } from '../utils/errors';

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
      setError(getErrorMessage(err, 'Failed to load S3 configurations'));
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
      alert(getErrorMessage(err, 'Failed to delete S3 configuration'));
    }
  };

  if (!user?.is_admin) {
    return (
      <div className="page">
        <div className="alert-error">
          <AlertCircle className="w-4 h-4 shrink-0 mt-0.5" />
          Only administrators can manage S3 configurations
        </div>
      </div>
    );
  }

  return (
    <div className="page">
      <div className="flex items-start justify-between gap-4 mb-8">
        <div>
          <h1 className="page-title">S3 Configurations</h1>
          <p className="page-subtitle">Manage S3-compatible storage backend configurations</p>
        </div>
        <button onClick={() => setShowCreateModal(true)} className="btn-primary">
          <Plus className="w-4 h-4" />
          Add Configuration
        </button>
      </div>

      {error && (
        <div className="alert-error mb-6">
          <AlertCircle className="w-4 h-4 shrink-0 mt-0.5" />
          {error}
        </div>
      )}

      {loading ? (
        <div className="flex flex-col items-center justify-center h-64 gap-3">
          <div className="spinner" />
          <p className="text-sm text-dark-textSecondary">Loading configurations…</p>
        </div>
      ) : configs.length === 0 ? (
        <div className="card empty-state">
          <Server className="empty-state-icon" />
          <h3 className="text-base font-semibold text-dark-text mb-1">No S3 configurations</h3>
          <p className="text-sm text-dark-textSecondary mb-5 max-w-sm">
            Add S3 configurations to enable buckets to use different S3-compatible storage backends.
          </p>
          <button onClick={() => setShowCreateModal(true)} className="btn-secondary">
            <Plus className="w-4 h-4" />
            Add First Configuration
          </button>
        </div>
      ) : (
        <div className="grid gap-4">
          {configs.map((config) => (
            <div
              key={config.id}
              className="card p-6 hover:border-dark-borderStrong transition-colors"
            >
              <div className="flex items-start justify-between gap-4">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-3 mb-4">
                    <span className="bg-blue-500/10 text-blue-500 p-2 rounded-lg">
                      <Server className="w-4 h-4" />
                    </span>
                    <h3 className="text-base font-semibold text-dark-text truncate">{config.name}</h3>
                    {config.is_default && <span className="badge-blue">Default</span>}
                    {config.use_ssl && <span className="badge-gray">SSL</span>}
                    {config.force_path_style && <span className="badge-gray">Path style</span>}
                  </div>
                  <div className="grid grid-cols-2 gap-x-6 gap-y-3">
                    <div>
                      <p className="text-xs text-dark-textMuted uppercase tracking-wider mb-0.5">Endpoint</p>
                      <p className="font-mono text-sm text-dark-text truncate">{config.endpoint}</p>
                    </div>
                    <div>
                      <p className="text-xs text-dark-textMuted uppercase tracking-wider mb-0.5">Region</p>
                      <p className="font-mono text-sm text-dark-text">{config.region}</p>
                    </div>
                    <div>
                      <p className="text-xs text-dark-textMuted uppercase tracking-wider mb-0.5">Access Key</p>
                      <p className="font-mono text-sm text-dark-text">
                        {config.access_key_id.length > 8
                          ? `${config.access_key_id.slice(0, 4)}****${config.access_key_id.slice(-4)}`
                          : '****'}
                      </p>
                    </div>
                    <div>
                      <p className="text-xs text-dark-textMuted uppercase tracking-wider mb-0.5">Bucket Prefix</p>
                      {config.bucket_prefix ? (
                        <p className="font-mono text-sm text-dark-text truncate">{config.bucket_prefix}</p>
                      ) : (
                        <p className="text-sm text-dark-textMuted">None</p>
                      )}
                    </div>
                  </div>
                  <p className="mt-4 text-xs text-dark-textMuted tabular-nums">
                    Created: {new Date(config.created_at).toLocaleString()}
                  </p>
                </div>
                <div className="flex items-center gap-1 shrink-0">
                  <button
                    onClick={() => setEditingConfig(config)}
                    className="btn-icon"
                    title="Edit configuration"
                  >
                    <Edit2 className="w-4 h-4" />
                  </button>
                  <button
                    onClick={() => handleDeleteConfig(config.id)}
                    className="btn-icon hover:!text-red-400 hover:!bg-red-500/10"
                    title="Delete configuration"
                  >
                    <Trash2 className="w-4 h-4" />
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
      setError(getErrorMessage(err, `Failed to ${config ? 'update' : 'create'} configuration`));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="modal-overlay">
      <div className="modal-panel !max-w-2xl">
        <div className="flex items-center justify-between mb-5">
          <h2 className="modal-title">
            {config ? 'Edit S3 Configuration' : 'Add S3 Configuration'}
          </h2>
          <button type="button" onClick={onClose} className="btn-icon">
            <X className="w-4 h-4" />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          {error && (
            <div className="alert-error">
              <AlertCircle className="w-4 h-4 shrink-0 mt-0.5" />
              {error}
            </div>
          )}

          <div>
            <label className="label">Configuration Name *</label>
            <input
              type="text"
              required
              value={formData.name}
              onChange={(e) => setFormData({ ...formData, name: e.target.value })}
              className="input"
              placeholder="e.g., AWS S3 Production, MinIO Development"
            />
          </div>

          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="label">Endpoint *</label>
              <input
                type="text"
                required
                value={formData.endpoint}
                onChange={(e) => setFormData({ ...formData, endpoint: e.target.value })}
                className="input font-mono"
                placeholder="s3.amazonaws.com"
              />
            </div>

            <div>
              <label className="label">Region *</label>
              <input
                type="text"
                required
                value={formData.region}
                onChange={(e) => setFormData({ ...formData, region: e.target.value })}
                className="input font-mono"
                placeholder="us-east-1"
              />
            </div>
          </div>

          <div>
            <label className="label">Access Key ID *</label>
            <input
              type="text"
              required
              value={formData.access_key_id}
              onChange={(e) => setFormData({ ...formData, access_key_id: e.target.value })}
              className="input font-mono"
            />
          </div>

          <div>
            <label className="label">
              Secret Access Key {config && '(leave blank to keep current)'}
            </label>
            <input
              type="password"
              required={!config}
              value={formData.secret_access_key}
              onChange={(e) => setFormData({ ...formData, secret_access_key: e.target.value })}
              className="input font-mono"
            />
          </div>

          <div>
            <label className="label">Bucket Prefix (optional)</label>
            <input
              type="text"
              value={formData.bucket_prefix}
              onChange={(e) => setFormData({ ...formData, bucket_prefix: e.target.value })}
              className="input font-mono"
              placeholder="objectstore-"
            />
          </div>

          <div className="grid grid-cols-2 gap-4">
            <label className="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={formData.use_ssl}
                onChange={(e) => setFormData({ ...formData, use_ssl: e.target.checked })}
                className="w-4 h-4 rounded border-dark-border bg-dark-inset text-blue-600 focus:ring-2 focus:ring-blue-500"
              />
              <span className="text-sm text-dark-text">Use SSL/TLS</span>
            </label>

            <label className="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={formData.force_path_style}
                onChange={(e) => setFormData({ ...formData, force_path_style: e.target.checked })}
                className="w-4 h-4 rounded border-dark-border bg-dark-inset text-blue-600 focus:ring-2 focus:ring-blue-500"
              />
              <span className="text-sm text-dark-text">Force Path Style</span>
            </label>
          </div>

          <label className="flex items-center gap-2 cursor-pointer">
            <input
              type="checkbox"
              checked={formData.is_default}
              onChange={(e) => setFormData({ ...formData, is_default: e.target.checked })}
              className="w-4 h-4 rounded border-dark-border bg-dark-inset text-blue-600 focus:ring-2 focus:ring-blue-500"
            />
            <span className="text-sm text-dark-text">Set as default configuration</span>
          </label>

          <div className="flex justify-end gap-2 pt-4">
            <button type="button" onClick={onClose} className="btn-ghost">
              Cancel
            </button>
            <button type="submit" disabled={submitting} className="btn-primary">
              {submitting && <span className="spinner !w-4 !h-4" />}
              {submitting ? 'Saving...' : config ? 'Update Configuration' : 'Create Configuration'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
