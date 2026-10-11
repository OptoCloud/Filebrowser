import { beforeEach, describe, expect, it, vi } from 'vitest';
import { Uploader } from './upload.svelte.js';

interface FakeOptions {
  fingerprint: (file: File, options: unknown) => Promise<string>;
  onSuccess: () => void;
  onError: (err: Error) => void;
  [k: string]: unknown;
}

// A stand-in for tus.Upload that records what the Uploader does with it.
const mocks = vi.hoisted(() => {
  class FakeUpload {
    started = false;
    aborted = false;
    options: FakeOptions;
    constructor(_file: File, options: FakeOptions) {
      this.options = options;
      mocks.instances.push(this);
    }
    findPreviousUploads() {
      return mocks.previous ?? Promise.resolve([]);
    }
    resumeFromPreviousUpload() {}
    start() {
      this.started = true;
    }
    abort() {
      this.aborted = true;
      return Promise.resolve();
    }
  }
  return {
    FakeUpload,
    instances: [] as InstanceType<typeof FakeUpload>[],
    previous: null as Promise<unknown[]> | null,
  };
});

vi.mock('tus-js-client', () => ({ Upload: mocks.FakeUpload }));

const flush = () => new Promise((r) => setTimeout(r, 0));

function file(name: string, size = 10) {
  return new File(['x'.repeat(size)], name, { lastModified: 1000 });
}

function picked(n: number) {
  return Array.from({ length: n }, (_, i) => ({ file: file(`f${i}.txt`), relativePath: '' }));
}

beforeEach(() => {
  mocks.instances.length = 0;
  mocks.previous = null;
});

describe('Uploader', () => {
  it('never starts an upload cancelled while looking for a previous one', async () => {
    let resolve!: (v: unknown[]) => void;
    mocks.previous = new Promise((r) => (resolve = r));
    const up = new Uploader('/api/uploads/');
    up.add(picked(1), '');
    expect(mocks.instances).toHaveLength(1);

    up.cancel(up.items[0].id);
    resolve([]);
    await flush();

    expect(mocks.instances[0].started).toBe(false);
    expect(up.items[0].status).toBe('cancelled');
    expect(up.running).toBe(0);
  });

  it('never creates an upload for a cancelled queued item', async () => {
    const up = new Uploader('/api/uploads/');
    up.add(picked(4), '');
    await flush();
    expect(mocks.instances).toHaveLength(3);

    up.cancel(up.items[3].id);
    mocks.instances[0].options.onSuccess();
    await flush();

    expect(mocks.instances).toHaveLength(3);
    expect(up.items[3].status).toBe('cancelled');
    expect(up.running).toBe(2);
  });

  it('keeps concurrency within 0..3 across cancel/success/error races', async () => {
    const up = new Uploader('/api/uploads/');
    const seen: number[] = [];
    const check = () => seen.push(up.running);
    up.add(picked(6), 'dir');
    await flush();
    check();
    expect(up.running).toBe(3);

    // Cancel a running upload, then its late callbacks arrive.
    up.cancel(up.items[0].id);
    check();
    mocks.instances[0].options.onSuccess();
    mocks.instances[0].options.onError(new Error('aborted'));
    check();
    expect(mocks.instances[0].aborted).toBe(true);
    expect(up.items[0].status).toBe('cancelled');
    expect(up.running).toBe(3);

    // Success followed by a cancel of the same item.
    mocks.instances[1].options.onSuccess();
    up.cancel(up.items[1].id);
    check();
    expect(up.items[1].status).toBe('done');

    // Error followed by a duplicate success callback.
    mocks.instances[2].options.onError(new Error('boom'));
    mocks.instances[2].options.onSuccess();
    check();
    expect(up.items[2].status).toBe('error');

    await flush();
    for (const inst of mocks.instances.slice(3)) inst.options.onSuccess();
    check();
    up.cancelAll();
    check();

    expect(mocks.instances).toHaveLength(6);
    expect(up.running).toBe(0);
    expect(Math.max(...seen)).toBeLessThanOrEqual(3);
    expect(Math.min(...seen)).toBeGreaterThanOrEqual(0);
    expect(up.busy).toBe(false);
  });

  it('notifies completion listeners until they unsubscribe', async () => {
    const up = new Uploader('/api/uploads/');
    const done: string[] = [];
    const off = up.onFileDone((item) => done.push(`${item.dir}/${item.relativePath}`));
    up.add(picked(2), 'a/b');
    await flush();
    mocks.instances[0].options.onSuccess();
    off();
    mocks.instances[1].options.onSuccess();
    expect(done).toEqual(['a/b/f0.txt']);
  });

  it('fingerprints by destination, relative path and share token', async () => {
    async function fingerprint(headers: Record<string, string>, dir: string, rel: string) {
      const up = new Uploader('/_upload/', headers);
      const f = file('same.txt');
      up.add([{ file: f, relativePath: rel }], dir);
      const inst = mocks.instances[mocks.instances.length - 1];
      return inst.options.fingerprint(f, inst.options);
    }
    const base = await fingerprint({}, 'a', 'x/same.txt');
    expect(await fingerprint({}, 'a', 'x/same.txt')).toBe(base);
    expect(await fingerprint({}, 'b', 'x/same.txt')).not.toBe(base);
    expect(await fingerprint({}, 'a', 'y/same.txt')).not.toBe(base);
    const shareA = await fingerprint({ 'X-Share-Token': 'aaa' }, 'a', 'x/same.txt');
    const shareB = await fingerprint({ 'X-Share-Token': 'bbb' }, 'a', 'x/same.txt');
    expect(shareA).not.toBe(base);
    expect(shareA).not.toBe(shareB);
  });
});
