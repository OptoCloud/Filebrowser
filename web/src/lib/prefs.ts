/**
 * Small things remembered in this browser (the view, the sort order). Storage
 * may be unavailable (private windows, blocked cookies): then nothing is
 * remembered and nothing breaks.
 */
export const PREF = {
  view: 'fb.view',
  sortKey: 'fb.sort',
  sortAsc: 'fb.asc',
  /** The name last given on a drop link that asks for one. */
  uploader: 'fb.uploader',
} as const;

export function loadPref(key: string, fallback: string): string {
  try {
    return localStorage.getItem(key) ?? fallback;
  } catch {
    return fallback;
  }
}

export function savePref(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* storage unavailable */
  }
}
