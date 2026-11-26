import { errorMessage, get, qs } from '#lib/api.js';
import type { Listing } from '#lib/types.js';
import type { PageLoad } from './$types';

// The listing is loaded before the page is shown, so a folder never appears
// under another folder's rows, and going back restores the scroll position
// against the rows that were there. `?preview=` is not read here: opening a
// preview does not reload the listing.
export const load: PageLoad = async ({ params, fetch, depends }) => {
  depends('app:files');
  const path = params.path ?? '';
  try {
    const listing = await get<Listing>('/api/files' + qs({ path }), {
      fetch,
      // The root layout handles authentication; never navigate from inside a load.
      redirectOnAuth: false,
    });
    return { path, listing: listing as Listing | null, loadError: '' };
  } catch (e) {
    return { path, listing: null as Listing | null, loadError: errorMessage(e) };
  }
};
