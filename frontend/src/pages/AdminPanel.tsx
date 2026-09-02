import { useState, useEffect } from 'react';
import { Settings, Users, Shield, Trash2, X, UserPlus, Lock, Unlock, Key, Plus } from 'lucide-react';
import api, { userApi, groupApi } from '../services/api';
import { listPolicies, attachPolicyToUser, detachPolicyFromUser, Policy } from '../services/policy';
import type { User, Group } from '../types';
import { getErrorMessage } from '../utils/errors';

interface AccessKey {
  id: string;
  access_key: string;
  is_active: boolean;
  last_used_at?: string;
  created_at: string;
}

export default function AdminPanel() {
  const [users, setUsers] = useState<User[]>([]);
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [groups, setGroups] = useState<Group[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [selectedUser, setSelectedUser] = useState<User | null>(null);
  const [showPolicyModal, setShowPolicyModal] = useState(false);
  const [showCreateUserModal, setShowCreateUserModal] = useState(false);
  const [showAccessKeysModal, setShowAccessKeysModal] = useState(false);
  const [showCreateGroupModal, setShowCreateGroupModal] = useState(false);
  const [selectedGroupId, setSelectedGroupId] = useState<string | null>(null);

  useEffect(() => {
    loadData();
  }, []);

  const loadData = async () => {
    try {
      setLoading(true);
      setLoadError('');
      const [usersData, policiesData, groupsData] = await Promise.all([
        api.get<User[]>('/users').then(res => res.data),
        listPolicies(),
        groupApi.listGroups(),
      ]);
      setUsers(usersData);
      setPolicies(policiesData);
      setGroups(groupsData);
    } catch (error) {
      console.error('Failed to load admin data:', error);
      setLoadError(getErrorMessage(error, 'Failed to load admin data'));
    } finally {
      setLoading(false);
    }
  };

  const loadGroups = async () => {
    try {
      const groupsData = await groupApi.listGroups();
      setGroups(groupsData);
    } catch (error) {
      console.error('Failed to reload groups:', error);
    }
  };

  const handleDeleteGroup = async (group: Group) => {
    if (!confirm(`Are you sure you want to delete the group "${group.name}"?`)) return;

    try {
      await groupApi.deleteGroup(group.id);
      await loadGroups();
    } catch (err: any) {
      alert(getErrorMessage(err, 'Failed to delete group'));
    }
  };

  const handleDeleteUser = async (userId: string) => {
    if (!confirm('Are you sure you want to delete this user?')) return;

    try {
      await api.delete(`/users/${userId}`);
      await loadData();
    } catch (err: any) {
      alert(getErrorMessage(err, 'Failed to delete user'));
    }
  };

  const handleManagePolicies = (user: User) => {
    setSelectedUser(user);
    setShowPolicyModal(true);
  };

  const handleToggleLock = async (user: User) => {
    const action = user.is_locked ? 'unlock' : 'lock';
    if (!confirm(`Are you sure you want to ${action} ${user.username}?`)) return;

    try {
      await api.post(`/users/${user.id}/${action}`);
      await loadData();
    } catch (err: any) {
      alert(getErrorMessage(err, `Failed to ${action} user`));
    }
  };

  const handleManageAccessKeys = (user: User) => {
    setSelectedUser(user);
    setShowAccessKeysModal(true);
  };

  // The detail modal reads the group from the live `groups` state so member /
  // policy changes (which reload the list) are reflected while it is open.
  const selectedGroup = selectedGroupId
    ? groups.find((g) => g.id === selectedGroupId) ?? null
    : null;

  if (loading) {
    return (
      <div className="page">
        <div className="flex flex-col items-center justify-center h-64 gap-3">
          <div className="spinner" />
          <p className="text-sm text-dark-textSecondary">Loading admin data…</p>
        </div>
      </div>
    );
  }

  return (
    <div className="page">
      <div className="flex items-start justify-between gap-4 mb-8">
        <div>
          <h1 className="page-title">Admin Panel</h1>
          <p className="page-subtitle">System administration and user management</p>
        </div>
        <button onClick={() => setShowCreateUserModal(true)} className="btn-primary">
          <UserPlus className="w-4 h-4" />
          Create User
        </button>
      </div>

      {loadError && <div className="alert-error mb-6">{loadError}</div>}

      {/* Statistics */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4 mb-8">
        <div className="card p-5 flex items-center gap-4">
          <span className="bg-blue-500/10 text-blue-500 p-2.5 rounded-lg">
            <Users className="w-5 h-5" />
          </span>
          <div>
            <p className="text-2xl font-semibold text-dark-text tabular-nums">{users.length}</p>
            <p className="text-xs text-dark-textSecondary uppercase tracking-wider">Total Users</p>
          </div>
        </div>

        <div className="card p-5 flex items-center gap-4">
          <span className="bg-orange-500/10 text-orange-500 p-2.5 rounded-lg">
            <Shield className="w-5 h-5" />
          </span>
          <div>
            <p className="text-2xl font-semibold text-dark-text tabular-nums">{policies.length}</p>
            <p className="text-xs text-dark-textSecondary uppercase tracking-wider">Active Policies</p>
          </div>
        </div>

        <div className="card p-5 flex items-center gap-4">
          <span className="bg-green-500/10 text-green-500 p-2.5 rounded-lg">
            <Settings className="w-5 h-5" />
          </span>
          <div>
            <p className="text-2xl font-semibold text-dark-text tabular-nums">
              {users.filter(u => u.sso_provider).length}
            </p>
            <p className="text-xs text-dark-textSecondary uppercase tracking-wider">SSO Users</p>
          </div>
        </div>
      </div>

      {/* User Management */}
      <div className="table-wrap">
        <div className="p-5 border-b border-dark-border">
          <h2 className="text-base font-semibold text-dark-text">User Management</h2>
          <p className="text-sm text-dark-textSecondary mt-0.5">
            Manage users and their policy assignments
          </p>
        </div>

        <div className="overflow-x-auto">
          <table className="table">
            <thead>
              <tr>
                <th>User</th>
                <th>Status</th>
                <th>Type</th>
                <th>Policies</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {users.map((user) => (
                <tr key={user.id}>
                  <td className="whitespace-nowrap">
                    <div className="text-sm font-medium text-dark-text">{user.username}</div>
                    <div className="text-xs text-dark-textMuted">{user.email}</div>
                  </td>
                  <td className="whitespace-nowrap">
                    <div className="flex items-center gap-1.5">
                      {user.is_locked ? (
                        <span className="badge-red">
                          <Lock className="w-3 h-3" />
                          Locked
                        </span>
                      ) : (
                        <span className="badge-green">Active</span>
                      )}
                      {user.is_admin && <span className="badge-blue">Admin</span>}
                    </div>
                  </td>
                  <td className="whitespace-nowrap">
                    {user.sso_provider ? (
                      <span className="badge-purple">{user.sso_provider.toUpperCase()} SSO</span>
                    ) : (
                      <span className="badge-gray">Local</span>
                    )}
                  </td>
                  <td className="whitespace-nowrap">
                    <button
                      onClick={() => handleManagePolicies(user)}
                      className="text-sm font-medium text-blue-400 hover:text-blue-300 transition-colors"
                    >
                      {user.policies?.length || 0} policies
                    </button>
                  </td>
                  <td className="whitespace-nowrap">
                    <div className="flex items-center gap-1">
                      <button
                        onClick={() => handleManagePolicies(user)}
                        className="btn-icon"
                        title="Manage policies"
                      >
                        <Shield className="w-4 h-4" />
                      </button>
                      <button
                        onClick={() => handleManageAccessKeys(user)}
                        className="btn-icon"
                        title="Manage access keys"
                      >
                        <Key className="w-4 h-4" />
                      </button>
                      {!user.is_admin && (
                        <>
                          <button
                            onClick={() => handleToggleLock(user)}
                            className="btn-icon"
                            title={user.is_locked ? 'Unlock user' : 'Lock user'}
                          >
                            {user.is_locked ? <Unlock className="w-4 h-4" /> : <Lock className="w-4 h-4" />}
                          </button>
                          <button
                            onClick={() => handleDeleteUser(user.id)}
                            className="btn-icon hover:!text-red-400 hover:!bg-red-500/10"
                            title="Delete user"
                          >
                            <Trash2 className="w-4 h-4" />
                          </button>
                        </>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {/* Groups */}
      <div className="table-wrap mt-8">
        <div className="p-5 border-b border-dark-border flex items-start justify-between gap-4">
          <div>
            <h2 className="text-base font-semibold text-dark-text">Groups</h2>
            <p className="text-sm text-dark-textSecondary mt-0.5">
              Organize users and share policy assignments
            </p>
          </div>
          <button onClick={() => setShowCreateGroupModal(true)} className="btn-secondary btn-sm">
            <Plus className="w-4 h-4" />
            New Group
          </button>
        </div>

        {groups.length === 0 ? (
          <div className="empty-state !py-10">
            <Users className="empty-state-icon" />
            <p className="text-sm text-dark-textSecondary">
              No groups yet. Create one to assign policies to several users at once.
            </p>
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="table">
              <thead>
                <tr>
                  <th>Group</th>
                  <th>Members</th>
                  <th>Policies</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {groups.map((group) => (
                  <tr key={group.id}>
                    <td className="whitespace-nowrap">
                      <div className="text-sm font-medium text-dark-text">{group.name}</div>
                      {group.description && (
                        <div className="text-xs text-dark-textMuted">{group.description}</div>
                      )}
                    </td>
                    <td className="whitespace-nowrap tabular-nums text-dark-textSecondary">
                      {group.users?.length || 0} member{(group.users?.length || 0) !== 1 ? 's' : ''}
                    </td>
                    <td>
                      {group.policies && group.policies.length > 0 ? (
                        <div className="flex flex-wrap gap-1.5">
                          {group.policies.map((policy) => (
                            <span key={policy.id} className="badge-blue">{policy.name}</span>
                          ))}
                        </div>
                      ) : (
                        <span className="text-xs text-dark-textMuted">None</span>
                      )}
                    </td>
                    <td className="whitespace-nowrap">
                      <div className="flex items-center gap-1">
                        <button
                          onClick={() => setSelectedGroupId(group.id)}
                          className="btn-icon"
                          title="Manage group"
                        >
                          <Settings className="w-4 h-4" />
                        </button>
                        <button
                          onClick={() => handleDeleteGroup(group)}
                          className="btn-icon hover:!text-red-400 hover:!bg-red-500/10"
                          title="Delete group"
                        >
                          <Trash2 className="w-4 h-4" />
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Policy Assignment Modal */}
      {showPolicyModal && selectedUser && (
        <PolicyAssignmentModal
          user={selectedUser}
          policies={policies}
          onClose={() => {
            setShowPolicyModal(false);
            setSelectedUser(null);
            loadData();
          }}
        />
      )}

      {/* Create User Modal */}
      {showCreateUserModal && (
        <CreateUserModal
          onClose={() => setShowCreateUserModal(false)}
          onSuccess={() => {
            setShowCreateUserModal(false);
            loadData();
          }}
        />
      )}

      {/* Access Keys Modal */}
      {showAccessKeysModal && selectedUser && (
        <AccessKeysModal
          user={selectedUser}
          onClose={() => {
            setShowAccessKeysModal(false);
            setSelectedUser(null);
          }}
        />
      )}

      {/* Create Group Modal */}
      {showCreateGroupModal && (
        <CreateGroupModal
          onClose={() => setShowCreateGroupModal(false)}
          onSuccess={() => {
            setShowCreateGroupModal(false);
            loadGroups();
          }}
        />
      )}

      {/* Group Detail Modal */}
      {selectedGroup && (
        <GroupDetailModal
          group={selectedGroup}
          users={users}
          policies={policies}
          onChanged={loadGroups}
          onClose={() => setSelectedGroupId(null)}
        />
      )}
    </div>
  );
}

function PolicyAssignmentModal({
  user,
  policies,
  onClose
}: {
  user: User;
  policies: Policy[];
  onClose: () => void;
}) {
  const [userPolicies, setUserPolicies] = useState<string[]>(user.policies?.map(p => p.id) || []);
  const [loading, setLoading] = useState(false);

  const handleTogglePolicy = async (policyId: string) => {
    const isAttached = userPolicies.includes(policyId);
    setLoading(true);

    try {
      if (isAttached) {
        await detachPolicyFromUser(user.id, policyId);
        setUserPolicies(prev => prev.filter(id => id !== policyId));
      } else {
        await attachPolicyToUser(user.id, policyId);
        setUserPolicies(prev => [...prev, policyId]);
      }
    } catch (err: any) {
      alert(getErrorMessage(err, 'Failed to update policy'));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="modal-overlay">
      <div className="modal-panel !max-w-2xl">
        <div className="flex items-center justify-between mb-5">
          <div>
            <h2 className="modal-title">Manage Policies</h2>
            <p className="text-sm text-dark-textSecondary mt-0.5">
              User: <span className="font-medium text-dark-text">{user.username}</span>
            </p>
          </div>
          <button onClick={onClose} className="btn-icon">
            <X className="w-4 h-4" />
          </button>
        </div>

        {policies.length === 0 ? (
          <div className="empty-state !py-10">
            <Shield className="empty-state-icon" />
            <p className="text-sm text-dark-textSecondary">No policies available</p>
          </div>
        ) : (
          <div className="space-y-2">
            {policies.map((policy) => {
              const isAttached = userPolicies.includes(policy.id);
              return (
                <div
                  key={policy.id}
                  className="flex items-center justify-between gap-4 p-4 bg-dark-inset border border-dark-border rounded-lg"
                >
                  <div className="flex-1 min-w-0">
                    <h3 className="text-sm font-medium text-dark-text">{policy.name}</h3>
                    <p className="text-xs text-dark-textSecondary mt-1">{policy.description}</p>
                  </div>
                  <button
                    onClick={() => handleTogglePolicy(policy.id)}
                    disabled={loading}
                    className={`btn-sm shrink-0 ${isAttached ? 'btn-danger-ghost' : 'btn-secondary'}`}
                  >
                    {isAttached ? 'Detach' : 'Attach'}
                  </button>
                </div>
              );
            })}
          </div>
        )}

        <div className="flex justify-end mt-6">
          <button onClick={onClose} className="btn-primary">
            Done
          </button>
        </div>
      </div>
    </div>
  );
}

function CreateUserModal({ onClose, onSuccess }: { onClose: () => void; onSuccess: () => void }) {
  const [username, setUsername] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [isAdmin, setIsAdmin] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setLoading(true);

    try {
      await userApi.createUser(username, email, password, isAdmin);
      onSuccess();
    } catch (err: any) {
      setError(getErrorMessage(err, 'Failed to create user'));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="modal-overlay">
      <div className="modal-panel">
        <div className="flex items-center justify-between mb-5">
          <div>
            <h2 className="modal-title">Create User</h2>
            <p className="text-sm text-dark-textSecondary mt-0.5">Add a new local user account</p>
          </div>
          <button onClick={onClose} className="btn-icon">
            <X className="w-4 h-4" />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          {error && <div className="alert-error">{error}</div>}

          <div>
            <label className="label">Username</label>
            <input
              type="text"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              className="input"
              placeholder="john_doe"
              required
              minLength={3}
            />
          </div>

          <div>
            <label className="label">Email</label>
            <input
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              className="input"
              placeholder="john@example.com"
              required
            />
          </div>

          <div>
            <label className="label">Password</label>
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className="input"
              placeholder="••••••••"
              required
              minLength={8}
            />
            <p className="help-text">Minimum 8 characters</p>
          </div>

          <div className="flex items-center">
            <input
              type="checkbox"
              id="is_admin"
              checked={isAdmin}
              onChange={(e) => setIsAdmin(e.target.checked)}
              className="w-4 h-4 text-blue-600 bg-dark-inset border-dark-border rounded focus:ring-blue-500"
            />
            <label htmlFor="is_admin" className="ml-2 text-sm text-dark-text">
              Grant administrator privileges
            </label>
          </div>
        </form>

        <div className="flex justify-end gap-2 mt-6">
          <button type="button" onClick={onClose} className="btn-ghost">
            Cancel
          </button>
          <button onClick={handleSubmit} disabled={loading} className="btn-primary">
            {loading && <span className="spinner !w-4 !h-4" />}
            {loading ? 'Creating...' : 'Create User'}
          </button>
        </div>
      </div>
    </div>
  );
}

function AccessKeysModal({ user, onClose }: { user: User; onClose: () => void }) {
  const [accessKeys, setAccessKeys] = useState<AccessKey[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    loadAccessKeys();
  }, [user.id]);

  const loadAccessKeys = async () => {
    try {
      setLoading(true);
      setError('');
      const response = await api.get<AccessKey[]>(`/users/${user.id}/access-keys`);
      setAccessKeys(response.data);
    } catch (error) {
      console.error('Failed to load access keys:', error);
      setError(getErrorMessage(error, 'Failed to load access keys'));
    } finally {
      setLoading(false);
    }
  };

  const handleDeleteKey = async (keyId: string) => {
    if (!confirm('Are you sure you want to delete this access key? This action cannot be undone.')) return;

    try {
      await api.delete(`/users/${user.id}/access-keys/${keyId}`);
      await loadAccessKeys();
    } catch (err: any) {
      alert(getErrorMessage(err, 'Failed to delete access key'));
    }
  };

  const formatDate = (dateString: string) => {
    return new Date(dateString).toLocaleString();
  };

  return (
    <div className="modal-overlay">
      <div className="modal-panel !max-w-2xl">
        <div className="flex items-center justify-between mb-5">
          <div>
            <h2 className="modal-title">Access Keys</h2>
            <p className="text-sm text-dark-textSecondary mt-0.5">
              User: <span className="font-medium text-dark-text">{user.username}</span>
            </p>
          </div>
          <button onClick={onClose} className="btn-icon">
            <X className="w-4 h-4" />
          </button>
        </div>

        {error && <div className="alert-error mb-4">{error}</div>}

        {loading ? (
          <div className="flex flex-col items-center justify-center py-12 gap-3">
            <div className="spinner" />
            <p className="text-sm text-dark-textSecondary">Loading access keys…</p>
          </div>
        ) : accessKeys.length === 0 ? (
          <div className="empty-state !py-10">
            <Key className="empty-state-icon" />
            <p className="text-sm text-dark-textSecondary">No access keys found for this user</p>
          </div>
        ) : (
          <div className="space-y-3">
            {accessKeys.map((key) => (
              <div
                key={key.id}
                className="flex items-center justify-between gap-4 p-4 bg-dark-inset border border-dark-border rounded-lg"
              >
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 mb-2">
                    <code className="kbd-mono">{key.access_key}</code>
                    {key.is_active ? (
                      <span className="badge-green">Active</span>
                    ) : (
                      <span className="badge-red">Inactive</span>
                    )}
                  </div>
                  <div className="flex gap-4 text-xs text-dark-textSecondary tabular-nums">
                    <span>Created: {formatDate(key.created_at)}</span>
                    {key.last_used_at && (
                      <span>Last used: {formatDate(key.last_used_at)}</span>
                    )}
                  </div>
                </div>
                <button
                  onClick={() => handleDeleteKey(key.id)}
                  className="btn-icon shrink-0 hover:!text-red-400 hover:!bg-red-500/10"
                  title="Delete access key"
                >
                  <Trash2 className="w-4 h-4" />
                </button>
              </div>
            ))}
          </div>
        )}

        <div className="flex justify-between items-center mt-6">
          <p className="text-sm text-dark-textSecondary tabular-nums">
            Total: {accessKeys.length} key{accessKeys.length !== 1 ? 's' : ''}
            {' '}({accessKeys.filter(k => k.is_active).length} active)
          </p>
          <button onClick={onClose} className="btn-secondary">
            Close
          </button>
        </div>
      </div>
    </div>
  );
}

function CreateGroupModal({ onClose, onSuccess }: { onClose: () => void; onSuccess: () => void }) {
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setLoading(true);

    try {
      await groupApi.createGroup(name.trim(), description.trim() || undefined);
      onSuccess();
    } catch (err: any) {
      setError(getErrorMessage(err, 'Failed to create group'));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="modal-overlay">
      <div className="modal-panel">
        <div className="flex items-center justify-between mb-5">
          <div>
            <h2 className="modal-title">Create Group</h2>
            <p className="text-sm text-dark-textSecondary mt-0.5">
              Group users to manage their policies together
            </p>
          </div>
          <button onClick={onClose} className="btn-icon">
            <X className="w-4 h-4" />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          {error && <div className="alert-error">{error}</div>}

          <div>
            <label className="label">Name</label>
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="input"
              placeholder="e.g. developers"
              required
            />
          </div>

          <div>
            <label className="label">Description</label>
            <input
              type="text"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              className="input"
              placeholder="What is this group for? (optional)"
            />
          </div>

          <div className="flex justify-end gap-2 mt-6">
            <button type="button" onClick={onClose} className="btn-ghost">
              Cancel
            </button>
            <button type="submit" disabled={loading || !name.trim()} className="btn-primary">
              {loading && <span className="spinner !w-4 !h-4" />}
              {loading ? 'Creating...' : 'Create Group'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function GroupDetailModal({
  group,
  users,
  policies,
  onChanged,
  onClose
}: {
  group: Group;
  users: User[];
  policies: Policy[];
  onChanged: () => void | Promise<void>;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [addUserId, setAddUserId] = useState('');
  const [attachPolicyId, setAttachPolicyId] = useState('');

  const members = group.users || [];
  const attachedPolicies = group.policies || [];
  const memberIds = new Set(members.map((u) => u.id));
  const attachedPolicyIds = new Set(attachedPolicies.map((p) => p.id));
  const availableUsers = users.filter((u) => !memberIds.has(u.id));
  const availablePolicies = policies.filter((p) => !attachedPolicyIds.has(p.id));

  const run = async (fn: () => Promise<void>, fallback: string) => {
    setBusy(true);
    setError('');
    try {
      await fn();
      await onChanged();
    } catch (err: any) {
      setError(getErrorMessage(err, fallback));
    } finally {
      setBusy(false);
    }
  };

  const handleAddMember = async () => {
    if (!addUserId) return;
    await run(() => groupApi.addMember(group.id, addUserId), 'Failed to add member');
    setAddUserId('');
  };

  const handleRemoveMember = async (userId: string) => {
    await run(() => groupApi.removeMember(group.id, userId), 'Failed to remove member');
  };

  const handleAttachPolicy = async () => {
    if (!attachPolicyId) return;
    await run(() => groupApi.attachPolicy(group.id, attachPolicyId), 'Failed to attach policy');
    setAttachPolicyId('');
  };

  const handleDetachPolicy = async (policyId: string) => {
    await run(() => groupApi.detachPolicy(group.id, policyId), 'Failed to detach policy');
  };

  return (
    <div className="modal-overlay">
      <div className="modal-panel !max-w-2xl">
        <div className="flex items-center justify-between mb-5">
          <div>
            <h2 className="modal-title">Manage Group</h2>
            <p className="text-sm text-dark-textSecondary mt-0.5">
              Group: <span className="font-medium text-dark-text">{group.name}</span>
            </p>
          </div>
          <button onClick={onClose} className="btn-icon">
            <X className="w-4 h-4" />
          </button>
        </div>

        {error && <div className="alert-error mb-4">{error}</div>}

        <div className="space-y-6">
          {/* Members */}
          <div>
            <h3 className="text-base font-semibold text-dark-text mb-3">Members</h3>
            {members.length === 0 ? (
              <p className="text-sm text-dark-textSecondary mb-3">No members yet.</p>
            ) : (
              <div className="space-y-2 mb-3">
                {members.map((member) => (
                  <div
                    key={member.id}
                    className="flex items-center justify-between gap-4 px-4 py-3 bg-dark-inset border border-dark-border rounded-lg"
                  >
                    <div className="flex-1 min-w-0">
                      <p className="text-sm font-medium text-dark-text truncate">{member.username}</p>
                      <p className="text-xs text-dark-textMuted truncate">{member.email}</p>
                    </div>
                    <button
                      onClick={() => handleRemoveMember(member.id)}
                      disabled={busy}
                      className="btn-icon shrink-0 hover:!text-red-400 hover:!bg-red-500/10"
                      title="Remove from group"
                    >
                      <X className="w-4 h-4" />
                    </button>
                  </div>
                ))}
              </div>
            )}
            <div className="flex items-center gap-2">
              <select
                value={addUserId}
                onChange={(e) => setAddUserId(e.target.value)}
                className="input flex-1"
                disabled={busy || availableUsers.length === 0}
              >
                <option value="">
                  {availableUsers.length === 0 ? 'All users are members' : 'Select a user…'}
                </option>
                {availableUsers.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.username} ({u.email})
                  </option>
                ))}
              </select>
              <button
                onClick={handleAddMember}
                disabled={busy || !addUserId}
                className="btn-secondary btn-sm shrink-0"
              >
                <UserPlus className="w-4 h-4" />
                Add
              </button>
            </div>
          </div>

          {/* Policies */}
          <div className="pt-6 border-t border-dark-border">
            <h3 className="text-base font-semibold text-dark-text mb-3">Policies</h3>
            {attachedPolicies.length === 0 ? (
              <p className="text-sm text-dark-textSecondary mb-3">No policies attached.</p>
            ) : (
              <div className="space-y-2 mb-3">
                {attachedPolicies.map((policy) => (
                  <div
                    key={policy.id}
                    className="flex items-center justify-between gap-4 px-4 py-3 bg-dark-inset border border-dark-border rounded-lg"
                  >
                    <div className="flex-1 min-w-0">
                      <p className="text-sm font-medium text-dark-text truncate">{policy.name}</p>
                      {policy.description && (
                        <p className="text-xs text-dark-textMuted truncate">{policy.description}</p>
                      )}
                    </div>
                    <button
                      onClick={() => handleDetachPolicy(policy.id)}
                      disabled={busy}
                      className="btn-icon shrink-0 hover:!text-red-400 hover:!bg-red-500/10"
                      title="Detach policy"
                    >
                      <X className="w-4 h-4" />
                    </button>
                  </div>
                ))}
              </div>
            )}
            <div className="flex items-center gap-2">
              <select
                value={attachPolicyId}
                onChange={(e) => setAttachPolicyId(e.target.value)}
                className="input flex-1"
                disabled={busy || availablePolicies.length === 0}
              >
                <option value="">
                  {availablePolicies.length === 0 ? 'All policies attached' : 'Select a policy…'}
                </option>
                {availablePolicies.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
              <button
                onClick={handleAttachPolicy}
                disabled={busy || !attachPolicyId}
                className="btn-secondary btn-sm shrink-0"
              >
                <Shield className="w-4 h-4" />
                Attach
              </button>
            </div>
          </div>
        </div>

        <div className="flex justify-end mt-6">
          <button onClick={onClose} className="btn-primary">
            Done
          </button>
        </div>
      </div>
    </div>
  );
}
