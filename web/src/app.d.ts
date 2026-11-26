// See https://svelte.dev/docs/kit/types#app.d.ts
import type { Me } from '#lib/types.js';

declare global {
  namespace App {
    interface PageData {
      /** The logged-in user (null on public pages), from the root layout. */
      me: Me | null;
    }
    interface PageState {
      /** Files page: the path of the file whose preview is open (null: closed). */
      preview?: string | null;
    }
  }
}
export {};
