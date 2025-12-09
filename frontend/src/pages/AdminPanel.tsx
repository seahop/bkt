import { useState, useEffect } from 'react';
import { Settings, Users, Shield, Trash2, Plus, X, UserPlus, Lock, Unlock, Key } from 'lucide-react';
import api, { userApi } from '../services/api';
import { listPolicies, attachPolicyToUser, detachPolicyFromUser, Policy } from '../services/policy';

interface User {
  id: string;
  username: string;
  email: string;
  is_admin: boolean;
  is_locked?: boolean;
  sso_provider?: string;
  created_at: string;
  policies?: Policy[];
}

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
  const [loading, setLoading] = useState(true);
  const [selectedUser, setSelectedUser] = useState<User | null>(null);
  const [showPolicyModal, setShowPolicyModal] = useState(false);
  const [showCreateUserModal, setShowCreateUserModal] = useState(false);
  const [showAccessKeysModal, setShowAccessKeysModal] = useState(false);

  useEffect(() => {
    loadData();
  }, []);

  const loadData = async () => {
    try {
      setLoading(true);
      const [usersData, policiesData] = await Promise.all([
        api.get<User[]>('/users').then(res => res.data),
        listPolicies(),
      ]);
      setUsers(usersData);
      setPolicies(policiesData);
    } catch (error) {
      console.error('Failed to load admin data:', error);
    } finally {
      setLoading(false);
    }
  };

  const handleDeleteUser = async (userId: string) => {
    if (!confirm('Are you sure you want to delete this user?')) return;

    try {
      await api.delete(`/users/${userId}`);
      await loadData();
    } catch (err: any) {
      alert(err.response?.data?.message || 'Failed to delete user');
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
      alert(err.response?.data?.message || `Failed to ${action} user`);
    }
  };

  const handleManageAccessKeys = (user: User) => {
    setSelectedUser(user);
    setShowAccessKeysModal(true);
  };

  if (loading) {
    return (
      <div className="p-8">
        <div className="flex items-center justify-center h-64">
          <Settings className="w-12 h-12 text-dark-textSecondary animate-spin" />
        </div>
      </div>
    );
  }

  return (
    <div className="p-8">
      <div className="mb-8 flex justify-between items-start">
        <div>
          <h1 className="text-3xl font-bold text-dark-text mb-2">Admin Panel</h1>
          <p className="text-dark-textSecondary">System administration and user management</p>
        </div>
        <button
          onClick={() => setShowCreateUserModal(true)}
          className="flex items-center gap-2 px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded-lg transition-colors"
        >
          <UserPlus className="w-5 h-5" />
          Create User
        </button>
      </div>

      {/* Statistics */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
        <div className="bg-dark-surface border border-dark-border rounded-lg p-6">
          <div className="flex items-center gap-3 mb-2">
            <Users className="w-6 h-6 text-blue-500" />
            <h3 className="text-lg font-semibold text-dark-text">Total Users</h3>
          </div>
          <p className="text-3xl font-bold text-dark-text">{users.length}</p>
        </div>

        <div className="bg-dark-surface border border-dark-border rounded-lg p-6">
          <div className="flex items-center gap-3 mb-2">
            <Shield className="w-6 h-6 text-orange-500" />
            <h3 className="text-lg font-semibold text-dark-text">Active Policies</h3>
          </div>
          <p className="text-3xl font-bold text-dark-text">{policies.length}</p>
        </div>

        <div className="bg-dark-surface border border-dark-border rounded-lg p-6">
          <div className="flex items-center gap-3 mb-2">
            <Settings className="w-6 h-6 text-green-500" />
            <h3 className="text-lg font-semibold text-dark-text">SSO Users</h3>
          </div>
          <p className="text-3xl font-bold text-dark-text">
            {users.filter(u => u.sso_provider).length}
          </p>
        </div>
      </div>

      {/* User Management */}
      <div className="bg-dark-surface border border-dark-border rounded-lg">
        <div className="p-6 border-b border-dark-border">
          <h2 className="text-xl font-semibold text-dark-text">User Management</h2>
          <p className="text-dark-textSecondary text-sm mt-1">Manage users and their policy assignments</p>
        </div>

        <div className="overflow-x-auto">
          <table className="w-full">
            <thead className="bg-dark-bg">
              <tr>
                <th className="px-6 py-3 text-left text-xs font-medium text-dark-textSecondary uppercase tracking-wider">
                  User
                </th>
                <th className="px-6 py-3 text-left text-xs font-medium text-dark-textSecondary uppercase tracking-wider">
                  Email
                </th>
                <th className="px-6 py-3 text-left text-xs font-medium text-dark-textSecondary uppercase tracking-wider">
                  Type
                </th>
                <th className="px-6 py-3 text-left text-xs font-medium text-dark-textSecondary uppercase tracking-wider">
                  Policies
                </th>
                <th className="px-6 py-3 text-left text-xs font-medium text-dark-textSecondary uppercase tracking-wider">
                  Actions
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-dark-border">
              {users.map((user) => (
                <tr key={user.id} className="hover:bg-dark-bg transition-colors">
                  <td className="px-6 py-4 whitespace-nowrap">
                    <div className="flex items-center gap-2">
                      <div>
                        <div className="text-sm font-medium text-dark-text flex items-center gap-2">
                          {user.username}
                          {user.is_locked && (
                            <Lock className="w-4 h-4 text-red-500" title="Account locked" />
                          )}
                        </div>
                        <div className="flex gap-1 mt-1">
                          {user.is_admin && (
                            <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-blue-500/10 text-blue-500">
                              Admin
                            </span>
                          )}
                          {user.is_locked && (
                            <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-red-500/10 text-red-500">
                              Locked
                            </span>
                          )}
                        </div>
                      </div>
                    </div>
                  </td>
                  <td className="px-6 py-4 whitespace-nowrap text-sm text-dark-textSecondary">
                    {user.email}
                  </td>
                  <td className="px-6 py-4 whitespace-nowrap">
                    {user.sso_provider ? (
                      <span className="inline-flex items-center px-2 py-1 rounded text-xs font-medium bg-green-500/10 text-green-500">
                        {user.sso_provider.toUpperCase()} SSO
                      </span>
                    ) : (
                      <span className="inline-flex items-center px-2 py-1 rounded text-xs font-medium bg-dark-bg text-dark-textSecondary">
                        Local
                      </span>
                    )}
                  </td>
                  <td className="px-6 py-4 whitespace-nowrap text-sm text-dark-textSecondary">
                    <button
                      onClick={() => handleManagePolicies(user)}
                      className="text-blue-500 hover:text-blue-400 font-medium"
                    >
                      {user.policies?.length || 0} policies
                    </button>
                  </td>
                  <td className="px-6 py-4 whitespace-nowrap text-sm">
                    <div className="flex items-center gap-2">
                      <button
                        onClick={() => handleManagePolicies(user)}
                        className="p-2 hover:bg-dark-bg rounded transition-colors text-blue-500"
                        title="Manage policies"
                      >
                        <Shield className="w-4 h-4" />
                      </button>
                      <button
                        onClick={() => handleManageAccessKeys(user)}
                        className="p-2 hover:bg-dark-bg rounded transition-colors text-purple-500"
                        title="Manage access keys"
                      >
                        <Key className="w-4 h-4" />
                      </button>
                      {!user.is_admin && (
                        <>
                          <button
                            onClick={() => handleToggleLock(user)}
                            className={`p-2 hover:bg-dark-bg rounded transition-colors ${
                              user.is_locked ? 'text-green-500' : 'text-orange-500'
                            }`}
                            title={user.is_locked ? 'Unlock user' : 'Lock user'}
                          >
                            {user.is_locked ? <Unlock className="w-4 h-4" /> : <Lock className="w-4 h-4" />}
                          </button>
                          <button
                            onClick={() => handleDeleteUser(user.id)}
                            className="p-2 hover:bg-dark-bg rounded transition-colors text-red-500"
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
      alert(err.response?.data?.message || 'Failed to update policy');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 overflow-y-auto bg-black bg-opacity-50 flex items-center justify-center p-4">
      <div className="bg-dark-surface rounded-lg max-w-2xl w-full max-h-[80vh] overflow-hidden flex flex-col">
        <div className="p-6 border-b border-dark-border flex justify-between items-start">
          <div>
            <h2 className="text-2xl font-bold text-dark-text">Manage Policies</h2>
            <p className="text-dark-textSecondary mt-1">
              User: <span className="font-medium text-dark-text">{user.username}</span>
            </p>
          </div>
          <button
            onClick={onClose}
            className="text-dark-textSecondary hover:text-dark-text transition-colors"
          >
            <X className="w-6 h-6" />
          </button>
        </div>

        <div className="flex-1 overflow-y-auto p-6">
          {policies.length === 0 ? (
            <div className="text-center py-12">
              <Shield className="w-16 h-16 text-dark-textSecondary mx-auto mb-4 opacity-50" />
              <p className="text-dark-textSecondary">No policies available</p>
            </div>
          ) : (
            <div className="space-y-2">
              {policies.map((policy) => {
                const isAttached = userPolicies.includes(policy.id);
                return (
                  <div
                    key={policy.id}
                    className="flex items-center justify-between p-4 bg-dark-bg rounded-lg hover:bg-dark-border transition-colors"
                  >
                    <div className="flex-1">
                      <h3 className="text-sm font-medium text-dark-text">{policy.name}</h3>
                      <p className="text-xs text-dark-textSecondary mt-1">{policy.description}</p>
                    </div>
                    <button
                      onClick={() => handleTogglePolicy(policy.id)}
                      disabled={loading}
                      className={`px-4 py-2 rounded-lg text-sm font-medium transition-colors disabled:opacity-50 ${
                        isAttached
                          ? 'bg-red-500/10 text-red-500 hover:bg-red-500/20'
                          : 'bg-blue-500/10 text-blue-500 hover:bg-blue-500/20'
                      }`}
                    >
                      {isAttached ? 'Detach' : 'Attach'}
                    </button>
                  </div>
                );
              })}
            </div>
          )}
        </div>

        <div className="p-6 border-t border-dark-border flex justify-end">
          <button
            onClick={onClose}
            className="px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded-lg transition-colors"
          >
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
      setError(err.response?.data?.message || 'Failed to create user');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 overflow-y-auto bg-black bg-opacity-50 flex items-center justify-center p-4">
      <div className="bg-dark-surface rounded-lg max-w-md w-full overflow-hidden flex flex-col">
        <div className="p-6 border-b border-dark-border flex justify-between items-start">
          <div>
            <h2 className="text-2xl font-bold text-dark-text">Create User</h2>
            <p className="text-dark-textSecondary mt-1">Add a new local user account</p>
          </div>
          <button
            onClick={onClose}
            className="text-dark-textSecondary hover:text-dark-text transition-colors"
          >
            <X className="w-6 h-6" />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="p-6 space-y-4">
          {error && (
            <div className="bg-red-500/10 border border-red-500 text-red-500 px-4 py-3 rounded-lg text-sm">
              {error}
            </div>
          )}

          <div>
            <label className="block text-sm font-medium text-dark-text mb-2">Username</label>
            <input
              type="text"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
              placeholder="john_doe"
              required
              minLength={3}
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-dark-text mb-2">Email</label>
            <input
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
              placeholder="john@example.com"
              required
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-dark-text mb-2">Password</label>
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className="w-full px-4 py-2 bg-dark-bg border border-dark-border rounded-lg text-dark-text focus:outline-none focus:ring-2 focus:ring-blue-500"
              placeholder="••••••••"
              required
              minLength={8}
            />
            <p className="text-xs text-dark-textSecondary mt-1">Minimum 8 characters</p>
          </div>

          <div className="flex items-center">
            <input
              type="checkbox"
              id="is_admin"
              checked={isAdmin}
              onChange={(e) => setIsAdmin(e.target.checked)}
              className="w-4 h-4 text-blue-600 bg-dark-bg border-dark-border rounded focus:ring-blue-500"
            />
            <label htmlFor="is_admin" className="ml-2 text-sm text-dark-text">
              Grant administrator privileges
            </label>
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

  useEffect(() => {
    loadAccessKeys();
  }, [user.id]);

  const loadAccessKeys = async () => {
    try {
      setLoading(true);
      const response = await api.get<AccessKey[]>(`/users/${user.id}/access-keys`);
      setAccessKeys(response.data);
    } catch (error) {
      console.error('Failed to load access keys:', error);
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
      alert(err.response?.data?.message || 'Failed to delete access key');
    }
  };

  const formatDate = (dateString: string) => {
    return new Date(dateString).toLocaleString();
  };

  return (
    <div className="fixed inset-0 z-50 overflow-y-auto bg-black bg-opacity-50 flex items-center justify-center p-4">
      <div className="bg-dark-surface rounded-lg max-w-4xl w-full max-h-[80vh] overflow-hidden flex flex-col">
        <div className="p-6 border-b border-dark-border flex justify-between items-start">
          <div>
            <h2 className="text-2xl font-bold text-dark-text">Access Keys</h2>
            <p className="text-dark-textSecondary mt-1">
              User: <span className="font-medium text-dark-text">{user.username}</span>
            </p>
          </div>
          <button
            onClick={onClose}
            className="text-dark-textSecondary hover:text-dark-text transition-colors"
          >
            <X className="w-6 h-6" />
          </button>
        </div>

        <div className="flex-1 overflow-y-auto p-6">
          {loading ? (
            <div className="text-center py-12">
              <Settings className="w-12 h-12 text-dark-textSecondary animate-spin mx-auto mb-4" />
              <p className="text-dark-textSecondary">Loading access keys...</p>
            </div>
          ) : accessKeys.length === 0 ? (
            <div className="text-center py-12">
              <Key className="w-16 h-16 text-dark-textSecondary mx-auto mb-4 opacity-50" />
              <p className="text-dark-textSecondary">No access keys found for this user</p>
            </div>
          ) : (
            <div className="space-y-3">
              {accessKeys.map((key) => (
                <div
                  key={key.id}
                  className="flex items-center justify-between p-4 bg-dark-bg rounded-lg border border-dark-border hover:border-dark-textSecondary transition-colors"
                >
                  <div className="flex-1">
                    <div className="flex items-center gap-2 mb-2">
                      <code className="text-sm font-mono text-dark-text bg-dark-surface px-2 py-1 rounded">
                        {key.access_key}
                      </code>
                      {!key.is_active && (
                        <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-red-500/10 text-red-500">
                          Inactive
                        </span>
                      )}
                      {key.is_active && (
                        <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-green-500/10 text-green-500">
                          Active
                        </span>
                      )}
                    </div>
                    <div className="flex gap-4 text-xs text-dark-textSecondary">
                      <span>Created: {formatDate(key.created_at)}</span>
                      {key.last_used_at && (
                        <span>Last used: {formatDate(key.last_used_at)}</span>
                      )}
                    </div>
                  </div>
                  <button
                    onClick={() => handleDeleteKey(key.id)}
                    className="p-2 hover:bg-dark-surface rounded transition-colors text-red-500"
                    title="Delete access key"
                  >
                    <Trash2 className="w-5 h-5" />
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="p-6 border-t border-dark-border flex justify-between items-center">
          <p className="text-sm text-dark-textSecondary">
            Total: {accessKeys.length} key{accessKeys.length !== 1 ? 's' : ''}
            {' '}({accessKeys.filter(k => k.is_active).length} active)
          </p>
          <button
            onClick={onClose}
            className="px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded-lg transition-colors"
          >
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
