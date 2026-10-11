import { errorMessage, get } from '#lib/api.js';
import type { TrashItem } from '#lib/types.js';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch, depends }) => {
  depends('app:trash');
  try {
    const r = await get<{ items: TrashItem[] | null; retention_days: number }>('/api/trash', {
      fetch,
      // The root layout handles authentication; never navigate from inside a load.
      redirectOnAuth: false,
    });
    return { items: r.items ?? [], retention: r.retention_days, loadError: '' };
  } catch (e) {
    return { items: [] as TrashItem[], retention: 0, loadError: errorMessage(e) };
  }
};
