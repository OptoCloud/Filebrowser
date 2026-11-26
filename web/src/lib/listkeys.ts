/** Keyboard movement through a list or grid of entries. */

/** Whether the key press belongs to a field the user is typing in. */
export function isTyping(target: HTMLElement): boolean {
  return (
    target.isContentEditable ||
    target.tagName === 'TEXTAREA' ||
    target.tagName === 'SELECT' ||
    (target.tagName === 'INPUT' && (target as HTMLInputElement).type !== 'checkbox')
  );
}

/** How many of the elements share the first one's row (1 in a list). */
export function perRow(els: HTMLElement[]): number {
  if (!els.length) return 1;
  const top = els[0].getBoundingClientRect().top;
  const n = els.findIndex((el) => el.getBoundingClientRect().top > top + 1);
  return n < 0 ? els.length : n;
}

/**
 * Where a movement key leads from position `at` (-1: nowhere yet) among
 * `count` entries laid out `columns` to a row, or null if the key is not a
 * movement. Left and right only move in a grid.
 */
export function moveTarget(key: string, at: number, count: number, columns: number): number | null {
  if (!count) return null;
  const clamp = (i: number) => Math.max(0, Math.min(count - 1, i));
  switch (key) {
    case 'ArrowDown':
      return at < 0 ? 0 : clamp(at + columns);
    case 'ArrowUp':
      return at < 0 ? 0 : clamp(at - columns);
    case 'ArrowRight':
      return columns > 1 ? clamp(at + 1) : null;
    case 'ArrowLeft':
      return columns > 1 ? clamp(at - 1) : null;
    case 'Home':
      return 0;
    case 'End':
      return count - 1;
  }
  return null;
}
