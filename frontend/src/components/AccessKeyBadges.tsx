import type { AccessKeyStatus } from '../types'

// Status + "Temporary" badges for an access key, shared by the Profile page
// and the admin view so both show the same thing for the same key.
export default function AccessKeyBadges({
  status,
  isActive,
  temporary,
  expiresAt,
}: {
  status?: AccessKeyStatus
  isActive: boolean
  temporary?: boolean
  expiresAt?: string
}) {
  const effective: AccessKeyStatus = status ?? (isActive ? 'active' : 'revoked')
  return (
    <span className="inline-flex items-center gap-1.5 flex-wrap">
      {effective === 'active' && <span className="badge-green">Active</span>}
      {effective === 'expired' && <span className="badge-yellow">Expired</span>}
      {effective === 'revoked' && <span className="badge-red">Revoked</span>}
      {temporary && (
        <span className="badge-blue" title={expiresAt ? `Expires ${new Date(expiresAt).toLocaleString()}` : undefined}>
          Temporary
        </span>
      )}
    </span>
  )
}
