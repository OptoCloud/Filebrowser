import { goto } from '$app/navigation';

export class ApiError extends Error {
  status: number;
  code?: string;
  retryAfter?: number;
  constructor(status: number, message: string, code?: string, retryAfter?: number) {
    super(message);
    this.status = status;
    this.code = code;
    this.retryAfter = retryAfter;
  }
}

function onShareOrigin(): boolean {
  return location.pathname.startsWith('/s/');
}

function formatWait(s: number): string {
  if (s < 90) return `${s} seconds`;
  return `${Math.ceil(s / 60)} minutes`;
}

/** Converts a failed response into an ApiError with a friendly message. */
export async function toError(res: Response): Promise<ApiError> {
  let message = res.statusText || `HTTP ${res.status}`;
  let code: string | undefined;
  try {
    const body = await res.json();
    if (body && typeof body.error === 'string') message = body.error;
    if (body && typeof body.code === 'string') code = body.code;
  } catch {
    /* not JSON */
  }
  let retryAfter: number | undefined;
  if (res.status === 429) {
    retryAfter = Number(res.headers.get('Retry-After')) || undefined;
    message = retryAfter
      ? `Too many attempts. Please wait ${formatWait(retryAfter)} and try again.`
      : 'Too many attempts. Please wait a moment and try again.';
  }
  return new ApiError(res.status, message, code, retryAfter);
}

/** Redirects for authentication problems on the app origin. Returns true if redirected. */
export function handleAuthError(err: unknown): boolean {
  if (!(err instanceof ApiError) || onShareOrigin()) return false;
  const here = location.pathname + location.search;
  if (err.status === 401 && err.code === 'unauthenticated') {
    if (!location.pathname.startsWith('/login')) {
      goto('/login?next=' + encodeURIComponent(here));
    }
    return true;
  }
  if (err.status === 403 && err.code === 'totp_enrollment_required') {
    if (location.pathname !== '/settings/2fa') goto('/settings/2fa');
    return true;
  }
  return false;
}

export interface RequestOptions {
  /** Redirect to the login / 2FA page on authentication errors (default true). */
  redirectOnAuth?: boolean;
  /** Aborts the request (see LatestRequest). */
  signal?: AbortSignal;
  /** The fetch to use, e.g. the one SvelteKit passes to load functions. */
  fetch?: typeof fetch;
}

export async function request<T = unknown>(
  method: string,
  url: string,
  body?: unknown,
  opts: RequestOptions = {}
): Promise<T> {
  const headers: Record<string, string> = {};
  const init: RequestInit = { method, credentials: 'same-origin', headers, signal: opts.signal };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
    headers['Content-Type'] = 'application/json';
  }
  const res = await (opts.fetch ?? fetch)(url, init);
  if (!res.ok) {
    const err = await toError(res);
    if (opts.redirectOnAuth !== false) handleAuthError(err);
    throw err;
  }
  const ct = res.headers.get('Content-Type') || '';
  if (res.status === 204 || !ct.includes('application/json')) return undefined as T;
  return (await res.json()) as T;
}

export const get = <T>(url: string, opts?: RequestOptions) =>
  request<T>('GET', url, undefined, opts);
export const post = <T>(url: string, body?: unknown, opts?: RequestOptions) =>
  request<T>('POST', url, body ?? {}, opts);
export const put = <T>(url: string, body: unknown) => request<T>('PUT', url, body);
export const patch = <T>(url: string, body: unknown) => request<T>('PATCH', url, body);
export const del = <T>(url: string) => request<T>('DELETE', url);

/** Ends the session (best effort) and goes to the login page with fresh data. */
export async function logout(endpoint = '/api/auth/logout') {
  try {
    await post(endpoint);
  } catch {
    /* the session may already be gone */
  }
  await goto('/login', { invalidateAll: true });
}

export function qs(
  params: Record<string, string | number | boolean | undefined | null | string[]>
): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === false) continue;
    if (Array.isArray(v)) v.forEach((x) => sp.append(k, x));
    else sp.set(k, v === true ? '1' : String(v));
  }
  const s = sp.toString();
  return s ? '?' + s : '';
}

export function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message;
  return String(err);
}

/** Submits a hidden form POST (used for zip downloads of many paths). */
export function postForm(action: string, fields: Record<string, string[]>) {
  const form = document.createElement('form');
  form.method = 'POST';
  form.action = action;
  form.style.display = 'none';
  for (const [name, values] of Object.entries(fields)) {
    for (const v of values) {
      const input = document.createElement('input');
      input.type = 'hidden';
      input.name = name;
      input.value = v;
      form.appendChild(input);
    }
  }
  document.body.appendChild(form);
  form.submit();
  form.remove();
}

/** Triggers a download of url without leaving the page. */
export function download(url: string) {
  const a = document.createElement('a');
  a.href = url;
  a.download = '';
  a.rel = 'noopener';
  document.body.appendChild(a);
  a.click();
  a.remove();
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
