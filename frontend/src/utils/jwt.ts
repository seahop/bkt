/**
 * Minimal, NON-verifying JWT payload reader. Used only for client-side timing
 * decisions (when to refresh an access token); the server remains the sole
 * authority on whether a token is valid.
 */
interface JwtTimes {
  /** Expiry, in epoch milliseconds. */
  expiresAt: number
  /** Issued-at, in epoch milliseconds (undefined if the claim is absent). */
  issuedAt?: number
}

export function readJwtTimes(token: string | null | undefined): JwtTimes | null {
  if (!token) return null
  const parts = token.split('.')
  if (parts.length !== 3) return null
  try {
    const b64 = parts[1].replace(/-/g, '+').replace(/_/g, '/')
    const padded = b64 + '='.repeat((4 - (b64.length % 4)) % 4)
    const payload: unknown = JSON.parse(atob(padded))
    if (typeof payload !== 'object' || payload === null) return null
    const { exp, iat } = payload as { exp?: unknown; iat?: unknown }
    if (typeof exp !== 'number' || !Number.isFinite(exp)) return null
    return {
      expiresAt: exp * 1000,
      issuedAt: typeof iat === 'number' && Number.isFinite(iat) ? iat * 1000 : undefined,
    }
  } catch {
    return null
  }
}

/** True if the token's exp has passed (or passes within `skewMs`). Unknown exp → false. */
export function isJwtExpired(token: string | null | undefined, skewMs = 0): boolean {
  const times = readJwtTimes(token)
  return times !== null && times.expiresAt - skewMs <= Date.now()
}
