import { useState, useEffect, useCallback, useEffectEvent, useMemo, useRef } from 'react';
import { Shield, Plus, Trash2, Edit, FileText, AlertCircle, FolderOpen, User as UserIcon, Database, ChevronDown, ChevronRight, Settings2 } from 'lucide-react';
import { listPolicies, createPolicy, updatePolicy, deletePolicy, Policy, attachPolicyToUser } from '../services/policy';
import { useAuthStore } from '../store/authStore';
import { bucketApi, userApi } from '../services/api';
import type { Bucket, User } from '../types';
import { getErrorMessage } from '../utils/errors';
import { useAsyncLoad } from '../utils/useAsyncLoad';

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

  const fetchPolicies = useCallback(async () => {
    try {
      setLoading(true);
      const data = await listPolicies();
      setPolicies(data || []);
      setError('');
    } catch (err) {
      console.error('Failed to fetch policies:', err);
      setError(getErrorMessage(err, 'Failed to load policies'));
    } finally {
      setLoading(false);
    }
  }, []);

  useAsyncLoad(fetchPolicies);

  const handleDeletePolicy = async (id: string) => {
    if (!confirm('Are you sure you want to delete this policy?')) return;

    try {
      await deletePolicy(id);
      await fetchPolicies();
    } catch (err) {
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
                          className="btn-icon hover:text-red-400! hover:bg-red-500/10!"
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
  // Bucket configuration changes (each is also covered by s3:*).
  config: [
    { action: 's3:PutBucketVersioning', label: 'Put Bucket Versioning', description: 'Enable or suspend versioning' },
    { action: 's3:GetLifecycleConfiguration', label: 'Get Lifecycle Config', description: 'Read lifecycle (expiry) rules' },
    { action: 's3:PutLifecycleConfiguration', label: 'Put Lifecycle Config', description: 'Change lifecycle (expiry) rules' },
    { action: 's3:PutReplicationConfiguration', label: 'Put Replication Config', description: 'Configure bucket replication' },
    { action: 's3:PutBucketNotification', label: 'Put Bucket Notification', description: 'Configure event notifications' },
    { action: 's3:PutBucketObjectLockConfiguration', label: 'Put Object Lock Config', description: 'Configure object lock / retention' },
    { action: 's3:PutBucketQuota', label: 'Put Bucket Quota', description: 'Set bucket size / object quotas' },
  ],
};

type ActionCategory = keyof typeof S3_ACTIONS;
const ACTION_CATEGORIES: { key: ActionCategory; title: string }[] = [
  { key: 'read', title: 'read' },
  { key: 'write', title: 'write' },
  { key: 'bucket', title: 'bucket' },
  { key: 'config', title: 'bucket config' },
];
const ACTION_INFO = ACTION_CATEGORIES.flatMap(c => S3_ACTIONS[c.key]);
const ALL_ACTIONS = ACTION_INFO.map(a => a.action);

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

interface PolicyStatement {
  Effect: 'Allow' | 'Deny';
  Action: string[];
  Resource: string[];
}

interface BuilderState {
  buckets: string[];
  actions: string[];
  effect: 'Allow' | 'Deny';
  advancedMode: boolean;
  bucketPermissions: BucketPermissions;
}

// Builder selections for the policy being edited (empty for a new policy).
const initialBuilderState = (policy: Policy | null): BuilderState => {
  const state: BuilderState = { buckets: [], actions: [], effect: 'Allow', advancedMode: false, bucketPermissions: {} };
  if (!policy) return state;

  state.buckets = extractBucketsFromPolicy(policy.document);
  const { actions, effect } = extractActionsFromPolicy(policy.document);
  state.actions = actions;
  state.effect = effect;

  // A multi-statement policy is edited in advanced (per-bucket) mode
  try {
    const doc = JSON.parse(policy.document);
    if (doc.Statement && doc.Statement.length > 1) {
      state.advancedMode = true;
      state.bucketPermissions = extractPerBucketPermissions(policy.document, state.buckets);
    }
  } catch {
    // Ignore parse errors
  }
  return state;
};

// Identity of the builder inputs that affect the generated document in the
// current mode (simple mode ignores per-bucket permissions and vice versa).
const builderKey = (
  advancedMode: boolean,
  effect: 'Allow' | 'Deny',
  actions: string[],
  buckets: string[],
  bucketPermissions: BucketPermissions
): string =>
  advancedMode
    ? JSON.stringify([true, buckets, bucketPermissions])
    : JSON.stringify([false, effect, actions, buckets]);

const bucketResources = (bucket: string): string[] => [
  `arn:aws:s3:::${bucket}`,
  `arn:aws:s3:::${bucket}/*`,
];

// Simple mode: one statement over the selected buckets (all buckets if none).
// Returns null when there is nothing to generate (no actions selected).
const buildSimplePolicyDocument = (
  effect: 'Allow' | 'Deny',
  actions: string[],
  buckets: string[]
): string | null => {
  if (actions.length === 0) return null;
  const resources = buckets.length > 0
    ? buckets.flatMap(bucketResources)
    : ['arn:aws:s3:::*', 'arn:aws:s3:::*/*'];
  const statement: PolicyStatement = { Effect: effect, Action: actions, Resource: resources };
  return JSON.stringify({ Version: '2012-10-17', Statement: [statement] }, null, 2);
};

// Advanced mode: one statement per selected bucket that has actions.
// Returns null when no bucket has any action yet.
const buildAdvancedPolicyDocument = (
  buckets: string[],
  bucketPermissions: BucketPermissions
): string | null => {
  const statements: PolicyStatement[] = [];
  for (const bucketName of buckets) {
    const perms = bucketPermissions[bucketName];
    if (perms && perms.actions.length > 0) {
      statements.push({ Effect: perms.effect, Action: perms.actions, Resource: bucketResources(bucketName) });
    }
  }
  if (statements.length === 0) return null;
  return JSON.stringify({ Version: '2012-10-17', Statement: statements }, null, 2);
};

// Suggested name/description for a simple-mode policy.
const autoPolicyName = (
  effect: 'Allow' | 'Deny',
  actions: string[],
  buckets: string[]
): { name: string; description: string } => {
  const actionDesc = actions.length === ALL_ACTIONS.length ? 'Full Access' : `${actions.length} Actions`;
  let bucketDesc: string;
  if (buckets.length === 0) {
    bucketDesc = 'All Buckets';
  } else if (buckets.length === 1) {
    bucketDesc = buckets[0];
  } else {
    bucketDesc = `${buckets.length} Buckets`;
  }
  return {
    name: `${bucketDesc} - ${actionDesc}`,
    description: `${effect}s ${actionDesc.toLowerCase()} on ${bucketDesc.toLowerCase()}`,
  };
};

interface PolicyModalProps {
  policy: Policy | null;
  onClose: () => void;
  onSuccess: () => void;
}

function PolicyModal({ policy, onClose, onSuccess }: PolicyModalProps) {
  const isEditMode = policy !== null;
  // Builder selections parsed from the policy being edited (computed once:
  // the modal is remounted for every policy it opens).
  const [initial] = useState(() => initialBuilderState(policy));

  // Basic fields
  const [name, setName] = useState(policy?.name || '');
  const [description, setDescription] = useState(policy?.description || '');
  const [document, setDocument] = useState(policy?.document || '');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  // User/bucket data
  const [buckets, setBuckets] = useState<Bucket[]>([]);
  const [selectedBuckets, setSelectedBuckets] = useState<string[]>(initial.buckets);
  const [loadingBuckets, setLoadingBuckets] = useState(true);
  const [users, setUsers] = useState<User[]>([]);
  const [selectedUserId, setSelectedUserId] = useState<string>('');
  const [loadingUsers, setLoadingUsers] = useState(true);

  // Simple mode state
  const [selectedActions, setSelectedActions] = useState<string[]>(initial.actions);
  const [effect, setEffect] = useState<'Allow' | 'Deny'>(initial.effect);

  // Advanced mode state
  const [advancedMode, setAdvancedMode] = useState(initial.advancedMode);
  const [bucketPermissions, setBucketPermissions] = useState<BucketPermissions>(initial.bucketPermissions);
  const [expandedBuckets, setExpandedBuckets] = useState<Set<string>>(new Set());

  // Track if name was manually edited
  const [nameManuallyEdited, setNameManuallyEdited] = useState(isEditMode);

  // Track manual edits to the raw JSON
  const [jsonManuallyEdited, setJsonManuallyEdited] = useState(false);

  // Validity of the raw JSON document (derived, recomputed when it changes)
  const jsonError = useMemo(() => {
    if (!document.trim()) return '';
    try {
      JSON.parse(document);
      return '';
    } catch (err) {
      return `Invalid JSON: ${(err as Error).message}`;
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

  // Regenerate the policy document (and, in simple mode, the auto-name) from
  // the visual builder. An Effect Event: it reads the latest state without
  // making the effect below re-run when e.g. the name or JSON is edited.
  const regenerateFromBuilder = useEffectEvent(() => {
    const doc = advancedMode
      ? buildAdvancedPolicyDocument(selectedBuckets, bucketPermissions)
      : buildSimplePolicyDocument(effect, selectedActions, selectedBuckets);
    if (doc === null) return;
    if (!confirmOverwriteJson()) return;
    setDocument(doc);

    // Auto-generate name if not manually edited
    if (!advancedMode && !nameManuallyEdited) {
      const auto = autoPolicyName(effect, selectedActions, selectedBuckets);
      setName(auto.name);
      setDescription(auto.description);
    }
  });

  // Auto-update the policy document when the builder selections change.
  // Keyed on the mode-relevant inputs so the stored document of a policy being
  // edited is shown as-is until the user actually changes a selection (and a
  // StrictMode double-invoke doesn't regenerate/prompt twice).
  const lastBuilderKey = useRef(
    builderKey(initial.advancedMode, initial.effect, initial.actions, initial.buckets, initial.bucketPermissions)
  );
  useEffect(() => {
    const key = builderKey(advancedMode, effect, selectedActions, selectedBuckets, bucketPermissions);
    if (key === lastBuilderKey.current) return;
    lastBuilderKey.current = key;
    regenerateFromBuilder();
  }, [selectedActions, effect, selectedBuckets, advancedMode, bucketPermissions]);

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
    } catch (err) {
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

  const handleSelectCategoryActions = (category: ActionCategory) => {
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
      <div className="modal-panel max-w-4xl! p-0! overflow-hidden! flex flex-col">
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

              <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4">
                {ACTION_CATEGORIES.map(({ key: category, title }) => (
                  <div key={category} className="space-y-2">
                    <div className="flex items-center justify-between mb-2">
                      <h4 className="text-xs font-medium uppercase tracking-wider text-dark-textSecondary">{title}</h4>
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
                              const actionInfo = ACTION_INFO.find(a => a.action === action);
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
                jsonError ? 'border-red-500/60! focus:ring-red-500/50!' : ''
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
            {loading && <span className="spinner w-4! h-4!" />}
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
  const formattedDoc = useMemo(() => {
    try {
      return JSON.stringify(JSON.parse(policy.document), null, 2);
    } catch {
      return policy.document;
    }
  }, [policy.document]);

  return (
    <div className="modal-overlay">
      <div className="modal-panel max-w-4xl! p-0! overflow-hidden! flex flex-col">
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
