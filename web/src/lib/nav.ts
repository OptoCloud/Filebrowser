/**
 * Sanitizes a post-login redirect target. Only same-origin URLs are accepted
 * (this rejects "//evil.example", "/\evil.example", "javascript:" and the like);
 * the result is always a local path.
 */
export function safeNext(
  next: string | null | undefined,
  origin: string,
  fallback = '/files'
): string {
  if (!next) return fallback;
  let u: URL;
  try {
    u = new URL(next, origin);
  } catch {
    return fallback;
  }
  if (u.origin !== origin) return fallback;
  return u.pathname + u.search + u.hash;
}
