import { get } from '#lib/api.js';
import type { Provider } from '#lib/types.js';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch }) => {
  // Single sign-on buttons are optional: without the config, password login still works.
  let providers: Provider[] = [];
  try {
    const c = await get<{ oidc: Provider[] | null }>('/api/auth/config', {
      fetch,
      redirectOnAuth: false,
    });
    providers = c.oidc ?? [];
  } catch {
    /* no SSO buttons */
  }
  return { providers };
};
