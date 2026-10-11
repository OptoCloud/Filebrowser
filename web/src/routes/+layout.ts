import type { Me } from '#lib/types.js';
import { error, redirect } from '@sveltejs/kit';
import type { LayoutLoad } from './$types';

// A pure client-side SPA embedded in the Go binary.
export const ssr = false;
export const prerender = false;

function isPublic(p: string): boolean {
  return p.startsWith('/s/') || p === '/login' || p.startsWith('/invite/');
}

export const load: LayoutLoad = async ({ url, fetch, depends }) => {
  depends('app:me');
  const p = url.pathname;
  if (isPublic(p)) return { me: null as Me | null };
  const res = await fetch('/api/me', { credentials: 'same-origin' });
  if (res.status === 401) redirect(307, '/login?next=' + encodeURIComponent(p + url.search));
  if (!res.ok)
    error(
      res.status,
      res.status === 429 ? 'Too many requests, try again shortly.' : 'Could not load your account.'
    );
  const me = (await res.json()) as Me;
  if (me.restricted && p !== '/settings/2fa') redirect(307, '/settings/2fa');
  return { me: me as Me | null };
};
