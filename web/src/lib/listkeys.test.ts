import { describe, expect, it } from 'vitest';
import { moveTarget } from './listkeys.js';

describe('moveTarget', () => {
  it('moves one at a time in a list and ignores left and right', () => {
    expect(moveTarget('ArrowDown', -1, 5, 1)).toBe(0);
    expect(moveTarget('ArrowDown', 1, 5, 1)).toBe(2);
    expect(moveTarget('ArrowDown', 4, 5, 1)).toBe(4);
    expect(moveTarget('ArrowUp', 0, 5, 1)).toBe(0);
    expect(moveTarget('ArrowRight', 1, 5, 1)).toBeNull();
  });

  it('moves by rows and columns in a grid', () => {
    expect(moveTarget('ArrowDown', 1, 10, 4)).toBe(5);
    expect(moveTarget('ArrowDown', 8, 10, 4)).toBe(9);
    expect(moveTarget('ArrowUp', 5, 10, 4)).toBe(1);
    expect(moveTarget('ArrowRight', 3, 10, 4)).toBe(4);
    expect(moveTarget('ArrowLeft', 0, 10, 4)).toBe(0);
  });

  it('knows Home and End, and nothing else', () => {
    expect(moveTarget('Home', 7, 10, 4)).toBe(0);
    expect(moveTarget('End', 0, 10, 4)).toBe(9);
    expect(moveTarget('a', 0, 10, 4)).toBeNull();
    expect(moveTarget('ArrowDown', 0, 0, 1)).toBeNull();
  });
});
