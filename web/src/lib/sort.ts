import type { Entry } from './types.js';

export type SortKey = 'name' | 'size' | 'mtime';

// One collator for every comparison: "file2" before "file10", case ignored.
const byName = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

/**
 * A sorted copy of a listing: folders first, then by key, with the name
 * breaking ties. Dates are parsed once rather than in every comparison,
 * which matters in a folder of thousands.
 */
export function sortEntries(list: Entry[], key: SortKey, ascending: boolean): Entry[] {
  const dir = ascending ? 1 : -1;
  const time = new Map(key === 'mtime' ? list.map((e) => [e, Date.parse(e.mtime)]) : []);
  return [...list].sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
    let c = 0;
    if (key === 'size') c = a.size - b.size;
    else if (key === 'mtime') c = (time.get(a) ?? 0) - (time.get(b) ?? 0);
    return (c || byName.compare(a.name, b.name)) * dir;
  });
}
