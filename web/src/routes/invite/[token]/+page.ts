import { errorMessage, get, qs } from '#lib/api.js';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ params, fetch }) => {
  try {
    const info = await get<{ role: string; expires_at: string }>(
      '/api/auth/invite' + qs({ token: params.token }),
      { fetch, redirectOnAuth: false }
    );
    return { info, loadError: '' };
  } catch (e) {
    return { info: null, loadError: errorMessage(e) };
  }
};
