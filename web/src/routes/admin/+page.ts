import { redirect } from '@sveltejs/kit';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ parent }) => {
  const { me } = await parent();
  if (me?.user.role !== 'admin') redirect(307, '/files');
};
