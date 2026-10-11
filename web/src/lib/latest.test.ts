import { describe, expect, it } from 'vitest';
import { LatestRequest } from './latest.js';

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

describe('LatestRequest', () => {
  it('aborts the previous signal when a new request starts', () => {
    const req = new LatestRequest();
    const a = req.next();
    expect(a.aborted).toBe(false);
    const b = req.next();
    expect(a.aborted).toBe(true);
    expect(b.aborted).toBe(false);
  });

  it('abort() cancels the current request and is idempotent', () => {
    const req = new LatestRequest();
    const a = req.next();
    req.abort();
    req.abort();
    expect(a.aborted).toBe(true);
    expect(req.next().aborted).toBe(false);
  });

  it('lets only the newest of out-of-order responses apply', async () => {
    const req = new LatestRequest();
    let shown = '';
    async function load(d: { promise: Promise<string> }) {
      const signal = req.next();
      const v = await d.promise;
      if (signal.aborted) return;
      shown = v;
    }
    const slow = deferred<string>();
    const fast = deferred<string>();
    const first = load(slow);
    const second = load(fast);
    fast.resolve('new');
    await second;
    slow.resolve('old');
    await first;
    expect(shown).toBe('new');
  });
});
