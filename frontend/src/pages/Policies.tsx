import { useState, useEffect } from 'react';
import { Shield, Plus, Trash2, Edit, FileText, AlertCircle, FolderOpen, User as UserIcon } from 'lucide-react';
import { listPolicies, createPolicy, deletePolicy, getPolicyTemplates, Policy, attachPolicyToUser } from '../services/policy';
import { useAuthStore } from '../store/authStore';
import { bucketApi, userApi } from '../services/api';
import type { Bucket, User } from '../types';

export default function Policies() {
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [showCreateModal, setShowCreateModal] = useState(false);
  const [selectedPolicy, setSelectedPolicy] = useState<Policy | null>(null);
  const { user } = useAuthStore();

  useEffect(() => {
    fetchPolicies();
  }, []);

  const fetchPolicies = async () => {
    try {
      setLoading(true);
      const data = await listPolicies();
      setPolicies(data || []);
      setError('');
    } catch (err: any) {
      console.error('Failed to fetch policies:', err);
      setError(err.response?.data?.message || 'Failed to load policies');
    } finally {
      setLoading(false);
    }
  };

  const handleDeletePolicy = async (id: string) => {
    if (!confirm('Are you sure you want to delete this policy?')) return;

    try {
      await deletePolicy(id);
      await fetchPolicies();
    } catch (err: any) {
      alert(err.response?.data?.message || 'Failed to delete policy');
    }
  };

  const handleViewPolicy = (policy: Policy) => {
    setSelectedPolicy(policy);
  };

  return (
    <div className="p-8">
      <div className="mb-8 flex justify-between items-center">
        <div>
          <h1 className="text-3xl font-bold text-dark-text mb-2">Policies</h1>
          <p className="text-dark-textSecondary">Manage IAM-style access control policies</p>
        </div>
        {user?.is_admin && (
          <button
            onClick={() => setShowCreateModal(true)}
            className="flex items-center gap-2 px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded-lg transition-colors"
          >
            <Plus className="w-5 h-5" />
            Create Policy
          </button>
        )}
      </div>

      {error && (
        <div className="mb-6 bg-red-500/10 border border-red-500 text-red-500 px-4 py-3 rounded-lg flex items-center gap-2">
          <AlertCircle className="w-5 h-5" />
          {error}
        </div>
      )}

      {loading ? (
        <div className="bg-dark-surface border border-dark-border rounded-lg p-12 text-center">
          <Shield className="w-16 h-16 text-dark-textSecondary mx-auto mb-4 opacity-50 animate-pulse" />
          <p className="text-dark-textSecondary">Loading policies...</p>
        </div>
      ) : policies.length === 0 ? (
        <div className="bg-dark-surface border border-dark-border rounded-lg p-12 text-center">
          <Shield className="w-16 h-16 text-dark-textSecondary mx-auto mb-4 opacity-50" />
          <h2 className="text-xl font-semibold text-dark-text mb-2">No Policies Yet</h2>
          <p className="text-dark-textSecondary mb-4">
            {user?.is_admin
              ? 'Create your first policy to control access to buckets and objects'
              : 'No policies have been assigned to you'}
          </p>
          {user?.is_admin && (
            <button
              onClick={() => setShowCreateModal(true)}
              className="inline-flex items-center gap-2 px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded-lg transition-colors"
            >
              <Plus className="w-5 h-5" />
              Create First Policy
            </button>
          )}
        </div>
      ) : (
        <div className="grid gap-4">
          {policies.map((policy) => (
            <div
              key={policy.id}
              className="bg-dark-surface border border-dark-border rounded-lg p-6 hover:border-blue-500/50 transition-colors"
            >
              <div className="flex items-start justify-between">
                <div className="flex-1">
                  <div className="flex items-center gap-3 mb-2">
                    <Shield className="w-5 h-5 text-blue-500" />
                    <h3 className="text-lg font-semibold text-dark-text">{policy.name}</h3>
                  </div>
                  <p className="text-dark-textSecondary mb-4">{policy.description}</p>
                  <div className="flex items-center gap-4 text-sm text-dark-textSecondary">
                    <span>Created: {new Date(policy.created_at).toLocaleDateString()}</span>
                    <span>Updated: {new Date(policy.updated_at).toLocaleDateString()}</span>
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <button
                    onClick={() => handleViewPolicy(policy)}
                    className="p-2 hover:bg-dark-bg rounded-lg transition-colors text-dark-textSecondary hover:text-blue-500"
                    title="View policy document"
                  >
                    <FileText className="w-5 h-5" />
                  </button>
                  {user?.is_admin && (
                    <button
                      onClick={() => handleDeletePolicy(policy.id)}
                      className="p-2 hover:bg-dark-bg rounded-lg transition-colors text-dark-textSecondary hover:text-red-500"
                      title="Delete policy"
                    >
                      <Trash2 className="w-5 h-5" />
                    </button>
                  )}
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Create Policy Modal */}
      {showCreateModal && (
        <CreatePolicyModal
          onClose={() => setShowCreateModal(false)}
          onSuccess={() => {
            setShowCreateModal(false);
            fetchPolicies();
          }}
        />
      )}

      {/* View Policy Modal */}
      {selectedPolicy && (
        <ViewPolicyModal
          policy={selectedPolicy}
          onClose={() => setSelectedPolicy(null)}
        />
      )}
    </div>
  );
}

// S3 Action definitions with categories
const S3_ACTIONS = {
  read: [
    { action: 's3:GetObject', label: 'Get Object', description: 'Download objects' },
    { action: 's3:ListBucket', label: 'List Bucket', description: 'List objects in bucket' },
    { action: 's3:HeadObject', label: 'Head Object', description: 'Get object metadata' },
    { action: 's3:GetBucketLocation', label: 'Get Bucket Location', description: 'Get bucket region' },
  ],
  write: [
    { action: 's3:PutObject', label: 'Put Object', description: 'Upload objects' },
    { action: 's3:DeleteObject', label: 'Delete Object', description: 'Delete objects' },
  ],
  bucket: [
    { action: 's3:CreateBucket', label: 'Create Bucket', description: 'Create new buckets' },
    { action: 's3:DeleteBucket', label: 'Delete Bucket', description: 'Delete buckets' },
    { action: 's3:PutBucketPolicy', label: 'Put Bucket Policy', description: 'Set bucket policies' },
    { action: 's3:GetBucketPolicy', label: 'Get Bucket Policy', description: 'Get bucket policies' },
  ],
};

function CreatePolicyModal({ onClose, onSuccess }: { onClose: () => void; onSuccess: () => void }) {
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [document, setDocument] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [buckets, setBuckets] = useState<Bucket[]>([]);
  const [selectedBucket, setSelectedBucket] = useState<string>('');
  const [loadingBuckets, setLoadingBuckets] = useState(true);
  const [users, setUsers] = useState<User[]>([]);
  const [selectedUserId, setSelectedUserId] = useState<string>('');
  const [loadingUsers, setLoadingUsers] = useState(true);
  const [selectedActions, setSelectedActions] = useState<string[]>([]);
  const [effect, setEffect] = useState<'Allow' | 'Deny'>('Allow');
  const templates = getPolicyTemplates();

  useEffect(() => {
    const fetchData = async () => {
      try {
        const [bucketsData, usersData] = await Promise.all([
          bucketApi.listBuckets(),
          userApi.listUsers()
        ]);
        setBuckets(bucketsData || []);
        setUsers(usersData || []);
      } catch (err) {
        console.error('Failed to fetch data:', err);
      } finally {
        setLoadingBuckets(false);
        setLoadingUsers(false);
      }
    };
    fetchData();
  }, []);

  // Auto-update policy document when actions change
  useEffect(() => {
    if (selectedActions.length > 0) {
      updatePolicyFromActions();
    }
  }, [selectedActions, effect, selectedBucket, selectedUserId]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');

    // Validate policy document
    if (!document || document.trim() === '') {
      setError('Please select at least one action or provide a custom policy document');
      return;
    }

    setLoading(true);

    try {
      // Create the policy
      const createdPolicy = await createPolicy({ name, description, document });

      // If a user was selected, attach the policy to that user
      if (selectedUserId) {
        await attachPolicyToUser(selectedUserId, createdPolicy.id);
      }

      onSuccess();
    } catch (err: any) {
      setError(err.response?.data?.message || 'Failed to create policy');
    } finally {
      setLoading(false);
    }
  };

  const handleBucketSelect = (bucketName: string) => {
    setSelectedBucket(bucketName);
    if (bucketName) {
      // Auto-fill with specific bucket template
      const template = templates.specificBucket(bucketName);
      const selectedUser = users.find(u => u.id === selectedUserId);

      // Include user in naming if selected
      if (selectedUser) {
        setName(`${selectedUser.username} - ${template.name}`);
        setDescription(`${template.description} for user ${selectedUser.username}`);
      } else {
        setName(template.name);
        setDescription(template.description);
      }

      setDocument(template.document);
    }
  };

  const handleUserSelect = (userId: string) => {
    setSelectedUserId(userId);

    // Update policy name/description if bucket is already selected
    if (selectedBucket) {
      const template = templates.specificBucket(selectedBucket);
      const selectedUser = users.find(u => u.id === userId);

      if (selectedUser) {
        setName(`${selectedUser.username} - ${template.name}`);
        setDescription(`${template.description} for user ${selectedUser.username}`);
      } else {
        setName(template.name);
        setDescription(template.description);
      }
    }
  };

  const handleActionToggle = (action: string) => {
    setSelectedActions(prev =>
      prev.includes(action)
        ? prev.filter(a => a !== action)
        : [...prev, action]
    );
  };

  const handleSelectAllActions = () => {
    const allActions = [
      ...S3_ACTIONS.read.map(a => a.action),
      ...S3_ACTIONS.write.map(a => a.action),
      ...S3_ACTIONS.bucket.map(a => a.action),
    ];
    setSelectedActions(allActions);
  };

  const handleSelectCategoryActions = (category: 'read' | 'write' | 'bucket') => {
    const categoryActions = S3_ACTIONS[category].map(a => a.action);
    const allSelected = categoryActions.every(action => selectedActions.includes(action));

    if (allSelected) {
      // Deselect all from this category
      setSelectedActions(prev => prev.filter(a => !categoryActions.includes(a)));
    } else {
      // Select all from this category
      setSelectedActions(prev => {
        const newActions = [...prev];
        categoryActions.forEach(action => {
          if (!newActions.includes(action)) {
            newActions.push(action);
          }
        });
        return newActions;
      });
    }
  };

  const updatePolicyFromActions = () => {
    if (selectedActions.length === 0) {
      return;
    }

    const selectedUser = users.find(u => u.id === selectedUserId);
    const resources = selectedBucket
      ? [`arn:aws:s3:::${selectedBucket}`, `arn:aws:s3:::${selectedBucket}/*`]
      : ['arn:aws:s3:::*', 'arn:aws:s3:::*/*'];

    const policyDoc = {
      Version: '2012-10-17',
      Statement: [{
        Effect: effect,
        Action: selectedActions,
        Resource: resources
      }]
    };

    setDocument(JSON.stringify(policyDoc, null, 2));

    // Update name/description
    const actionCount = selectedActions.length;
    const allActions = [...S3_ACTIONS.read, ...S3_ACTIONS.write, ...S3_ACTIONS.bucket].length;
    const actionDesc = actionCount === allActions ? 'Full Access' : `${actionCount} Action${actionCount > 1 ? 's' : ''}`;

    const baseName = selectedBucket
      ? `${selectedBucket} - ${actionDesc}`
      : actionDesc;

    const finalName = selectedUser
      ? `${selectedUser.username} - ${baseName}`
      : baseName;

    setName(finalName);
    setDescription(`${effect}s ${actionDesc}${selectedBucket ? ` on ${selectedBucket}` : ''}${selectedUser ? ` for ${selectedUser.username}` : ''}`);
  };

  const applyTemplate = (templateType: 'readOnly' | 'fullAccess' | 'denyAll') => {
    const template = templates[templateType];
    const selectedUser = users.find(u => u.id === selectedUserId);

    // Set actions based on template
    if (templateType === 'readOnly') {
      setSelectedActions(['s3:GetObject', 's3:ListBucket']);
      setEffect('Allow');
    } else if (templateType === 'fullAccess') {
      handleSelectAllActions();
      setEffect('Allow');
    } else if (templateType === 'denyAll') {
      handleSelectAllActions();
      setEffect('Deny');
    }

    // If no bucket selected, use generic names
    if (!selectedBucket) {
      const baseName = selectedUser ? `${selectedUser.username} - ${template.name}` : template.name;
      const baseDesc = selectedUser
        ? `${template.description} for user ${selectedUser.username}`
        : template.description;

      setName(baseName);
      setDescription(baseDesc);
      setDocument(template.document);
    } else {
      // Keep bucket-specific naming if bucket is selected
      const bucketSpecificName = selectedUser
        ? `${selectedUser.username} - ${selectedBucket} - ${template.name}`
        : `${selectedBucket} - ${template.name}`;
      const bucketSpecificDesc = selectedUser
        ? `${template.description} for ${selectedBucket} bucket (user: ${selectedUser.username})`
        : `${template.description} for ${selectedBucket} bucket`;

      // Update document to use selected bucket
      try {
        const doc = JSON.parse(template.document);
        const newResources: string[] = [];

        // Convert resources to bucket-specific
        for (const resource of doc.Statement[0].Resource) {
          if (resource === 'arn:aws:s3:::*') {
            newResources.push(`arn:aws:s3:::${selectedBucket}`);
          } else if (resource === 'arn:aws:s3:::*/*') {
            newResources.push(`arn:aws:s3:::${selectedBucket}/*`);
          } else {
            newResources.push(resource);
          }
        }

        doc.Statement[0].Resource = newResources;
        setDocument(JSON.stringify(doc, null, 2));
        setName(bucketSpecificName);
        setDescription(bucketSpecificDesc);
      } catch (err) {
        console.error('Failed to parse template:', err);
        setDocument(template.document);
      }
    }
  };

  return (
    <div className="fixed inset-0 z-50 overflow-y-auto bg-black bg-opacity-50 flex items-center justify-center p-4">
      <div className="bg-dark-surface rounded-lg max-w-4xl w-full max-h-[90vh] overflow-hidden flex flex-col">
        <div className="p-6 border-b border-dark-border">
          <h2 className="text-2xl font-bold text-dark-text">Create Policy</h2>
          <p className="text-dark-textSecondary mt-1">Define an IAM-style access control policy</p>
        </div>

        <form onSubmit={handleSubmit} className="flex-1 overflow-y-auto p-6 space-y-6">
          {error && (
            <div className="bg-red-500/10 border border-red-500 text-red-500 px-4 py-3 rounded-lg">
              {error}
            </div>
          )}

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
              <label className="block text-sm font-medium text-dark-text mb-2">
                <div className="flex items-center gap-2">
                  <UserIcon className="w-4 h-4" />
                  Select User (Optional)
                </div>
              </label>
              <select
                value={selectedUserId}
                onChange={(e) => handleUserSelect(e.target.value)}
                className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
                disabled={loadingUsers}
              >
                <option value="">All Users / Custom Policy</option>
                {users.map((user) => (
                  <option key={user.id} value={user.id}>
                    {user.username} ({user.email})
                  </option>
                ))}
              </select>
              <p className="text-xs text-dark-textSecondary mt-1">
                Select a user to automatically attach this policy to them
              </p>
            </div>

            <div>
              <label className="block text-sm font-medium text-dark-text mb-2">
                <div className="flex items-center gap-2">
                  <FolderOpen className="w-4 h-4" />
                  Select Bucket (Optional)
                </div>
              </label>
              <select
                value={selectedBucket}
                onChange={(e) => handleBucketSelect(e.target.value)}
                className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
                disabled={loadingBuckets}
              >
                <option value="">All Buckets / Custom Policy</option>
                {buckets.map((bucket) => (
                  <option key={bucket.id} value={bucket.name}>
                    {bucket.name} ({bucket.storage_backend})
                  </option>
                ))}
              </select>
              <p className="text-xs text-dark-textSecondary mt-1">
                Select a bucket to scope the policy to specific bucket permissions
              </p>
            </div>
          </div>

          {selectedUserId && selectedBucket && (
            <div className="bg-blue-500/10 border border-blue-500 text-blue-400 px-4 py-3 rounded-lg flex items-start gap-2">
              <Shield className="w-5 h-5 flex-shrink-0 mt-0.5" />
              <div>
                <p className="font-medium">Policy Binding</p>
                <p className="text-sm">
                  This policy will grant{' '}
                  <span className="font-semibold">{users.find(u => u.id === selectedUserId)?.username}</span>{' '}
                  access to the{' '}
                  <span className="font-semibold">{selectedBucket}</span> bucket
                </p>
              </div>
            </div>
          )}

          {/* Action Selector */}
          <div className="border border-dark-border rounded-lg p-4">
            <div className="flex items-center justify-between mb-4">
              <label className="text-sm font-medium text-dark-text">
                Select Permissions
              </label>
              <div className="flex gap-2">
                <button
                  type="button"
                  onClick={handleSelectAllActions}
                  className="px-3 py-1 text-xs bg-blue-600 hover:bg-blue-700 text-white rounded transition-colors"
                >
                  Select All
                </button>
                <button
                  type="button"
                  onClick={() => setSelectedActions([])}
                  className="px-3 py-1 text-xs bg-dark-bg hover:bg-dark-border text-dark-textSecondary rounded transition-colors"
                >
                  Clear All
                </button>
              </div>
            </div>

            <div className="mb-4">
              <label className="text-sm font-medium text-dark-text mb-2 block">Effect</label>
              <div className="flex gap-4">
                <label className="flex items-center gap-2 cursor-pointer">
                  <input
                    type="radio"
                    checked={effect === 'Allow'}
                    onChange={() => setEffect('Allow')}
                    className="text-blue-600"
                  />
                  <span className="text-sm text-dark-text">Allow</span>
                </label>
                <label className="flex items-center gap-2 cursor-pointer">
                  <input
                    type="radio"
                    checked={effect === 'Deny'}
                    onChange={() => setEffect('Deny')}
                    className="text-red-600"
                  />
                  <span className="text-sm text-dark-text">Deny</span>
                </label>
              </div>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
              {/* Read Operations */}
              <div className="space-y-2">
                <div className="flex items-center justify-between mb-2">
                  <h4 className="text-xs font-semibold text-dark-text uppercase">Read</h4>
                  <button
                    type="button"
                    onClick={() => handleSelectCategoryActions('read')}
                    className="text-xs text-blue-500 hover:text-blue-400"
                  >
                    {S3_ACTIONS.read.every(a => selectedActions.includes(a.action)) ? 'Deselect' : 'Select'} All
                  </button>
                </div>
                {S3_ACTIONS.read.map(({ action, label, description }) => (
                  <label key={action} className="flex items-start gap-2 cursor-pointer group">
                    <input
                      type="checkbox"
                      checked={selectedActions.includes(action)}
                      onChange={() => handleActionToggle(action)}
                      className="mt-1 text-blue-600"
                    />
                    <div>
                      <div className="text-sm text-dark-text group-hover:text-blue-500">{label}</div>
                      <div className="text-xs text-dark-textSecondary">{description}</div>
                    </div>
                  </label>
                ))}
              </div>

              {/* Write Operations */}
              <div className="space-y-2">
                <div className="flex items-center justify-between mb-2">
                  <h4 className="text-xs font-semibold text-dark-text uppercase">Write</h4>
                  <button
                    type="button"
                    onClick={() => handleSelectCategoryActions('write')}
                    className="text-xs text-blue-500 hover:text-blue-400"
                  >
                    {S3_ACTIONS.write.every(a => selectedActions.includes(a.action)) ? 'Deselect' : 'Select'} All
                  </button>
                </div>
                {S3_ACTIONS.write.map(({ action, label, description }) => (
                  <label key={action} className="flex items-start gap-2 cursor-pointer group">
                    <input
                      type="checkbox"
                      checked={selectedActions.includes(action)}
                      onChange={() => handleActionToggle(action)}
                      className="mt-1 text-blue-600"
                    />
                    <div>
                      <div className="text-sm text-dark-text group-hover:text-blue-500">{label}</div>
                      <div className="text-xs text-dark-textSecondary">{description}</div>
                    </div>
                  </label>
                ))}
              </div>

              {/* Bucket Operations */}
              <div className="space-y-2">
                <div className="flex items-center justify-between mb-2">
                  <h4 className="text-xs font-semibold text-dark-text uppercase">Bucket</h4>
                  <button
                    type="button"
                    onClick={() => handleSelectCategoryActions('bucket')}
                    className="text-xs text-blue-500 hover:text-blue-400"
                  >
                    {S3_ACTIONS.bucket.every(a => selectedActions.includes(a.action)) ? 'Deselect' : 'Select'} All
                  </button>
                </div>
                {S3_ACTIONS.bucket.map(({ action, label, description }) => (
                  <label key={action} className="flex items-start gap-2 cursor-pointer group">
                    <input
                      type="checkbox"
                      checked={selectedActions.includes(action)}
                      onChange={() => handleActionToggle(action)}
                      className="mt-1 text-blue-600"
                    />
                    <div>
                      <div className="text-sm text-dark-text group-hover:text-blue-500">{label}</div>
                      <div className="text-xs text-dark-textSecondary">{description}</div>
                    </div>
                  </label>
                ))}
              </div>
            </div>

            <div className="mt-4">
              <div className="flex items-center gap-2 text-sm">
                <span className="text-dark-textSecondary">
                  {selectedActions.length} action{selectedActions.length !== 1 ? 's' : ''} selected
                </span>
                {selectedActions.length > 0 && (
                  <span className="text-green-500">✓ Policy will be auto-generated</span>
                )}
              </div>
            </div>
          </div>

          <div>
            <label className="block text-sm font-medium text-dark-text mb-2">Policy Name</label>
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
              placeholder="my-policy"
              required
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-dark-text mb-2">Description</label>
            <input
              type="text"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
              placeholder="Brief description of what this policy does"
              required
            />
          </div>

          <div className="border-t border-dark-border pt-4">
            <div className="mb-3">
              <label className="block text-sm font-medium text-dark-text mb-2">
                Quick Templates
              </label>
              <div className="flex gap-2 flex-wrap">
                <button
                  type="button"
                  onClick={() => applyTemplate('readOnly')}
                  className="px-3 py-1.5 text-sm bg-dark-bg hover:bg-dark-border rounded text-dark-textSecondary hover:text-dark-text border border-dark-border transition-colors"
                >
                  📖 Read Only
                </button>
                <button
                  type="button"
                  onClick={() => applyTemplate('fullAccess')}
                  className="px-3 py-1.5 text-sm bg-dark-bg hover:bg-dark-border rounded text-dark-textSecondary hover:text-dark-text border border-dark-border transition-colors"
                >
                  ✅ Full Access
                </button>
                <button
                  type="button"
                  onClick={() => applyTemplate('denyAll')}
                  className="px-3 py-1.5 text-sm bg-dark-bg hover:bg-dark-border rounded text-dark-textSecondary hover:text-dark-text border border-dark-border transition-colors"
                >
                  🚫 Deny All
                </button>
              </div>
              <p className="text-xs text-dark-textSecondary mt-2">
                Templates will auto-select actions and generate the policy
              </p>
            </div>
          </div>

          <div>
            <label className="block text-sm font-medium text-dark-text mb-2">
              Policy Document (JSON)
              {selectedActions.length > 0 && (
                <span className="ml-2 text-xs text-orange-500">
                  (Auto-generated from selected actions)
                </span>
              )}
            </label>
            <textarea
              value={document}
              onChange={(e) => setDocument(e.target.value)}
              className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text font-mono text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
              rows={12}
              placeholder={JSON.stringify({
                Version: '2012-10-17',
                Statement: [
                  {
                    Effect: 'Allow',
                    Action: ['s3:GetObject', 's3:PutObject'],
                    Resource: ['arn:aws:s3:::my-bucket', 'arn:aws:s3:::my-bucket/*']
                  }
                ]
              }, null, 2)}
              required
            />
            <p className="text-xs text-dark-textSecondary mt-1">
              You can manually edit the policy JSON or use the action selector above
            </p>
          </div>
        </form>

        <div className="p-6 border-t border-dark-border flex justify-end gap-3">
          <button
            type="button"
            onClick={onClose}
            className="px-4 py-2 bg-dark-bg hover:bg-dark-border text-dark-text rounded-lg transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={handleSubmit}
            disabled={loading}
            className="px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded-lg transition-colors disabled:opacity-50"
          >
            {loading ? 'Creating...' : selectedUserId ? 'Create & Attach Policy' : 'Create Policy'}
          </button>
        </div>
      </div>
    </div>
  );
}

function ViewPolicyModal({ policy, onClose }: { policy: Policy; onClose: () => void }) {
  const [formattedDoc, setFormattedDoc] = useState('');

  useEffect(() => {
    try {
      const parsed = JSON.parse(policy.document);
      setFormattedDoc(JSON.stringify(parsed, null, 2));
    } catch {
      setFormattedDoc(policy.document);
    }
  }, [policy]);

  return (
    <div className="fixed inset-0 z-50 overflow-y-auto bg-black bg-opacity-50 flex items-center justify-center p-4">
      <div className="bg-dark-surface rounded-lg max-w-4xl w-full max-h-[90vh] overflow-hidden flex flex-col">
        <div className="p-6 border-b border-dark-border">
          <h2 className="text-2xl font-bold text-dark-text">{policy.name}</h2>
          <p className="text-dark-textSecondary mt-1">{policy.description}</p>
        </div>

        <div className="flex-1 overflow-y-auto p-6">
          <h3 className="text-sm font-medium text-dark-text mb-2">Policy Document</h3>
          <pre className="bg-dark-bg border border-dark-border rounded-lg p-4 text-sm text-dark-text font-mono overflow-x-auto">
            {formattedDoc}
          </pre>
        </div>

        <div className="p-6 border-t border-dark-border flex justify-end">
          <button
            onClick={onClose}
            className="px-4 py-2 bg-dark-bg hover:bg-dark-border text-dark-text rounded-lg transition-colors"
          >
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
