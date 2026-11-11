import { describe, expect, it } from 'vitest';
import { filesHref, safeNext, shareHref } from './nav.js';

const origin = 'https://files.example.com';

describe('safeNext', () => {
  it('keeps same-origin paths with query and hash', () => {
    expect(safeNext('/files/a%20b?x=1#top', origin)).toBe('/files/a%20b?x=1#top');
    expect(safeNext('/settings', origin)).toBe('/settings');
    expect(safeNext(origin + '/trash', origin)).toBe('/trash');
  });

  it('falls back for missing values', () => {
    expect(safeNext(null, origin)).toBe('/files');
    expect(safeNext('', origin)).toBe('/files');
    expect(safeNext(undefined, origin, filesHref('x'))).toBe('/files/x');
  });

  it('rejects other origins and schemes', () => {
    expect(safeNext('//evil.example/files', origin)).toBe('/files');
    expect(safeNext('/\\evil.example', origin)).toBe('/files');
    expect(safeNext('https://evil.example/', origin)).toBe('/files');
    expect(safeNext('http://files.example.com/files', origin)).toBe('/files');
    expect(safeNext('javascript:alert(1)', origin)).toBe('/files');
    expect(safeNext('data:text/html,hi', origin)).toBe('/files');
  });
});

describe('route helpers', () => {
  it('builds encoded /files URLs', () => {
    expect(filesHref('')).toBe('/files');
    expect(filesHref('a b/c#d')).toBe('/files/a%20b/c%23d');
    expect(filesHref('what?')).toBe('/files/what%3F');
  });

  it('builds share URLs', () => {
    expect(shareHref('ab/c')).toBe('/s/ab%2Fc');
    expect(shareHref('t', 'a b/c')).toBe('/s/t?path=a+b%2Fc');
  });
});
