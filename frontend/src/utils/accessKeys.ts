import type { AccessKeyStatus } from '../types'

// Whether a key works right now. Uses the server-computed status when present
// (it accounts for expiry and revoked STS sessions), else falls back to is_active.
export function keyIsActive(k: { status?: AccessKeyStatus; is_active: boolean }): boolean {
  return (k.status ?? (k.is_active ? 'active' : 'revoked')) === 'active'
}
