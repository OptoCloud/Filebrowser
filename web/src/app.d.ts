// See https://svelte.dev/docs/kit/types#app.d.ts
import type { Me } from '#lib/types.js';

declare global {
  namespace App {
    interface PageData {
      /** The logged-in user (null on public pages), from the root layout. */
      me: Me | null;
    }
  }
}
export {};
