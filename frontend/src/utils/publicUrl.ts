/** Tooltip for the "Public" bucket badge. */
export const PUBLIC_BADGE_TITLE = 'Public read: objects downloadable without sign-in'

/**
 * Builds the unsigned (public-read) URL of an object: the server-provided
 * base (`<S3 endpoint>/<bucket>`) plus the key with each segment
 * percent-encoded ("/" kept as the separator).
 *
 * Returns null when the key cannot be expressed as a URL path: browsers and
 * HTTP clients resolve "." and ".." segments before sending the request, so
 * such a key would address a different object.
 */
export function publicObjectUrl(base: string, key: string): string | null {
  const segments = key.split('/')
  if (segments.some((s) => s === '.' || s === '..')) return null
  return `${base.replace(/\/+$/, '')}/${segments.map(encodeURIComponent).join('/')}`
}
