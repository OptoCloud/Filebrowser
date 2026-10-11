import type { Me } from './types';

/** The logged-in user, set by the root layout. */
export const session = $state<{ me: Me | null }>({ me: null });
