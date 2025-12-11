import { useState, useEffect } from 'react';
import { Shield, Plus, Trash2, Edit, FileText, AlertCircle, FolderOpen, User as UserIcon, Database, ChevronDown, ChevronRight, Settings2 } from 'lucide-react';
import { listPolicies, createPolicy, updatePolicy, deletePolicy, getPolicyTemplates, Policy, attachPolicyToUser } from '../services/policy';
import { useAuthStore } from '../store/authStore';
import { bucketApi, userApi } from '../services/api';
import type { Bucket, User } from '../types';

// Helper to extract bucket names from a policy document
const extractBucketsFromPolicy = (document: string): string[] => {
  try {
    const doc = JSON.parse(document);
    const buckets = new Set<string>();

    for (const statement of doc.Statement || []) {
      for (const resource of statement.Resource || []) {
        // Match arn:aws:s3:::bucket-name or arn:aws:s3:::bucket-name/*
        const match = resource.match(/^arn:aws:s3:::([^/*]+)/);
        if (match && match[1] !== '*') {
          buckets.add(match[1]);
        }
      }
    }

    return Array.from(buckets);
  } catch {
    return [];
  }
};

export default function Policies() {
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [showPolicyModal, setShowPolicyModal] = useState(false);
  const [editingPolicy, setEditingPolicy] = useState<Policy | null>(null);
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

  const handleEditPolicy = (policy: Policy) => {
    setEditingPolicy(policy);
    setShowPolicyModal(true);
  };

  const handleCreatePolicy = () => {
    setEditingPolicy(null);
    setShowPolicyModal(true);
  };

  const handleModalClose = () => {
    setShowPolicyModal(false);
    setEditingPolicy(null);
  };

  const handleModalSuccess = () => {
    handleModalClose();
    fetchPolicies();
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
            onClick={handleCreatePolicy}
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
              onClick={handleCreatePolicy}
              className="inline-flex items-center gap-2 px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded-lg transition-colors"
            >
              <Plus className="w-5 h-5" />
              Create First Policy
            </button>
          )}
        </div>
      ) : (
        <div className="grid gap-4">
          {policies.map((policy) => {
            const policyBuckets = extractBucketsFromPolicy(policy.document);
            return (
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
                    <p className="text-dark-textSecondary mb-3">{policy.description}</p>

                    {/* Show buckets this policy applies to */}
                    <div className="flex items-center gap-2 mb-3 flex-wrap">
                      <Database className="w-4 h-4 text-dark-textSecondary" />
                      {policyBuckets.length > 0 ? (
                        policyBuckets.map((bucket) => (
                          <span
                            key={bucket}
                            className="px-2 py-0.5 text-xs bg-blue-500/20 text-blue-400 rounded"
                          >
                            {bucket}
                          </span>
                        ))
                      ) : (
                        <span className="text-xs text-dark-textSecondary">All buckets (*)</span>
                      )}
                    </div>

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
                      <>
                        <button
                          onClick={() => handleEditPolicy(policy)}
                          className="p-2 hover:bg-dark-bg rounded-lg transition-colors text-dark-textSecondary hover:text-green-500"
                          title="Edit policy"
                        >
                          <Edit className="w-5 h-5" />
                        </button>
                        <button
                          onClick={() => handleDeletePolicy(policy.id)}
                          className="p-2 hover:bg-dark-bg rounded-lg transition-colors text-dark-textSecondary hover:text-red-500"
                          title="Delete policy"
                        >
                          <Trash2 className="w-5 h-5" />
                        </button>
                      </>
                    )}
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      )}

      {/* Create/Edit Policy Modal */}
      {showPolicyModal && (
        <PolicyModal
          policy={editingPolicy}
          onClose={handleModalClose}
          onSuccess={handleModalSuccess}
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

const ALL_ACTIONS = [...S3_ACTIONS.read, ...S3_ACTIONS.write, ...S3_ACTIONS.bucket].map(a => a.action);

// Type for per-bucket permissions in advanced mode
type BucketPermissions = {
  [bucketName: string]: {
    actions: string[];
    effect: 'Allow' | 'Deny';
  };
};

// Helper to extract per-bucket permissions from policy document
const extractPerBucketPermissions = (document: string, bucketNames: string[]): BucketPermissions => {
  try {
    const doc = JSON.parse(document);
    const permissions: BucketPermissions = {};

    // Initialize all buckets with empty permissions
    for (const bucketName of bucketNames) {
      permissions[bucketName] = { actions: [], effect: 'Allow' };
    }

    // Parse statements to extract per-bucket permissions
    for (const statement of doc.Statement || []) {
      const effect = (statement.Effect as 'Allow' | 'Deny') || 'Allow';
      const actions = statement.Action || [];

      for (const resource of statement.Resource || []) {
        const match = resource.match(/^arn:aws:s3:::([^/*]+)/);
        if (match && match[1] !== '*' && permissions[match[1]]) {
          // Expand s3:* to all actions
          const expandedActions = actions.flatMap((a: string) =>
            a === 's3:*' || a === '*' ? ALL_ACTIONS : [a]
          );
          permissions[match[1]] = {
            actions: [...new Set([...permissions[match[1]].actions, ...expandedActions])],
            effect,
          };
        }
      }
    }

    return permissions;
  } catch {
    return {};
  }
};

// Helper to extract simple mode data
const extractActionsFromPolicy = (document: string): { actions: string[]; effect: 'Allow' | 'Deny' } => {
  try {
    const doc = JSON.parse(document);
    const actions = new Set<string>();
    let effect: 'Allow' | 'Deny' = 'Allow';

    for (const statement of doc.Statement || []) {
      if (statement.Effect) {
        effect = statement.Effect as 'Allow' | 'Deny';
      }
      for (const action of statement.Action || []) {
        if (action === 's3:*' || action === '*') {
          ALL_ACTIONS.forEach(a => actions.add(a));
        } else {
          actions.add(action);
        }
      }
    }

    return { actions: Array.from(actions), effect };
  } catch {
    return { actions: [], effect: 'Allow' };
  }
};

interface PolicyModalProps {
  policy: Policy | null;
  onClose: () => void;
  onSuccess: () => void;
}

function PolicyModal({ policy, onClose, onSuccess }: PolicyModalProps) {
  const isEditMode = policy !== null;
  const templates = getPolicyTemplates();

  // Basic fields
  const [name, setName] = useState(policy?.name || '');
  const [description, setDescription] = useState(policy?.description || '');
  const [document, setDocument] = useState(policy?.document || '');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  // User/bucket data
  const [buckets, setBuckets] = useState<Bucket[]>([]);
  const [selectedBuckets, setSelectedBuckets] = useState<string[]>([]);
  const [loadingBuckets, setLoadingBuckets] = useState(true);
  const [users, setUsers] = useState<User[]>([]);
  const [selectedUserId, setSelectedUserId] = useState<string>('');
  const [loadingUsers, setLoadingUsers] = useState(true);

  // Simple mode state
  const [selectedActions, setSelectedActions] = useState<string[]>([]);
  const [effect, setEffect] = useState<'Allow' | 'Deny'>('Allow');

  // Advanced mode state
  const [advancedMode, setAdvancedMode] = useState(false);
  const [bucketPermissions, setBucketPermissions] = useState<BucketPermissions>({});
  const [expandedBuckets, setExpandedBuckets] = useState<Set<string>>(new Set());

  // Track if name was manually edited
  const [nameManuallyEdited, setNameManuallyEdited] = useState(isEditMode);

  // Track initialization
  const [initialized, setInitialized] = useState(false);

  // Fetch buckets and users
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

  // Initialize form from existing policy when editing
  useEffect(() => {
    if (isEditMode && policy && !initialized) {
      const policyBuckets = extractBucketsFromPolicy(policy.document);
      const { actions, effect: policyEffect } = extractActionsFromPolicy(policy.document);

      setSelectedBuckets(policyBuckets);
      setSelectedActions(actions);
      setEffect(policyEffect);

      // Check if this is a multi-statement policy (advanced mode)
      try {
        const doc = JSON.parse(policy.document);
        if (doc.Statement && doc.Statement.length > 1) {
          setAdvancedMode(true);
          const perms = extractPerBucketPermissions(policy.document, policyBuckets);
          setBucketPermissions(perms);
        }
      } catch {
        // Ignore parse errors
      }

      setInitialized(true);
    }
  }, [isEditMode, policy, initialized]);

  // Auto-update policy document when selections change (simple mode only)
  useEffect(() => {
    if (!advancedMode && (initialized || !isEditMode)) {
      if (selectedActions.length > 0 || selectedBuckets.length > 0) {
        generatePolicyDocument();
      }
    }
  }, [selectedActions, effect, selectedBuckets, advancedMode, initialized]);

  // Auto-update policy document in advanced mode
  useEffect(() => {
    if (advancedMode && selectedBuckets.length > 0) {
      generateAdvancedPolicyDocument();
    }
  }, [bucketPermissions, advancedMode, selectedBuckets]);

  const generatePolicyDocument = () => {
    if (selectedActions.length === 0) return;

    let resources: string[];
    if (selectedBuckets.length > 0) {
      resources = [];
      for (const bucket of selectedBuckets) {
        resources.push(`arn:aws:s3:::${bucket}`);
        resources.push(`arn:aws:s3:::${bucket}/*`);
      }
    } else {
      resources = ['arn:aws:s3:::*', 'arn:aws:s3:::*/*'];
    }

    const policyDoc = {
      Version: '2012-10-17',
      Statement: [{
        Effect: effect,
        Action: selectedActions,
        Resource: resources
      }]
    };

    setDocument(JSON.stringify(policyDoc, null, 2));

    // Auto-generate name if not manually edited
    if (!nameManuallyEdited) {
      const actionCount = selectedActions.length;
      const actionDesc = actionCount === ALL_ACTIONS.length ? 'Full Access' : `${actionCount} Actions`;

      let bucketDesc = '';
      if (selectedBuckets.length === 0) {
        bucketDesc = 'All Buckets';
      } else if (selectedBuckets.length === 1) {
        bucketDesc = selectedBuckets[0];
      } else {
        bucketDesc = `${selectedBuckets.length} Buckets`;
      }

      setName(`${bucketDesc} - ${actionDesc}`);
      setDescription(`${effect}s ${actionDesc.toLowerCase()} on ${bucketDesc.toLowerCase()}`);
    }
  };

  const generateAdvancedPolicyDocument = () => {
    const statements: any[] = [];

    for (const bucketName of selectedBuckets) {
      const perms = bucketPermissions[bucketName];
      if (perms && perms.actions.length > 0) {
        statements.push({
          Effect: perms.effect,
          Action: perms.actions,
          Resource: [
            `arn:aws:s3:::${bucketName}`,
            `arn:aws:s3:::${bucketName}/*`
          ]
        });
      }
    }

    if (statements.length > 0) {
      const policyDoc = {
        Version: '2012-10-17',
        Statement: statements
      };
      setDocument(JSON.stringify(policyDoc, null, 2));
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');

    if (!name.trim()) {
      setError('Please enter a policy name');
      return;
    }

    if (!document || document.trim() === '') {
      setError('Please select at least one action or provide a custom policy document');
      return;
    }

    setLoading(true);

    try {
      if (isEditMode && policy) {
        await updatePolicy(policy.id, { name, description, document });
      } else {
        const createdPolicy = await createPolicy({ name, description, document });
        if (selectedUserId) {
          await attachPolicyToUser(selectedUserId, createdPolicy.id);
        }
      }
      onSuccess();
    } catch (err: any) {
      setError(err.response?.data?.message || `Failed to ${isEditMode ? 'update' : 'create'} policy`);
    } finally {
      setLoading(false);
    }
  };

  const handleNameChange = (value: string) => {
    setName(value);
    setNameManuallyEdited(true);
  };

  const handleBucketToggle = (bucketName: string) => {
    setSelectedBuckets(prev => {
      const newBuckets = prev.includes(bucketName)
        ? prev.filter(b => b !== bucketName)
        : [...prev, bucketName];

      // Initialize bucket permissions in advanced mode
      if (advancedMode && !prev.includes(bucketName)) {
        setBucketPermissions(p => ({
          ...p,
          [bucketName]: { actions: [], effect: 'Allow' }
        }));
      }

      return newBuckets;
    });
  };

  const handleSelectAllBuckets = () => {
    if (selectedBuckets.length === buckets.length) {
      setSelectedBuckets([]);
    } else {
      const allNames = buckets.map(b => b.name);
      setSelectedBuckets(allNames);

      // Initialize all bucket permissions in advanced mode
      if (advancedMode) {
        const newPerms: BucketPermissions = {};
        for (const name of allNames) {
          newPerms[name] = bucketPermissions[name] || { actions: [], effect: 'Allow' };
        }
        setBucketPermissions(newPerms);
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
    setSelectedActions(ALL_ACTIONS);
  };

  const handleSelectCategoryActions = (category: 'read' | 'write' | 'bucket') => {
    const categoryActions = S3_ACTIONS[category].map(a => a.action);
    const allSelected = categoryActions.every(action => selectedActions.includes(action));

    if (allSelected) {
      setSelectedActions(prev => prev.filter(a => !categoryActions.includes(a)));
    } else {
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

  // Advanced mode handlers
  const handleBucketActionToggle = (bucketName: string, action: string) => {
    setBucketPermissions(prev => {
      const current = prev[bucketName] || { actions: [], effect: 'Allow' };
      const newActions = current.actions.includes(action)
        ? current.actions.filter(a => a !== action)
        : [...current.actions, action];
      return {
        ...prev,
        [bucketName]: { ...current, actions: newActions }
      };
    });
  };

  const handleBucketEffectChange = (bucketName: string, newEffect: 'Allow' | 'Deny') => {
    setBucketPermissions(prev => ({
      ...prev,
      [bucketName]: { ...prev[bucketName], effect: newEffect }
    }));
  };

  const handleBucketSelectAll = (bucketName: string) => {
    setBucketPermissions(prev => {
      const current = prev[bucketName] || { actions: [], effect: 'Allow' };
      const hasAll = ALL_ACTIONS.every(a => current.actions.includes(a));
      return {
        ...prev,
        [bucketName]: {
          ...current,
          actions: hasAll ? [] : [...ALL_ACTIONS]
        }
      };
    });
  };

  const toggleBucketExpanded = (bucketName: string) => {
    setExpandedBuckets(prev => {
      const next = new Set(prev);
      if (next.has(bucketName)) {
        next.delete(bucketName);
      } else {
        next.add(bucketName);
      }
      return next;
    });
  };

  const applyTemplate = (templateType: 'readOnly' | 'fullAccess' | 'denyAll') => {
    if (templateType === 'readOnly') {
      setSelectedActions(['s3:GetObject', 's3:ListBucket']);
      setEffect('Allow');
    } else if (templateType === 'fullAccess') {
      setSelectedActions([...ALL_ACTIONS]);
      setEffect('Allow');
    } else if (templateType === 'denyAll') {
      setSelectedActions([...ALL_ACTIONS]);
      setEffect('Deny');
    }
  };

  const applyFullAccessToAllBuckets = () => {
    const newPerms: BucketPermissions = {};
    for (const bucketName of selectedBuckets) {
      newPerms[bucketName] = { actions: [...ALL_ACTIONS], effect: 'Allow' };
    }
    setBucketPermissions(newPerms);
  };

  return (
    <div className="fixed inset-0 z-50 overflow-y-auto bg-black bg-opacity-50 flex items-center justify-center p-4">
      <div className="bg-dark-surface rounded-lg max-w-4xl w-full max-h-[90vh] overflow-hidden flex flex-col">
        <div className="p-6 border-b border-dark-border">
          <h2 className="text-2xl font-bold text-dark-text">
            {isEditMode ? 'Edit Policy' : 'Create Policy'}
          </h2>
          <p className="text-dark-textSecondary mt-1">
            {isEditMode ? 'Modify the policy settings and permissions' : 'Define an IAM-style access control policy for users or teams'}
          </p>
        </div>

        <form onSubmit={handleSubmit} className="flex-1 overflow-y-auto p-6 space-y-6">
          {error && (
            <div className="bg-red-500/10 border border-red-500 text-red-500 px-4 py-3 rounded-lg">
              {error}
            </div>
          )}

          {/* Policy Name - Always editable, first field */}
          <div>
            <label className="block text-sm font-medium text-dark-text mb-2">
              Policy Name <span className="text-red-500">*</span>
            </label>
            <input
              type="text"
              value={name}
              onChange={(e) => handleNameChange(e.target.value)}
              className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
              placeholder="e.g., team-engineering-access, devops-readonly"
              required
            />
            <p className="text-xs text-dark-textSecondary mt-1">
              Use a descriptive name. For SSO, this name must match the policy name in your JWT claims.
            </p>
          </div>

          {/* Description */}
          <div>
            <label className="block text-sm font-medium text-dark-text mb-2">Description</label>
            <input
              type="text"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
              placeholder="Brief description of what this policy does"
            />
          </div>

          {/* User selection - only show in create mode */}
          {!isEditMode && (
            <div>
              <label className="block text-sm font-medium text-dark-text mb-2">
                <div className="flex items-center gap-2">
                  <UserIcon className="w-4 h-4" />
                  Attach to User (Optional)
                </div>
              </label>
              <select
                value={selectedUserId}
                onChange={(e) => setSelectedUserId(e.target.value)}
                className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
                disabled={loadingUsers}
              >
                <option value="">No user (Team/SSO policy)</option>
                {users.map((user) => (
                  <option key={user.id} value={user.id}>
                    {user.username} ({user.email})
                  </option>
                ))}
              </select>
              <p className="text-xs text-dark-textSecondary mt-1">
                Leave empty to create a team policy for SSO, or select a user to attach immediately
              </p>
            </div>
          )}

          {/* Multi-bucket selection */}
          <div className="border border-dark-border rounded-lg p-4">
            <div className="flex items-center justify-between mb-3">
              <label className="text-sm font-medium text-dark-text">
                <div className="flex items-center gap-2">
                  <FolderOpen className="w-4 h-4" />
                  Select Buckets
                </div>
              </label>
              <div className="flex gap-2">
                <button
                  type="button"
                  onClick={handleSelectAllBuckets}
                  className="px-3 py-1 text-xs bg-blue-600 hover:bg-blue-700 text-white rounded transition-colors"
                >
                  {selectedBuckets.length === buckets.length ? 'Deselect All' : 'Select All'}
                </button>
                <button
                  type="button"
                  onClick={() => setSelectedBuckets([])}
                  className="px-3 py-1 text-xs bg-dark-bg hover:bg-dark-border text-dark-textSecondary rounded transition-colors"
                >
                  Clear
                </button>
              </div>
            </div>

            {loadingBuckets ? (
              <p className="text-dark-textSecondary text-sm">Loading buckets...</p>
            ) : buckets.length === 0 ? (
              <p className="text-dark-textSecondary text-sm">No buckets available</p>
            ) : (
              <div className="grid grid-cols-2 md:grid-cols-3 gap-2 max-h-40 overflow-y-auto">
                {buckets.map((bucket) => (
                  <label
                    key={bucket.id}
                    className={`flex items-center gap-2 p-2 rounded cursor-pointer transition-colors ${
                      selectedBuckets.includes(bucket.name)
                        ? 'bg-blue-500/20 border border-blue-500'
                        : 'bg-dark-bg border border-dark-border hover:border-blue-500/50'
                    }`}
                  >
                    <input
                      type="checkbox"
                      checked={selectedBuckets.includes(bucket.name)}
                      onChange={() => handleBucketToggle(bucket.name)}
                      className="text-blue-600"
                    />
                    <div className="flex-1 min-w-0">
                      <div className="text-sm text-dark-text truncate">{bucket.name}</div>
                      <div className="text-xs text-dark-textSecondary">{bucket.storage_backend}</div>
                    </div>
                  </label>
                ))}
              </div>
            )}

            <p className="text-xs text-dark-textSecondary mt-2">
              {selectedBuckets.length === 0
                ? 'No buckets selected - policy will apply to all buckets (*)'
                : `${selectedBuckets.length} bucket${selectedBuckets.length > 1 ? 's' : ''} selected`}
            </p>
          </div>

          {/* Mode Toggle - Only show when multiple buckets selected */}
          {selectedBuckets.length > 1 && (
            <div className="flex items-center justify-between p-4 bg-dark-bg rounded-lg border border-dark-border">
              <div className="flex items-center gap-2">
                <Settings2 className="w-4 h-4 text-dark-textSecondary" />
                <span className="text-sm text-dark-text">Permission Mode</span>
              </div>
              <div className="flex gap-2">
                <button
                  type="button"
                  onClick={() => setAdvancedMode(false)}
                  className={`px-3 py-1.5 text-sm rounded transition-colors ${
                    !advancedMode
                      ? 'bg-blue-600 text-white'
                      : 'bg-dark-surface text-dark-textSecondary hover:text-dark-text'
                  }`}
                >
                  Simple (Same for all)
                </button>
                <button
                  type="button"
                  onClick={() => {
                    setAdvancedMode(true);
                    // Initialize bucket permissions
                    const newPerms: BucketPermissions = {};
                    for (const name of selectedBuckets) {
                      newPerms[name] = bucketPermissions[name] || { actions: [...selectedActions], effect };
                    }
                    setBucketPermissions(newPerms);
                  }}
                  className={`px-3 py-1.5 text-sm rounded transition-colors ${
                    advancedMode
                      ? 'bg-blue-600 text-white'
                      : 'bg-dark-surface text-dark-textSecondary hover:text-dark-text'
                  }`}
                >
                  Advanced (Per-bucket)
                </button>
              </div>
            </div>
          )}

          {/* Simple Mode - Action Selector */}
          {!advancedMode && (
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
                {(['read', 'write', 'bucket'] as const).map((category) => (
                  <div key={category} className="space-y-2">
                    <div className="flex items-center justify-between mb-2">
                      <h4 className="text-xs font-semibold text-dark-text uppercase">{category}</h4>
                      <button
                        type="button"
                        onClick={() => handleSelectCategoryActions(category)}
                        className="text-xs text-blue-500 hover:text-blue-400"
                      >
                        {S3_ACTIONS[category].every(a => selectedActions.includes(a.action)) ? 'Deselect' : 'Select'} All
                      </button>
                    </div>
                    {S3_ACTIONS[category].map(({ action, label, description }) => (
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
                ))}
              </div>

              <div className="mt-4 flex items-center gap-2 text-sm">
                <span className="text-dark-textSecondary">
                  {selectedActions.length} action{selectedActions.length !== 1 ? 's' : ''} selected
                </span>
              </div>
            </div>
          )}

          {/* Advanced Mode - Per-bucket permissions */}
          {advancedMode && selectedBuckets.length > 0 && (
            <div className="border border-dark-border rounded-lg p-4">
              <div className="flex items-center justify-between mb-4">
                <label className="text-sm font-medium text-dark-text">
                  Per-Bucket Permissions
                </label>
                <button
                  type="button"
                  onClick={applyFullAccessToAllBuckets}
                  className="px-3 py-1 text-xs bg-green-600 hover:bg-green-700 text-white rounded transition-colors"
                >
                  Full Access to All
                </button>
              </div>

              <div className="space-y-2">
                {selectedBuckets.map((bucketName) => {
                  const isExpanded = expandedBuckets.has(bucketName);
                  const perms = bucketPermissions[bucketName] || { actions: [], effect: 'Allow' };
                  const actionCount = perms.actions.length;

                  return (
                    <div key={bucketName} className="border border-dark-border rounded-lg overflow-hidden">
                      <button
                        type="button"
                        onClick={() => toggleBucketExpanded(bucketName)}
                        className="w-full flex items-center justify-between p-3 bg-dark-bg hover:bg-dark-border transition-colors"
                      >
                        <div className="flex items-center gap-2">
                          {isExpanded ? (
                            <ChevronDown className="w-4 h-4 text-dark-textSecondary" />
                          ) : (
                            <ChevronRight className="w-4 h-4 text-dark-textSecondary" />
                          )}
                          <Database className="w-4 h-4 text-blue-500" />
                          <span className="text-sm font-medium text-dark-text">{bucketName}</span>
                        </div>
                        <div className="flex items-center gap-2">
                          <span className={`px-2 py-0.5 text-xs rounded ${
                            perms.effect === 'Allow' ? 'bg-green-500/20 text-green-400' : 'bg-red-500/20 text-red-400'
                          }`}>
                            {perms.effect}
                          </span>
                          <span className="text-xs text-dark-textSecondary">
                            {actionCount} action{actionCount !== 1 ? 's' : ''}
                          </span>
                        </div>
                      </button>

                      {isExpanded && (
                        <div className="p-4 border-t border-dark-border space-y-4">
                          <div className="flex items-center justify-between">
                            <div className="flex gap-4">
                              <label className="flex items-center gap-2 cursor-pointer">
                                <input
                                  type="radio"
                                  checked={perms.effect === 'Allow'}
                                  onChange={() => handleBucketEffectChange(bucketName, 'Allow')}
                                  className="text-green-600"
                                />
                                <span className="text-sm text-dark-text">Allow</span>
                              </label>
                              <label className="flex items-center gap-2 cursor-pointer">
                                <input
                                  type="radio"
                                  checked={perms.effect === 'Deny'}
                                  onChange={() => handleBucketEffectChange(bucketName, 'Deny')}
                                  className="text-red-600"
                                />
                                <span className="text-sm text-dark-text">Deny</span>
                              </label>
                            </div>
                            <button
                              type="button"
                              onClick={() => handleBucketSelectAll(bucketName)}
                              className="px-2 py-1 text-xs bg-blue-600 hover:bg-blue-700 text-white rounded transition-colors"
                            >
                              {ALL_ACTIONS.every(a => perms.actions.includes(a)) ? 'Deselect All' : 'Select All'}
                            </button>
                          </div>

                          <div className="grid grid-cols-2 md:grid-cols-3 gap-2">
                            {ALL_ACTIONS.map((action) => {
                              const actionInfo = [...S3_ACTIONS.read, ...S3_ACTIONS.write, ...S3_ACTIONS.bucket].find(a => a.action === action);
                              return (
                                <label key={action} className="flex items-center gap-2 cursor-pointer text-sm">
                                  <input
                                    type="checkbox"
                                    checked={perms.actions.includes(action)}
                                    onChange={() => handleBucketActionToggle(bucketName, action)}
                                    className="text-blue-600"
                                  />
                                  <span className="text-dark-text">{actionInfo?.label || action}</span>
                                </label>
                              );
                            })}
                          </div>
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
            </div>
          )}

          {/* Quick Templates - Simple mode only */}
          {!advancedMode && (
            <div className="border-t border-dark-border pt-4">
              <label className="block text-sm font-medium text-dark-text mb-2">
                Quick Templates
              </label>
              <div className="flex gap-2 flex-wrap">
                <button
                  type="button"
                  onClick={() => applyTemplate('readOnly')}
                  className="px-3 py-1.5 text-sm bg-dark-bg hover:bg-dark-border rounded text-dark-textSecondary hover:text-dark-text border border-dark-border transition-colors"
                >
                  Read Only
                </button>
                <button
                  type="button"
                  onClick={() => applyTemplate('fullAccess')}
                  className="px-3 py-1.5 text-sm bg-dark-bg hover:bg-dark-border rounded text-dark-textSecondary hover:text-dark-text border border-dark-border transition-colors"
                >
                  Full Access
                </button>
                <button
                  type="button"
                  onClick={() => applyTemplate('denyAll')}
                  className="px-3 py-1.5 text-sm bg-dark-bg hover:bg-dark-border rounded text-dark-textSecondary hover:text-dark-text border border-dark-border transition-colors"
                >
                  Deny All
                </button>
              </div>
            </div>
          )}

          {/* Policy Document Preview */}
          <div>
            <label className="block text-sm font-medium text-dark-text mb-2">
              Policy Document (JSON)
              <span className="ml-2 text-xs text-dark-textSecondary">
                (Auto-generated, or edit manually)
              </span>
            </label>
            <textarea
              value={document}
              onChange={(e) => setDocument(e.target.value)}
              className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text font-mono text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
              rows={10}
              placeholder='{"Version": "2012-10-17", "Statement": [...]}'
            />
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
            {loading
              ? (isEditMode ? 'Saving...' : 'Creating...')
              : isEditMode
                ? 'Save Changes'
                : selectedUserId
                  ? 'Create & Attach Policy'
                  : 'Create Policy'}
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
