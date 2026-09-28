// Linked SSO (identity-provider) group names: same limits and
// case-insensitive de-duplication as the server (PUT /api/groups/:id/sso-groups).
export const SSO_GROUP_MAX_LEN = 256;
export const SSO_GROUPS_MAX = 100;

// addSSOGroupNames appends the comma/newline separated names in raw to
// current, trimming them and skipping case-insensitive duplicates.
export function addSSOGroupNames(current: string[], raw: string): string[] {
  const next = [...current];
  const seen = new Set(current.map((n) => n.toLowerCase()));
  for (const part of raw.split(/[,\n]/)) {
    const name = part.trim().slice(0, SSO_GROUP_MAX_LEN);
    if (!name || seen.has(name.toLowerCase())) continue;
    seen.add(name.toLowerCase());
    next.push(name);
  }
  return next.slice(0, SSO_GROUPS_MAX);
}

// sameSSOGroups compares two lists ignoring order and case.
export function sameSSOGroups(a: string[], b: string[]): boolean {
  const norm = (l: string[]) => l.map((n) => n.toLowerCase()).sort().join('\n');
  return a.length === b.length && norm(a) === norm(b);
}
