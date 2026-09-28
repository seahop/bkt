import { useCallback, useState } from 'react'
import { FileBraces, Pencil, Trash2 } from 'lucide-react'
import { bucketApi } from '../services/api'
import { getErrorMessage, getErrorStatus } from '../utils/errors'
import { useAsyncLoad } from '../utils/useAsyncLoad'

// Bucket policy section of the bucket settings modal. Everyone allowed to
// read the policy (admin or s3:GetBucketPolicy) sees it pretty-printed;
// admins can edit, replace from a template, or delete it.

interface Template {
  label: string
  build: (bucket: string) => object
}

const TEMPLATES: Template[] = [
  {
    label: 'Deny everyone access to secret/*',
    build: (b) => ({
      Version: '2012-10-17',
      Statement: [
        {
          Sid: 'DenySecretPrefix',
          Effect: 'Deny',
          Principal: '*',
          Action: ['s3:GetObject', 's3:PutObject', 's3:DeleteObject'],
          Resource: [`arn:aws:s3:::${b}/secret/*`],
        },
      ],
    }),
  },
  {
    label: 'Allow a user read-only',
    build: (b) => ({
      Version: '2012-10-17',
      Statement: [
        {
          Sid: 'ReadOnlyForUser',
          Effect: 'Allow',
          Principal: ['USERNAME'],
          Action: ['s3:ListBucket', 's3:GetObject'],
          Resource: [`arn:aws:s3:::${b}`, `arn:aws:s3:::${b}/*`],
        },
      ],
    }),
  },
  {
    label: 'Allow a user to upload to uploads/*',
    build: (b) => ({
      Version: '2012-10-17',
      Statement: [
        {
          Sid: 'UploadsForUser',
          Effect: 'Allow',
          Principal: ['USERNAME'],
          Action: ['s3:PutObject'],
          Resource: [`arn:aws:s3:::${b}/uploads/*`],
        },
      ],
    }),
  },
]

const pretty = (doc: string): string => {
  try {
    return JSON.stringify(JSON.parse(doc), null, 2)
  } catch {
    return doc
  }
}

export default function BucketPolicyPanel({
  bucketName,
  isAdmin,
  canRead = true,
}: {
  bucketName: string
  isAdmin: boolean
  // false when GET /api/buckets/:name reported no s3:GetBucketPolicy: skip the
  // request that would only answer 403.
  canRead?: boolean
}) {
  const [loading, setLoading] = useState(canRead)
  const [policy, setPolicy] = useState<string | null>(null)
  const [forbidden, setForbidden] = useState(!canRead)
  const [loadError, setLoadError] = useState('')

  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [editError, setEditError] = useState('')
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [notice, setNotice] = useState('')

  const load = useCallback(async () => {
    try {
      setPolicy(await bucketApi.getBucketPolicy(bucketName))
      setForbidden(false)
      setLoadError('')
    } catch (error) {
      if (getErrorStatus(error) === 403) {
        setForbidden(true)
      } else {
        setLoadError(getErrorMessage(error, 'Failed to load the bucket policy'))
      }
    } finally {
      setLoading(false)
    }
  }, [bucketName])
  useAsyncLoad(load, canRead)

  const startEditing = () => {
    setDraft(policy ? pretty(policy) : '')
    setEditError('')
    setNotice('')
    setEditing(true)
  }

  const applyTemplate = (t: Template) => {
    setDraft(JSON.stringify(t.build(bucketName), null, 2))
    setEditError('')
  }

  const handleSave = async () => {
    setEditError('')
    setNotice('')
    try {
      JSON.parse(draft)
    } catch (e) {
      setEditError(`Not valid JSON: ${e instanceof Error ? e.message : String(e)}`)
      return
    }
    setSaving(true)
    try {
      await bucketApi.setBucketPolicy(bucketName, draft)
      setEditing(false)
      setNotice('Bucket policy saved')
      await load()
    } catch (error) {
      // The server's validator explains what is wrong (unsupported
      // Condition/Principal forms, bad actions, ...): show it inline.
      setEditError(getErrorMessage(error, 'Failed to save the bucket policy'))
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async () => {
    if (!confirm(`Delete the bucket policy of "${bucketName}"? Access then follows user and group policies only.`)) return
    setEditError('')
    setNotice('')
    setDeleting(true)
    try {
      await bucketApi.deleteBucketPolicy(bucketName)
      setEditing(false)
      setNotice('Bucket policy deleted')
      await load()
    } catch (error) {
      setLoadError(getErrorMessage(error, 'Failed to delete the bucket policy'))
    } finally {
      setDeleting(false)
    }
  }

  return (
    <div>
      <div className="flex items-center justify-between gap-4 mb-2">
        <h3 className="text-base font-semibold text-dark-text">Bucket policy</h3>
        {isAdmin && !loading && !forbidden && !editing && (
          <div className="flex gap-2">
            <button type="button" onClick={startEditing} className="btn-secondary btn-sm">
              <Pencil className="w-3.5 h-3.5" />
              {policy ? 'Edit' : 'Add policy'}
            </button>
            {policy && (
              <button
                type="button"
                onClick={() => void handleDelete()}
                disabled={deleting}
                className="btn-danger-ghost btn-sm"
              >
                {deleting ? <span className="spinner w-3.5! h-3.5!" /> : <Trash2 className="w-3.5 h-3.5" />}
                Delete policy
              </button>
            )}
          </div>
        )}
      </div>

      <p className="help-text mb-3">
        Evaluated together with user and group policies; an explicit Deny always wins. Principal is a list
        of bkt usernames or <code className="font-mono">"*"</code> (every signed-in bkt user); the AWS form{' '}
        <code className="font-mono">{'{"AWS": [...]}'}</code> is accepted too. Anonymous downloads are
        controlled by the Public read access toggle above, not by the policy (a Deny for{' '}
        <code className="font-mono">"*"</code> still applies to them).
      </p>

      {notice && <div className="alert-success mb-3">{notice}</div>}
      {loadError && <div className="alert-error mb-3">{loadError}</div>}

      {loading ? (
        <div className="flex items-center justify-center py-4">
          <div className="spinner" />
        </div>
      ) : forbidden ? (
        <p className="text-sm text-dark-textSecondary">
          You don't have permission to view this bucket's policy (requires s3:GetBucketPolicy).
        </p>
      ) : editing ? (
        <div className="space-y-3">
          <div>
            <span className="label">Start from a template</span>
            <div className="flex flex-wrap gap-2">
              {TEMPLATES.map((t) => (
                <button key={t.label} type="button" onClick={() => applyTemplate(t)} className="btn-secondary btn-sm">
                  {t.label}
                </button>
              ))}
            </div>
            <p className="help-text">Templates replace the editor contents; replace USERNAME with a bkt username.</p>
          </div>
          <textarea
            aria-label="Bucket policy JSON"
            value={draft}
            onChange={(e) => {
              setDraft(e.target.value)
              setEditError('')
            }}
            spellCheck={false}
            placeholder={'{\n  "Version": "2012-10-17",\n  "Statement": [ ... ]\n}'}
            className={`input font-mono text-xs min-h-[240px] ${
              editError ? 'border-red-500/60! focus:ring-red-500/50!' : ''
            }`}
          />
          {editError && <div className="alert-error">{editError}</div>}
          <div className="flex justify-end gap-2">
            <button type="button" onClick={() => setEditing(false)} disabled={saving} className="btn-ghost">
              Cancel
            </button>
            <button type="button" onClick={() => void handleSave()} disabled={saving || !draft.trim()} className="btn-primary">
              {saving && <span className="spinner w-4! h-4!" />}
              {saving ? 'Saving...' : 'Save policy'}
            </button>
          </div>
        </div>
      ) : policy ? (
        <pre className="bg-dark-inset border border-dark-border rounded-lg p-3 text-xs text-dark-text font-mono overflow-x-auto max-h-80">
          {pretty(policy)}
        </pre>
      ) : (
        <div className="flex items-center gap-2 text-sm text-dark-textSecondary">
          <FileBraces className="w-4 h-4" />
          No bucket policy
        </div>
      )}
    </div>
  )
}
