import { errorMessage, get } from '#lib/api.js';
import type { Session } from '#lib/types.js';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch, depends }) => {
  depends('app:sessions');
  try {
    const sessions =
      (await get<Session[] | null>('/api/me/sessions', {
        fetch,
        // The root layout handles authentication; never navigate from inside a load.
        redirectOnAuth: false,
      })) ?? [];
    return { sessions, sessionsError: '' };
  } catch (e) {
    // The rest of the page still works; show the error in the sessions card.
    return { sessions: [] as Session[], sessionsError: errorMessage(e) };
  }
};
