/**
 * Run `fn` while holding a lock shared by every tab of this origin.
 *
 * Used to serialise refresh-token rotation: the backend rotates the refresh
 * token on every use and treats a replay of an already-rotated token as theft
 * (it revokes every session of the user). Two tabs refreshing with the same
 * refresh token at once would therefore log the user out everywhere.
 *
 * Prefers the Web Locks API (atomic, released automatically if the tab dies).
 * It is only exposed in secure contexts, so plain-http deployments fall back
 * to a best-effort localStorage lease.
 */
export async function withCrossTabLock<T>(name: string, fn: () => Promise<T>): Promise<T> {
  const locks = typeof navigator !== 'undefined' ? navigator.locks : undefined
  if (locks && typeof locks.request === 'function') {
    return locks.request(name, { mode: 'exclusive' }, () => fn())
  }
  return withLeaseLock(name, fn)
}

const LEASE_MS = 10_000
const POLL_MS = 100

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))

function readLease(key: string): { owner: string; expiresAt: number } | null {
  try {
    const raw = localStorage.getItem(key)
    if (!raw) return null
    const sep = raw.lastIndexOf(':')
    const expiresAt = Number(raw.slice(sep + 1))
    return sep > 0 && Number.isFinite(expiresAt) ? { owner: raw.slice(0, sep), expiresAt } : null
  } catch {
    return null
  }
}

async function withLeaseLock<T>(name: string, fn: () => Promise<T>): Promise<T> {
  const key = `bkt-lock:${name}`
  const owner = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  const giveUpAt = Date.now() + LEASE_MS
  let held = false

  try {
    while (!held && Date.now() < giveUpAt) {
      const lease = readLease(key)
      if (!lease || lease.expiresAt < Date.now()) {
        localStorage.setItem(key, `${owner}:${Date.now() + LEASE_MS}`)
        // Give a racing tab's write time to land, then check who won.
        await sleep(30)
        held = readLease(key)?.owner === owner
        if (held) break
      }
      await sleep(POLL_MS)
    }
  } catch {
    // Storage unavailable: proceed unlocked (no worse than before).
  }

  try {
    return await fn()
  } finally {
    if (held) {
      try {
        if (readLease(key)?.owner === owner) localStorage.removeItem(key)
      } catch {
        // ignore
      }
    }
  }
}
