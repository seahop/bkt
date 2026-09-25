// Helpers for showing object bytes in the browser.
//
// An object's Content-Type is chosen by whoever uploaded it. A blob: URL made
// from it inherits the console's origin, so rendering e.g. text/html or
// image/svg+xml inline would execute attacker-controlled script with access to
// the session token. Only media types that browsers render passively (no
// script) may be shown inline; everything else must be downloaded.

const INLINE_EXACT = new Set([
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
  'image/avif',
  'image/bmp',
  'application/pdf',
  'text/plain',
])

/**
 * Returns the media type to use for an inline preview of an object, or null
 * when the object must not be rendered inline. Parameters (charset etc.) are
 * dropped; text is always shown as UTF-8 plain text. Never returns an active
 * type (HTML, SVG, XML, JavaScript, ...).
 */
export function safeInlineType(contentType: string | undefined | null): string | null {
  const mediaType = (contentType || '').split(';')[0].trim().toLowerCase()
  if (!mediaType) return null
  if (mediaType === 'text/plain') return 'text/plain; charset=utf-8'
  if (INLINE_EXACT.has(mediaType)) return mediaType
  if (/^(video|audio)\/[a-z0-9.+-]+$/.test(mediaType)) return mediaType
  return null
}

/** Last path segment of an object key, for use as a download filename. */
export function downloadName(key: string): string {
  const parts = key.split('/').filter(Boolean)
  return parts[parts.length - 1] || 'download'
}

/**
 * Saves a blob as a file download. The blob is re-typed as
 * application/octet-stream so the browser never renders it, whatever the
 * stored Content-Type claims.
 */
export function saveBlob(blob: Blob, key: string): void {
  const url = window.URL.createObjectURL(new Blob([blob], { type: 'application/octet-stream' }))
  const link = document.createElement('a')
  link.href = url
  link.download = downloadName(key)
  link.rel = 'noopener'
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  // Revoke after the download has had a chance to start.
  window.setTimeout(() => window.URL.revokeObjectURL(url), 10_000)
}

/**
 * "Open in new tab" loads the whole object into memory (as a Blob) before it
 * can be shown, so larger objects are downloaded instead.
 */
export const INLINE_OPEN_MAX_BYTES = 500 * 1024 * 1024

/** Upper bound on how long an opened tab's blob: URL is kept alive. */
export const INLINE_BLOB_URL_MAX_LIFETIME_MS = 30 * 60 * 1000
const INLINE_TAB_POLL_MS = 3_000

/**
 * Opens an empty tab synchronously. Call this directly from the click handler
 * — before any await — so popup blockers treat it as user-initiated; fill it
 * later with showBlobInTab, or close it on failure. Returns null when the
 * browser blocked the popup.
 */
export function openPendingTab(): Window | null {
  // 'noopener' would make window.open return null, and we need the handle to
  // navigate the tab once the object has downloaded; cut the back-reference
  // by hand instead.
  const tab = window.open('', '_blank')
  if (!tab) return null
  try {
    tab.opener = null
    tab.document.title = 'Loading…'
    tab.document.body.textContent = 'Loading…'
  } catch {
    // Cosmetic only.
  }
  return tab
}

/**
 * Navigates a tab from openPendingTab to a blob: URL of `blob`, re-typed as
 * `inlineType` (which must come from safeInlineType). The URL stays valid
 * while the tab is open — so the viewer can seek in media or reload — and is
 * revoked once the tab is closed or after INLINE_BLOB_URL_MAX_LIFETIME_MS.
 */
export function showBlobInTab(tab: Window, blob: Blob, inlineType: string): void {
  const url = window.URL.createObjectURL(new Blob([blob], { type: inlineType }))
  try {
    tab.location.href = url
  } catch (err) {
    window.URL.revokeObjectURL(url)
    throw err
  }
  const openedAt = Date.now()
  const timer = window.setInterval(() => {
    let closed = true
    try {
      closed = tab.closed
    } catch {
      // Treat an inaccessible handle as closed.
    }
    if (closed || Date.now() - openedAt > INLINE_BLOB_URL_MAX_LIFETIME_MS) {
      window.clearInterval(timer)
      window.URL.revokeObjectURL(url)
    }
  }, INLINE_TAB_POLL_MS)
}

/** Closes a pending tab, ignoring errors (it may already be gone). */
export function closeTab(tab: Window | null): void {
  try {
    tab?.close()
  } catch {
    // ignore
  }
}
