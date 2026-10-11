import { errorMessage, get } from '#lib/api.js';
import type { WebAuthnKey } from '#lib/types.js';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch, depends }) => {
  depends('app:webauthn');
  try {
    const keys =
      (await get<WebAuthnKey[] | null>('/api/me/webauthn', {
        fetch,
        // The root layout handles authentication; never navigate from inside a load.
        redirectOnAuth: false,
      })) ?? [];
    return { keys, keysError: '' };
  } catch (e) {
    // The authenticator app setup still works; show the error in the keys card.
    return { keys: [] as WebAuthnKey[], keysError: errorMessage(e) };
  }
};
