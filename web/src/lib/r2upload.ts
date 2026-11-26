/**
 * Uploads one file through the server's upload bucket (see
 * internal/uploads/r2.go): the server says into how many parts the file is
 * cut and signs an address for each; the parts go straight to the bucket; the
 * server then completes the upload and pulls the file. Nothing here knows the
 * bucket's credentials, and what may be uploaded was settled by `begin`.
 */

export interface R2Options {
  /** Where the server's side lives: '/api/r2/' or '/_r2/'. */
  base: string;
  /** Sent with every request to the server (a drop link's X-Share-Token). */
  headers: Record<string, string>;
  file: File;
  dir: string;
  relativePath: string;
  /** Drop links that ask for it: the uploader's name. */
  uploader?: string;
  /** localStorage key under which the upload is remembered, to take it up again. */
  resumeKey: string;
  signal: AbortSignal;
  /** Bytes in the bucket so far. */
  onProgress: (sent: number) => void;
  /** The file is in the bucket; the server is fetching it. */
  onFinishing: () => void;
}

/** An answer from the server (or the bucket) that is not a success. */
export class R2Error extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

const CONCURRENCY = 3;
const PART_TRIES = 4;

interface Status {
  state: 'uploading' | 'uploaded' | 'pulling' | 'done' | 'failed' | 'expired';
  part_size: number;
  parts: number;
  have?: number[];
  name?: string;
  error?: string;
}

async function call<T>(o: R2Options, method: string, op: string, body?: unknown): Promise<T> {
  const res = await fetch(o.base + op, {
    method,
    credentials: 'same-origin',
    signal: o.signal,
    headers: { ...o.headers, ...(body ? { 'Content-Type': 'application/json' } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  const json = (await res.json().catch(() => ({}))) as T & { error?: string };
  if (!res.ok)
    throw Object.assign(new R2Error(res.status, json.error || `HTTP ${res.status}`), json);
  return json;
}

const sleep = (ms: number, signal: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    const t = setTimeout(resolve, ms);
    signal.addEventListener('abort', () => (clearTimeout(t), reject(signal.reason)), {
      once: true,
    });
  });

/** PUTs one part to its signed address, reporting progress within it. */
function putPart(url: string, blob: Blob, signal: AbortSignal, progress: (n: number) => void) {
  return new Promise<void>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('PUT', url);
    xhr.upload.onprogress = (e) => progress(e.loaded);
    xhr.onload = () =>
      xhr.status >= 200 && xhr.status < 300
        ? resolve()
        : reject(new R2Error(xhr.status, `The upload buffer answered ${xhr.status}`));
    xhr.onerror = () => reject(new R2Error(0, 'Network error'));
    xhr.onabort = () => reject(signal.reason);
    signal.addEventListener('abort', () => xhr.abort(), { once: true });
    xhr.send(blob);
  });
}

function remember(key: string, id: string | null) {
  try {
    if (id) localStorage.setItem(key, id);
    else localStorage.removeItem(key);
  } catch {
    /* storage unavailable: the upload just cannot be taken up again */
  }
}

function remembered(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

/** Uploads o.file and resolves with the name it was stored under. */
export async function uploadViaR2(o: R2Options): Promise<string> {
  const { file, signal } = o;
  let id = remembered(o.resumeKey);
  let partSize = 0;
  let parts = 0;
  let have: number[] = [];
  let state: Status['state'] = 'uploading';

  // An upload started earlier (a reload, a lost connection) is taken up
  // where the bucket has it.
  if (id) {
    try {
      const st = await call<Status>(o, 'GET', 'status?id=' + encodeURIComponent(id));
      if (st.state === 'failed' || st.state === 'expired') id = null;
      else {
        ({ state, part_size: partSize, parts } = st);
        have = st.have ?? [];
      }
    } catch (e) {
      if (signal.aborted) throw e;
      id = null;
    }
  }
  if (!id) {
    const b = await call<{ id: string; part_size: number; parts: number }>(o, 'POST', 'begin', {
      dir: o.dir,
      name: file.name,
      relative_path: o.relativePath,
      uploader: o.uploader ?? '',
      size: file.size,
    });
    ({ id, part_size: partSize, parts } = b);
    remember(o.resumeKey, id);
  }
  const uploadId = id;
  // Leaving for good (not a failure that may be retried) discards the parts.
  const giveUp = () => {
    remember(o.resumeKey, null);
    void fetch(o.base + 'abort', {
      method: 'POST',
      credentials: 'same-origin',
      keepalive: true,
      headers: { ...o.headers, 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: uploadId }),
    }).catch(() => {});
  };
  signal.addEventListener('abort', giveUp, { once: true });

  try {
    const lengthOf = (n: number) => (n === parts ? file.size - (n - 1) * partSize : partSize);
    for (let round = 0; state === 'uploading'; round++) {
      const done = new Map(have.map((n) => [n, lengthOf(n)] as const));
      const inFlight = new Map<number, number>();
      const report = () => {
        let sum = 0;
        for (const v of done.values()) sum += v;
        for (const v of inFlight.values()) sum += v;
        o.onProgress(sum);
      };
      report();
      const todo: number[] = [];
      for (let n = 1; n <= parts; n++) if (!done.has(n)) todo.push(n);

      const sendPart = async (n: number) => {
        for (let attempt = 1; ; attempt++) {
          try {
            // The address is good for minutes: one is asked for as its part is sent.
            const { urls } = await call<{ urls: Record<string, string> }>(o, 'POST', 'sign', {
              id: uploadId,
              parts: [n],
            });
            const blob = file.slice((n - 1) * partSize, (n - 1) * partSize + lengthOf(n));
            await putPart(urls[String(n)], blob, signal, (loaded) => {
              inFlight.set(n, loaded);
              report();
            });
            inFlight.delete(n);
            done.set(n, lengthOf(n));
            report();
            return;
          } catch (e) {
            inFlight.delete(n);
            // The server said no (the upload ended, or was never ours): asking again will not help.
            const refused =
              e instanceof R2Error && e.status >= 400 && e.status < 500 && e.status !== 408;
            if (signal.aborted || attempt >= PART_TRIES || (refused && e.status !== 403)) throw e;
            await sleep(1000 * 2 ** (attempt - 1), signal);
          }
        }
      };
      const workers = Array.from({ length: Math.min(CONCURRENCY, todo.length) }, async () => {
        for (let n = todo.shift(); n !== undefined; n = todo.shift()) await sendPart(n);
      });
      await Promise.all(workers);

      try {
        await call(o, 'POST', 'complete', { id: uploadId });
        state = 'uploaded';
      } catch (e) {
        // The bucket does not have a part we thought sent: send those again, once.
        const missing = (e as { missing?: number[] }).missing;
        if (!(e instanceof R2Error) || e.status !== 409 || !missing || round > 0) throw e;
        have = Array.from({ length: parts }, (_, i) => i + 1).filter((n) => !missing.includes(n));
      }
    }

    // In the bucket: the server now fetches the file, which takes a while for a large one.
    o.onProgress(file.size);
    o.onFinishing();
    for (let wait = 500; ; wait = Math.min(wait * 1.5, 5000)) {
      const st = await call<Status>(o, 'GET', 'status?id=' + encodeURIComponent(uploadId));
      if (st.state === 'done') {
        remember(o.resumeKey, null);
        return st.name ?? file.name;
      }
      if (st.state === 'failed' || st.state === 'expired') {
        remember(o.resumeKey, null);
        throw new R2Error(422, st.error || 'The upload failed');
      }
      await sleep(wait, signal);
    }
  } finally {
    signal.removeEventListener('abort', giveUp);
  }
}
