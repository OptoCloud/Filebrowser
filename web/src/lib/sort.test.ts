import { describe, expect, it } from 'vitest';
import { sortEntries } from './sort.js';
import type { Entry } from './types.js';

const e = (name: string, size = 0, mtime = '2026-01-01T00:00:00Z', is_dir = false): Entry => ({
  name,
  path: name,
  size,
  mtime,
  is_dir,
});

describe('sortEntries', () => {
  const list = [
    e('file10.txt', 5, '2026-03-01T00:00:00Z'),
    e('File2.txt', 9, '2026-01-01T00:00:00Z'),
    e('zeta', 0, '2026-02-01T00:00:00Z', true),
    e('alpha.txt', 5, '2026-02-01T00:00:00Z'),
  ];
  const names = (key: 'name' | 'size' | 'mtime', asc: boolean) =>
    sortEntries(list, key, asc).map((x) => x.name);

  it('puts folders first and reads numbers in names as numbers', () => {
    expect(names('name', true)).toEqual(['zeta', 'alpha.txt', 'File2.txt', 'file10.txt']);
    expect(names('name', false)).toEqual(['zeta', 'file10.txt', 'File2.txt', 'alpha.txt']);
  });

  it('sorts by size and date, the name breaking ties', () => {
    expect(names('size', true)).toEqual(['zeta', 'alpha.txt', 'file10.txt', 'File2.txt']);
    expect(names('mtime', false)).toEqual(['zeta', 'file10.txt', 'alpha.txt', 'File2.txt']);
  });

  it('leaves the listing it was given alone', () => {
    sortEntries(list, 'size', true);
    expect(list[0].name).toBe('file10.txt');
  });
});
