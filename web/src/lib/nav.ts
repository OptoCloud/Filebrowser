import { resolve } from '$app/paths';
import type { ResolvedPathname } from '$app/types';

/**
 * Sanitizes a post-login redirect target. Only same-origin URLs are accepted
 * (this rejects "//evil.example", "/\evil.example", "javascript:" and the like);
 * the result is always a local path.
 */
export function safeNext(
  next: string | null | undefined,
  origin: string,
  fallback: ResolvedPathname = filesHref('')
): ResolvedPathname {
  if (!next) return fallback;
  let u: URL;
  try {
    u = new URL(next, origin);
  } catch {
    return fallback;
  }
  if (u.origin !== origin) return fallback;
  // Same origin and parsed by URL, so this is a local path (any route, or a 404).
  return (u.pathname + u.search + u.hash) as ResolvedPathname;
}

/** The /files page for a slash-separated path ('' is the root); each segment is encoded. */
export function filesHref(path: string): ResolvedPathname {
  return resolve('/files/[...path]', { path: path.split('/').map(encodeURIComponent).join('/') });
}

/** The share page for a token, optionally at a subfolder of the share. */
export function shareHref(token: string, path = ''): ResolvedPathname {
  const href = resolve('/s/[token]', { token: encodeURIComponent(token) });
  return path ? (`${href}?${new URLSearchParams({ path })}` as ResolvedPathname) : href;
}
