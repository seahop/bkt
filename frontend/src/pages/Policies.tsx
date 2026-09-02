import { useState, useEffect } from 'react';
import { Shield, Plus, Trash2, Edit, FileText, AlertCircle, FolderOpen, User as UserIcon, Database, ChevronDown, ChevronRight, Settings2 } from 'lucide-react';
import { listPolicies, createPolicy, updatePolicy, deletePolicy, getPolicyTemplates, Policy, attachPolicyToUser } from '../services/policy';
import { useAuthStore } from '../store/authStore';
import { bucketApi, userApi } from '../services/api';
import type { Bucket, User } from '../types';
import { getErrorMessage } from '../utils/errors';

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
      setError(getErrorMessage(err, 'Failed to load policies'));
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
      alert(getErrorMessage(err, 'Failed to delete policy'));
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
    <div className="page">
      <div className="flex items-start justify-between gap-4 mb-8">
        <div>
          <h1 className="page-title">Policies</h1>
          <p className="page-subtitle">Manage IAM-style access control policies</p>
        </div>
        {user?.is_admin && (
          <button onClick={handleCreatePolicy} className="btn-primary">
            <Plus className="w-4 h-4" />
            Create Policy
          </button>
        )}
      </div>

      {error && (
        <div className="alert-error mb-6">
          <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
          <span>{error}</span>
        </div>
      )}

      {loading ? (
        <div className="flex flex-col items-center justify-center h-64 gap-3">
          <div className="spinner" />
          <p className="text-sm text-dark-textSecondary">Loading policies…</p>
        </div>
      ) : policies.length === 0 ? (
        <div className="card empty-state">
          <Shield className="empty-state-icon" />
          <h3 className="text-base font-semibold text-dark-text mb-1">No policies yet</h3>
          <p className="text-sm text-dark-textSecondary mb-5 max-w-sm">
            {user?.is_admin
              ? 'Create your first policy to control access to buckets and objects.'
              : 'No policies have been assigned to you.'}
          </p>
          {user?.is_admin && (
            <button onClick={handleCreatePolicy} className="btn-secondary">
              <Plus className="w-4 h-4" />
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
                className="card p-6 transition-colors hover:border-dark-borderStrong"
              >
                <div className="flex items-start justify-between gap-4">
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2.5 mb-1.5">
                      <span className="flex items-center justify-center w-8 h-8 rounded-lg bg-blue-600/15 shrink-0">
                        <Shield className="w-4 h-4 text-blue-500" />
                      </span>
                      <h3 className="text-base font-semibold text-dark-text font-mono truncate">
                        {policy.name}
                      </h3>
                    </div>
                    {policy.description && (
                      <p className="text-sm text-dark-textSecondary mb-3">{policy.description}</p>
                    )}

                    {/* Show buckets this policy applies to */}
                    <div className="flex items-center gap-1.5 mb-3 flex-wrap">
                      <Database className="w-4 h-4 text-dark-textMuted shrink-0" />
                      {policyBuckets.length > 0 ? (
                        policyBuckets.map((bucket) => (
                          <span key={bucket} className="badge-blue font-mono">
                            {bucket}
                          </span>
                        ))
                      ) : (
                        <span className="badge-gray">All buckets (*)</span>
                      )}
                    </div>

                    <div className="flex items-center gap-4 text-xs text-dark-textMuted tabular-nums">
                      <span>Created {new Date(policy.created_at).toLocaleDateString()}</span>
                      <span>Updated {new Date(policy.updated_at).toLocaleDateString()}</span>
                    </div>
                  </div>
                  <div className="flex items-center gap-1 shrink-0">
                    <button
                      onClick={() => handleViewPolicy(policy)}
                      className="btn-icon"
                      title="View policy document"
                    >
                      <FileText className="w-4 h-4" />
                    </button>
                    {user?.is_admin && (
                      <>
                        <button
                          onClick={() => handleEditPolicy(policy)}
                          className="btn-icon"
                          title="Edit policy"
                        >
                          <Edit className="w-4 h-4" />
                        </button>
                        <button
                          onClick={() => handleDeletePolicy(policy.id)}
                          className="btn-icon hover:!text-red-400 hover:!bg-red-500/10"
                          title="Delete policy"
                        >
                          <Trash2 className="w-4 h-4" />
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

  // Track manual edits to the raw JSON and its validity
  const [jsonManuallyEdited, setJsonManuallyEdited] = useState(false);
  const [jsonError, setJsonError] = useState('');

  // Validate the raw JSON document whenever it changes
  useEffect(() => {
    if (!document.trim()) {
      setJsonError('');
      return;
    }
    try {
      JSON.parse(document);
      setJsonError('');
    } catch (err) {
      setJsonError(`Invalid JSON: ${(err as Error).message}`);
    }
  }, [document]);

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
        setError(getErrorMessage(err, 'Failed to load buckets and users'));
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

  // Warn before overwriting hand-edited JSON with a builder-generated document.
  // Returns true if the builder is allowed to regenerate the document.
  const confirmOverwriteJson = (): boolean => {
    if (!jsonManuallyEdited) return true;
    const ok = confirm(
      'You have manually edited the policy JSON. Changing this selection will overwrite your edits. Continue?'
    );
    if (ok) {
      setJsonManuallyEdited(false);
    }
    return ok;
  };

  // Auto-update policy document when selections change (simple mode only)
  useEffect(() => {
    if (!advancedMode && (initialized || !isEditMode)) {
      if (selectedActions.length > 0 || selectedBuckets.length > 0) {
        if (!confirmOverwriteJson()) return;
        generatePolicyDocument();
      }
    }
  }, [selectedActions, effect, selectedBuckets, advancedMode, initialized]);

  // Auto-update policy document in advanced mode
  useEffect(() => {
    if (advancedMode && selectedBuckets.length > 0) {
      if (!confirmOverwriteJson()) return;
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

    // Validate that the policy document is well-formed JSON before submitting
    try {
      JSON.parse(document);
    } catch (parseErr) {
      setError(`Policy document is not valid JSON: ${(parseErr as Error).message}`);
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
      setError(getErrorMessage(err, `Failed to ${isEditMode ? 'update' : 'create'} policy`));
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
    <div className="modal-overlay">
      <div className="modal-panel !max-w-4xl !p-0 !overflow-hidden flex flex-col">
        <div className="p-6 border-b border-dark-border shrink-0">
          <h2 className="modal-title">
            {isEditMode ? 'Edit Policy' : 'Create Policy'}
          </h2>
          <p className="text-sm text-dark-textSecondary mt-1">
            {isEditMode ? 'Modify the policy settings and permissions' : 'Define an IAM-style access control policy for users or teams'}
          </p>
        </div>

        <form onSubmit={handleSubmit} className="flex-1 overflow-y-auto p-6 space-y-6">
          {error && (
            <div className="alert-error">
              <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
              <span>{error}</span>
            </div>
          )}

          {/* Policy Name - Always editable, first field */}
          <div>
            <label className="label">
              Policy Name <span className="text-red-400">*</span>
            </label>
            <input
              type="text"
              value={name}
              onChange={(e) => handleNameChange(e.target.value)}
              className="input"
              placeholder="e.g., team-engineering-access, devops-readonly"
              required
            />
            <p className="help-text">
              Use a descriptive name. For SSO, this name must match the policy name in your JWT claims.
            </p>
          </div>

          {/* Description */}
          <div>
            <label className="label">Description</label>
            <input
              type="text"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              className="input"
              placeholder="Brief description of what this policy does"
            />
          </div>

          {/* User selection - only show in create mode */}
          {!isEditMode && (
            <div>
              <label className="label">
                <span className="inline-flex items-center gap-2">
                  <UserIcon className="w-4 h-4 text-dark-textMuted" />
                  Attach to User (Optional)
                </span>
              </label>
              <select
                value={selectedUserId}
                onChange={(e) => setSelectedUserId(e.target.value)}
                className="input"
                disabled={loadingUsers}
              >
                <option value="">No user (Team/SSO policy)</option>
                {users.map((user) => (
                  <option key={user.id} value={user.id}>
                    {user.username} ({user.email})
                  </option>
                ))}
              </select>
              <p className="help-text">
                Leave empty to create a team policy for SSO, or select a user to attach immediately
              </p>
            </div>
          )}

          {/* Multi-bucket selection */}
          <div className="bg-dark-inset border border-dark-border rounded-lg p-4">
            <div className="flex items-center justify-between gap-3 mb-3">
              <h3 className="text-base font-semibold text-dark-text flex items-center gap-2">
                <FolderOpen className="w-4 h-4 text-dark-textMuted" />
                Buckets
              </h3>
              <div className="flex gap-2">
                <button
                  type="button"
                  onClick={handleSelectAllBuckets}
                  className="btn-secondary btn-sm"
                >
                  {selectedBuckets.length === buckets.length ? 'Deselect All' : 'Select All'}
                </button>
                <button
                  type="button"
                  onClick={() => setSelectedBuckets([])}
                  className="btn-ghost btn-sm"
                >
                  Clear
                </button>
              </div>
            </div>

            {loadingBuckets ? (
              <p className="text-sm text-dark-textSecondary">Loading buckets…</p>
            ) : buckets.length === 0 ? (
              <p className="text-sm text-dark-textSecondary">No buckets available</p>
            ) : (
              <div className="grid grid-cols-2 md:grid-cols-3 gap-2 max-h-40 overflow-y-auto">
                {buckets.map((bucket) => (
                  <label
                    key={bucket.id}
                    className={`flex items-center gap-2.5 p-2.5 rounded-lg border cursor-pointer transition-colors ${
                      selectedBuckets.includes(bucket.name)
                        ? 'bg-accent-soft border-blue-500/50'
                        : 'bg-dark-surface border-dark-border hover:border-dark-borderStrong'
                    }`}
                  >
                    <input
                      type="checkbox"
                      checked={selectedBuckets.includes(bucket.name)}
                      onChange={() => handleBucketToggle(bucket.name)}
                      className="accent-blue-600 shrink-0"
                    />
                    <div className="flex-1 min-w-0">
                      <div className="text-sm text-dark-text font-mono truncate">{bucket.name}</div>
                      <div className="text-xs text-dark-textMuted">{bucket.storage_backend}</div>
                    </div>
                  </label>
                ))}
              </div>
            )}

            <p className="help-text mt-3">
              {selectedBuckets.length === 0
                ? 'No buckets selected — policy will apply to all buckets (*)'
                : `${selectedBuckets.length} bucket${selectedBuckets.length > 1 ? 's' : ''} selected`}
            </p>
          </div>

          {/* Mode Toggle - Only show when multiple buckets selected */}
          {selectedBuckets.length > 1 && (
            <div className="flex items-center justify-between gap-3 bg-dark-inset border border-dark-border rounded-lg p-4">
              <div className="flex items-center gap-2">
                <Settings2 className="w-4 h-4 text-dark-textMuted" />
                <span className="text-sm font-medium text-dark-text">Permission Mode</span>
              </div>
              <div className="flex gap-1 bg-dark-surface border border-dark-border rounded-lg p-1">
                <button
                  type="button"
                  onClick={() => setAdvancedMode(false)}
                  className={`px-3 py-1.5 text-xs font-medium rounded-md transition-colors ${
                    !advancedMode
                      ? 'bg-blue-600 text-white'
                      : 'text-dark-textSecondary hover:text-dark-text'
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
                  className={`px-3 py-1.5 text-xs font-medium rounded-md transition-colors ${
                    advancedMode
                      ? 'bg-blue-600 text-white'
                      : 'text-dark-textSecondary hover:text-dark-text'
                  }`}
                >
                  Advanced (Per-bucket)
                </button>
              </div>
            </div>
          )}

          {/* Simple Mode - Action Selector */}
          {!advancedMode && (
            <div className="bg-dark-inset border border-dark-border rounded-lg p-4">
              <div className="flex items-center justify-between gap-3 mb-4">
                <h3 className="text-base font-semibold text-dark-text">Permissions</h3>
                <div className="flex gap-2">
                  <button
                    type="button"
                    onClick={handleSelectAllActions}
                    className="btn-secondary btn-sm"
                  >
                    Select All
                  </button>
                  <button
                    type="button"
                    onClick={() => setSelectedActions([])}
                    className="btn-ghost btn-sm"
                  >
                    Clear All
                  </button>
                </div>
              </div>

              <div className="mb-4">
                <label className="label">Effect</label>
                <div className="flex gap-4">
                  <label className="flex items-center gap-2 cursor-pointer">
                    <input
                      type="radio"
                      checked={effect === 'Allow'}
                      onChange={() => setEffect('Allow')}
                      className="accent-blue-600"
                    />
                    <span className="badge-green">Allow</span>
                  </label>
                  <label className="flex items-center gap-2 cursor-pointer">
                    <input
                      type="radio"
                      checked={effect === 'Deny'}
                      onChange={() => setEffect('Deny')}
                      className="accent-red-600"
                    />
                    <span className="badge-red">Deny</span>
                  </label>
                </div>
              </div>

              <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                {(['read', 'write', 'bucket'] as const).map((category) => (
                  <div key={category} className="space-y-2">
                    <div className="flex items-center justify-between mb-2">
                      <h4 className="text-xs font-medium uppercase tracking-wider text-dark-textSecondary">{category}</h4>
                      <button
                        type="button"
                        onClick={() => handleSelectCategoryActions(category)}
                        className="text-xs text-blue-400 hover:text-blue-300 transition-colors"
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
                          className="mt-1 accent-blue-600"
                        />
                        <div>
                          <div className="text-sm text-dark-text group-hover:text-blue-400 transition-colors">{label}</div>
                          <div className="text-xs text-dark-textMuted">{description}</div>
                        </div>
                      </label>
                    ))}
                  </div>
                ))}
              </div>

              <p className="help-text mt-4">
                {selectedActions.length} action{selectedActions.length !== 1 ? 's' : ''} selected
              </p>
            </div>
          )}

          {/* Advanced Mode - Per-bucket permissions */}
          {advancedMode && selectedBuckets.length > 0 && (
            <div className="bg-dark-inset border border-dark-border rounded-lg p-4">
              <div className="flex items-center justify-between gap-3 mb-4">
                <h3 className="text-base font-semibold text-dark-text">Per-Bucket Permissions</h3>
                <button
                  type="button"
                  onClick={applyFullAccessToAllBuckets}
                  className="btn-secondary btn-sm"
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
                    <div key={bucketName} className="bg-dark-surface border border-dark-border rounded-lg overflow-hidden">
                      <button
                        type="button"
                        onClick={() => toggleBucketExpanded(bucketName)}
                        className="w-full flex items-center justify-between gap-3 p-3 hover:bg-dark-surfaceHover transition-colors"
                      >
                        <div className="flex items-center gap-2 min-w-0">
                          {isExpanded ? (
                            <ChevronDown className="w-4 h-4 text-dark-textMuted shrink-0" />
                          ) : (
                            <ChevronRight className="w-4 h-4 text-dark-textMuted shrink-0" />
                          )}
                          <Database className="w-4 h-4 text-blue-500 shrink-0" />
                          <span className="text-sm font-medium text-dark-text font-mono truncate">{bucketName}</span>
                        </div>
                        <div className="flex items-center gap-2 shrink-0">
                          <span className={perms.effect === 'Allow' ? 'badge-green' : 'badge-red'}>
                            {perms.effect}
                          </span>
                          <span className="text-xs text-dark-textMuted tabular-nums">
                            {actionCount} action{actionCount !== 1 ? 's' : ''}
                          </span>
                        </div>
                      </button>

                      {isExpanded && (
                        <div className="p-4 border-t border-dark-border space-y-4">
                          <div className="flex items-center justify-between gap-3">
                            <div className="flex gap-4">
                              <label className="flex items-center gap-2 cursor-pointer">
                                <input
                                  type="radio"
                                  checked={perms.effect === 'Allow'}
                                  onChange={() => handleBucketEffectChange(bucketName, 'Allow')}
                                  className="accent-green-600"
                                />
                                <span className="badge-green">Allow</span>
                              </label>
                              <label className="flex items-center gap-2 cursor-pointer">
                                <input
                                  type="radio"
                                  checked={perms.effect === 'Deny'}
                                  onChange={() => handleBucketEffectChange(bucketName, 'Deny')}
                                  className="accent-red-600"
                                />
                                <span className="badge-red">Deny</span>
                              </label>
                            </div>
                            <button
                              type="button"
                              onClick={() => handleBucketSelectAll(bucketName)}
                              className="btn-secondary btn-sm"
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
                                    className="accent-blue-600"
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
            <div className="border-t border-dark-border pt-5">
              <label className="label">Quick Templates</label>
              <div className="flex gap-2 flex-wrap">
                <button
                  type="button"
                  onClick={() => applyTemplate('readOnly')}
                  className="btn-secondary btn-sm"
                >
                  Read Only
                </button>
                <button
                  type="button"
                  onClick={() => applyTemplate('fullAccess')}
                  className="btn-secondary btn-sm"
                >
                  Full Access
                </button>
                <button
                  type="button"
                  onClick={() => applyTemplate('denyAll')}
                  className="btn-secondary btn-sm"
                >
                  Deny All
                </button>
              </div>
            </div>
          )}

          {/* Policy Document Preview */}
          <div>
            <div className="flex items-baseline justify-between gap-3 mb-1.5">
              <h3 className="text-base font-semibold text-dark-text">Policy Document (JSON)</h3>
              <span className="text-xs text-dark-textMuted">Auto-generated, or edit manually</span>
            </div>
            <textarea
              value={document}
              onChange={(e) => {
                setDocument(e.target.value);
                setJsonManuallyEdited(true);
              }}
              className={`input font-mono min-h-[220px] ${
                jsonError ? '!border-red-500/60 focus:!ring-red-500/50' : ''
              }`}
              rows={10}
              placeholder='{"Version": "2012-10-17", "Statement": [...]}'
            />
            {jsonError && (
              <div className="alert-error mt-2">
                <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
                <span>{jsonError}</span>
              </div>
            )}
          </div>
        </form>

        <div className="p-6 border-t border-dark-border flex justify-end gap-2 shrink-0">
          <button type="button" onClick={onClose} className="btn-ghost">
            Cancel
          </button>
          <button
            onClick={handleSubmit}
            disabled={loading || !!jsonError}
            className="btn-primary"
          >
            {loading && <span className="spinner !w-4 !h-4" />}
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
    <div className="modal-overlay">
      <div className="modal-panel !max-w-4xl !p-0 !overflow-hidden flex flex-col">
        <div className="p-6 border-b border-dark-border shrink-0">
          <h2 className="modal-title font-mono">{policy.name}</h2>
          {policy.description && (
            <p className="text-sm text-dark-textSecondary mt-1">{policy.description}</p>
          )}
        </div>

        <div className="flex-1 overflow-y-auto p-6">
          <h3 className="text-base font-semibold text-dark-text mb-3">Policy Document</h3>
          <pre className="bg-dark-inset border border-dark-border rounded-lg p-4 text-sm text-dark-text font-mono overflow-x-auto">
            {formattedDoc}
          </pre>
        </div>

        <div className="p-6 border-t border-dark-border flex justify-end shrink-0">
          <button onClick={onClose} className="btn-ghost">
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
