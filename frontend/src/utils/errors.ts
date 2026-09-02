/**
 * Extract a human-readable message from an unknown error.
 *
 * The backend returns errors as `{ "error": "...", "message": "..." }` where
 * `message` is often absent. Previously the frontend only read `data.message`
 * and discarded the real reason in `data.error`. This helper prefers `message`,
 * falls back to `error`, then to a generic Error's `.message`, and finally the
 * provided fallback string.
 */
export function getErrorMessage(err: unknown, fallback: string): string {
  const e = err as {
    response?: { data?: { message?: string; error?: string } }
    message?: string
  }
  return (
    e?.response?.data?.message ??
    e?.response?.data?.error ??
    e?.message ??
    fallback
  )
}
