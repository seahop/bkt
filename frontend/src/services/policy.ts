import api from './api';

export interface Policy {
  id: string;
  name: string;
  description: string;
  document: string;
  created_at: string;
  updated_at: string;
}

export interface CreatePolicyRequest {
  name: string;
  description: string;
  document: string;
}

export interface UpdatePolicyRequest {
  name?: string;
  description?: string;
  document?: string;
}

/**
 * List all policies (admin sees all, users see their own)
 */
export const listPolicies = async (): Promise<Policy[]> => {
  const response = await api.get<Policy[]>('/policies');
  return response.data;
};

/**
 * Get a specific policy by ID
 */
export const getPolicy = async (id: string): Promise<Policy> => {
  const response = await api.get<Policy>(`/policies/${id}`);
  return response.data;
};

/**
 * Create a new policy (admin only)
 */
export const createPolicy = async (policy: CreatePolicyRequest): Promise<Policy> => {
  const response = await api.post<Policy>('/policies', policy);
  return response.data;
};

/**
 * Update an existing policy (admin only)
 */
export const updatePolicy = async (id: string, policy: UpdatePolicyRequest): Promise<Policy> => {
  const response = await api.put<Policy>(`/policies/${id}`, policy);
  return response.data;
};

/**
 * Delete a policy (admin only)
 */
export const deletePolicy = async (id: string): Promise<void> => {
  await api.delete(`/policies/${id}`);
};

/**
 * Attach a policy to a user (admin only)
 */
export const attachPolicyToUser = async (userId: string, policyId: string): Promise<void> => {
  await api.post(`/policies/users/${userId}/attach`, { policy_id: policyId });
};

/**
 * Detach a policy from a user (admin only)
 */
export const detachPolicyFromUser = async (userId: string, policyId: string): Promise<void> => {
  await api.delete(`/policies/users/${userId}/detach/${policyId}`);
};

/**
 * Get default policy templates
 */
export const getPolicyTemplates = () => {
  return {
    readOnly: {
      name: 'Read Only Access',
      description: 'Allows read-only access to all buckets',
      document: JSON.stringify({
        Version: '2012-10-17',
        Statement: [
          {
            Effect: 'Allow',
            Action: ['s3:GetObject', 's3:ListBucket'],
            Resource: ['arn:aws:s3:::*', 'arn:aws:s3:::*/*']
          }
        ]
      }, null, 2)
    },
    fullAccess: {
      name: 'Full Access',
      description: 'Allows full access to all buckets',
      document: JSON.stringify({
        Version: '2012-10-17',
        Statement: [
          {
            Effect: 'Allow',
            Action: ['s3:*'],
            Resource: ['arn:aws:s3:::*', 'arn:aws:s3:::*/*']
          }
        ]
      }, null, 2)
    },
    specificBucket: (bucketName: string) => ({
      name: `${bucketName} Access`,
      description: `Full access to ${bucketName} bucket`,
      document: JSON.stringify({
        Version: '2012-10-17',
        Statement: [
          {
            Effect: 'Allow',
            Action: ['s3:*'],
            Resource: [`arn:aws:s3:::${bucketName}`, `arn:aws:s3:::${bucketName}/*`]
          }
        ]
      }, null, 2)
    }),
    denyAll: {
      name: 'Deny All',
      description: 'Explicitly denies all access',
      document: JSON.stringify({
        Version: '2012-10-17',
        Statement: [
          {
            Effect: 'Deny',
            Action: ['s3:*'],
            Resource: ['arn:aws:s3:::*', 'arn:aws:s3:::*/*']
          }
        ]
      }, null, 2)
    }
  };
};
