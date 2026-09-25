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
