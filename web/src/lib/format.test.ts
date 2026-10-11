import { describe, expect, it } from 'vitest';
import {
  baseName,
  filesHref,
  formatSize,
  isViewableImage,
  joinPath,
  kindOf,
  parentPath,
  pathCrumbs,
  thumbable,
} from './format.js';

describe('formatSize', () => {
  it('handles missing values', () => {
    expect(formatSize(undefined)).toBe('');
    expect(formatSize(null)).toBe('');
  });

  it('formats bytes and binary units', () => {
    expect(formatSize(0)).toBe('0 B');
    expect(formatSize(1023)).toBe('1023 B');
    expect(formatSize(1024)).toBe('1.0 KB');
    expect(formatSize(1536)).toBe('1.5 KB');
    expect(formatSize(20 * 1024 * 1024)).toBe('20 MB');
    expect(formatSize(1024 ** 6)).toBe('1024 PB');
  });
});

describe('path helpers', () => {
  it('splits and joins slash-separated paths', () => {
    expect(parentPath('a/b/c.txt')).toBe('a/b');
    expect(parentPath('c.txt')).toBe('');
    expect(baseName('a/b/c.txt')).toBe('c.txt');
    expect(baseName('c.txt')).toBe('c.txt');
    expect(joinPath('', 'x')).toBe('x');
    expect(joinPath('a/b', 'x')).toBe('a/b/x');
  });

  it('builds breadcrumbs', () => {
    expect(pathCrumbs('')).toEqual([]);
    expect(pathCrumbs('a/b')).toEqual([
      { name: 'a', path: 'a' },
      { name: 'b', path: 'a/b' },
    ]);
  });

  it('builds encoded /files URLs', () => {
    expect(filesHref('')).toBe('/files');
    expect(filesHref('a b/c#d')).toBe('/files/a%20b/c%23d');
  });
});

describe('kindOf / previews', () => {
  it('classifies entries by mime type', () => {
    expect(kindOf({ is_dir: true })).toBe('dir');
    expect(kindOf({ is_dir: false, mime: 'image/png' })).toBe('image');
    expect(kindOf({ is_dir: false, mime: 'image/svg+xml' })).toBe('file');
    expect(kindOf({ is_dir: false, mime: 'video/mp4' })).toBe('video');
    expect(kindOf({ is_dir: false, mime: 'application/json' })).toBe('text');
    expect(kindOf({ is_dir: false, mime: 'application/zip' })).toBe('archive');
    expect(kindOf({ is_dir: false })).toBe('file');
  });

  it('only allows safe inline image types', () => {
    expect(isViewableImage('image/png')).toBe(true);
    expect(isViewableImage('image/svg+xml')).toBe(false);
    expect(isViewableImage(undefined)).toBe(false);
  });

  it('knows which entries can be thumbnailed', () => {
    expect(thumbable({ is_dir: false, mime: 'image/heic' })).toBe(true);
    expect(thumbable({ is_dir: false, mime: 'video/webm' })).toBe(true);
    expect(thumbable({ is_dir: true, mime: 'image/png' })).toBe(false);
    expect(thumbable({ is_dir: false, mime: 'text/plain' })).toBe(false);
  });
});
