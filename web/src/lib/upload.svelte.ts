import * as tus from 'tus-js-client';
import { R2Error, uploadViaR2 } from './r2upload.js';

export interface PickedFile {
  file: File;
  /** Path relative to the drop target, including the file name ("a/b/c.txt"). */
  relativePath: string;
}

export interface UploadItem {
  id: number;
  name: string;
  relativePath: string;
  /** Destination folder the upload was started in. */
  dir: string;
  size: number;
  sent: number;
  /** finishing: sent to the upload bucket, the server is fetching it from there. */
  status: 'queued' | 'uploading' | 'finishing' | 'done' | 'error' | 'cancelled';
  error?: string;
  storedName?: string;
}

/** Prefix of the upload fingerprints, under which tus-js-client keeps what it needs to resume. */
const FINGERPRINT = 'fb';

async function digest(s: string): Promise<string> {
  if (!s) return '';
  const hash = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(s));
  return Array.from(new Uint8Array(hash).subarray(0, 12), (b) =>
    b.toString(16).padStart(2, '0')
  ).join('');
}

/**
 * Removes what was kept to resume unfinished uploads (file names and
 * folders), for signing out on a shared computer.
 */
export function forgetUploads() {
  try {
    for (let i = localStorage.length - 1; i >= 0; i--) {
      const k = localStorage.key(i);
      if (k?.startsWith(`tus::${FINGERPRINT}|`) || k?.startsWith('fbr2|'))
        localStorage.removeItem(k);
    }
  } catch {
    /* storage unavailable */
  }
}

const CHUNK = 50 * 1024 * 1024; // stays below Cloudflare's 100 MB request body limit
const CONCURRENCY = 3;

let nextId = 1;

function tusError(err: Error): string {
  const res = (err as Error & { originalResponse?: tus.HttpResponse | null }).originalResponse;
  if (!res) return 'Network error';
  const status = res.getStatus();
  let body = res.getBody() || '';
  try {
    const j = JSON.parse(body);
    if (j && typeof j.error === 'string') body = j.error;
  } catch {
    /* plain text */
  }
  // The server's own refusals come as "ERR_FB_REJECTED: <what to tell the user>".
  body = body.replace(/^ERR_[A-Z_]+:\s*/, '');
  if (status === 401) return 'Not authorized (session expired or password required)';
  if (status === 429) return 'Too many requests, try again later';
  if (status === 413) return body.trim() || 'File too large or quota exceeded';
  return body.trim() || `Upload failed (HTTP ${status})`;
}

function tusStatus(err: Error): number {
  return (
    (err as Error & { originalResponse?: tus.HttpResponse | null }).originalResponse?.getStatus() ??
    0
  );
}

/** Whether an item is still on its way: waiting, being sent, or being fetched by the server. */
export function isActive(item: UploadItem): boolean {
  return item.status === 'queued' || item.status === 'uploading' || item.status === 'finishing';
}

/** A queue of tus uploads with reactive progress. */
export class Uploader {
  items = $state<UploadItem[]>([]);
  // Internal bookkeeping of in-flight tus uploads; never read by the UI, so no reactivity needed.
  // eslint-disable-next-line svelte/prefer-svelte-reactivity
  private uploads = new Map<number, tus.Upload>();
  // eslint-disable-next-line svelte/prefer-svelte-reactivity
  private finishers = new Map<number, () => void>();
  private queue: { item: UploadItem; file: File; dir: string }[] = [];
  // What each failed or cancelled item was, so that it can be tried again.
  // eslint-disable-next-line svelte/prefer-svelte-reactivity
  private retryable = new Map<number, { file: File; dir: string }>();
  private active = 0;
  private endpoint: string;
  private headers: Record<string, string>;
  /** Further tus metadata sent with every upload, read when one starts. */
  extra: Record<string, string> = {};
  /**
   * Set when the server has an upload bucket: files of at least `threshold`
   * bytes go through it (see r2upload.ts) instead of tus.
   */
  r2: { base: string; threshold: number } | null = null;
  // eslint-disable-next-line svelte/prefer-svelte-reactivity
  private aborters = new Map<number, AbortController>();
  // eslint-disable-next-line svelte/prefer-svelte-reactivity
  private doneListeners = new Set<(item: UploadItem) => void>();

  constructor(endpoint: string, headers: Record<string, string> = {}) {
    this.endpoint = endpoint;
    this.headers = headers;
  }

  get busy(): boolean {
    return this.items.some(isActive);
  }

  /** Number of tus uploads currently started and not yet finished. */
  get running(): number {
    return this.active;
  }

  /** Calls fn whenever a file finishes uploading; returns an unsubscribe function. */
  onFileDone(fn: (item: UploadItem) => void): () => void {
    this.doneListeners.add(fn);
    return () => this.doneListeners.delete(fn);
  }

  add(files: PickedFile[], dir: string) {
    for (const f of files) {
      this.items.push({
        id: nextId++,
        name: f.file.name,
        relativePath: f.relativePath || f.file.name,
        dir,
        size: f.file.size,
        sent: 0,
        status: 'queued',
      });
      // Use the reactive proxy so later mutations update the UI.
      this.queue.push({ item: this.items[this.items.length - 1], file: f.file, dir });
    }
    this.pump();
  }

  private pump() {
    while (this.active < CONCURRENCY && this.queue.length) {
      const next = this.queue.shift()!;
      if (next.item.status !== 'queued') continue;
      this.start(next.item, next.file, next.dir);
    }
  }

  private start(item: UploadItem, file: File, dir: string) {
    this.active++;
    item.status = 'uploading';
    // Runs exactly once per started upload, whether it succeeds, fails or is cancelled.
    let finished = false;
    const finish = () => {
      if (finished) return;
      finished = true;
      this.active--;
      this.uploads.delete(item.id);
      this.finishers.delete(item.id);
      this.pump();
    };
    this.finishers.set(item.id, finish);
    if (this.r2 && file.size > 0 && file.size >= this.r2.threshold) {
      void this.startR2(item, file, dir, this.r2.base, () => finished, finish);
      return;
    }
    const upload = new tus.Upload(file, {
      endpoint: this.endpoint,
      chunkSize: CHUNK,
      retryDelays: [0, 2000, 5000, 10000],
      headers: this.headers,
      removeFingerprintOnSuccess: true,
      // The default fingerprint ignores the destination, so identical files in
      // different folders (or shares) would resume each other's uploads.
      // It is stored in localStorage until the upload succeeds, so the
      // share token goes in hashed.
      fingerprint: async (f) =>
        [
          FINGERPRINT,
          this.endpoint,
          await digest(this.headers['X-Share-Token'] ?? ''),
          dir,
          this.extra.uploader ?? '',
          item.relativePath,
          f.size,
          f.lastModified,
        ].join('|'),
      metadata: {
        filename: file.name,
        dir,
        relativePath: item.relativePath,
        filetype: file.type || 'application/octet-stream',
        ...this.extra,
      },
      onProgress: (sent) => {
        item.sent = sent;
      },
      onAfterResponse: (_req, res) => {
        const name = res.getHeader('X-Fb-Name') || res.getHeader('X-Fb-Path');
        if (name) item.storedName = name.split('/').pop();
      },
      // Late or duplicate callbacks (after cancel, or after the other outcome) are ignored.
      onSuccess: () => {
        if (finished) return;
        item.status = 'done';
        item.sent = item.size;
        finish();
        for (const fn of this.doneListeners) fn(item);
      },
      onError: (err) => {
        if (finished) return;
        item.status = 'error';
        item.error = tusError(err);
        // An answer that says no (too large, wrong kind, forbidden) will say so again.
        const status = tusStatus(err);
        if (!status || status >= 500 || status === 409 || status === 423 || status === 429)
          this.retryable.set(item.id, { file, dir });
        finish();
      },
    });
    this.uploads.set(item.id, upload);
    upload
      .findPreviousUploads()
      .then((prev) => {
        // Cancelled while looking for a previous upload: never start.
        if (item.status === 'cancelled') return;
        if (prev.length) upload.resumeFromPreviousUpload(prev[0]);
        upload.start();
      })
      .catch(() => {
        if (item.status !== 'cancelled') upload.start();
      });
  }

  /** Sends a file through the upload bucket; the counterpart of the tus path in start. */
  private async startR2(
    item: UploadItem,
    file: File,
    dir: string,
    base: string,
    isFinished: () => boolean,
    finish: () => void
  ) {
    const ctrl = new AbortController();
    this.aborters.set(item.id, ctrl);
    try {
      const name = await uploadViaR2({
        base,
        headers: this.headers,
        file,
        dir,
        relativePath: item.relativePath,
        uploader: this.extra.uploader,
        resumeKey: [
          'fbr2',
          base,
          await digest(this.headers['X-Share-Token'] ?? ''),
          dir,
          this.extra.uploader ?? '',
          item.relativePath,
          file.size,
          file.lastModified,
        ].join('|'),
        signal: ctrl.signal,
        onProgress: (sent) => {
          item.sent = sent;
        },
        onFinishing: () => {
          if (!isFinished()) item.status = 'finishing';
        },
      });
      if (isFinished()) return;
      item.status = 'done';
      item.sent = item.size;
      item.storedName = name;
      finish();
      for (const fn of this.doneListeners) fn(item);
    } catch (e) {
      if (isFinished()) return; // cancelled
      item.status = 'error';
      item.error = e instanceof Error ? e.message : String(e);
      // An answer that says no will say so again; anything else may pass.
      if (!(e instanceof R2Error) || e.status === 0 || e.status >= 500 || e.status === 429)
        this.retryable.set(item.id, { file, dir });
      finish();
    } finally {
      this.aborters.delete(item.id);
    }
  }

  cancel(id: number) {
    const item = this.items.find((i) => i.id === id);
    if (!item || !isActive(item)) return;
    const wasActive = item.status !== 'queued';
    item.status = 'cancelled';
    this.aborters.get(id)?.abort(new DOMException('cancelled', 'AbortError'));
    const up = this.uploads.get(id);
    if (up) {
      up.abort(true).catch(() => {});
      this.uploads.delete(id);
    }
    if (wasActive) {
      // Mark the upload finished so a late callback cannot decrement again.
      this.finishers.get(id)?.();
    }
  }

  /** Whether a failed upload can be tried again. */
  canRetry(id: number): boolean {
    return this.items.some((i) => i.id === id && i.status === 'error') && this.retryable.has(id);
  }

  /** Queues a failed upload again; it resumes from what the server already has. */
  retry(id: number) {
    const item = this.items.find((i) => i.id === id);
    const again = this.retryable.get(id);
    if (!item || !again || item.status !== 'error') return;
    this.retryable.delete(id);
    item.status = 'queued';
    item.error = undefined;
    this.queue.push({ item, ...again });
    this.pump();
  }

  cancelAll() {
    for (const i of this.items) this.cancel(i.id);
  }

  clearFinished() {
    this.items = this.items.filter(isActive);
    this.retryable.clear();
  }
}

/**
 * The uploader for the app origin. It lives for the whole page session, so
 * uploads keep running (and stay visible) when the user leaves the folder.
 */
export const appUploader = new Uploader('/api/uploads/');

/** Collects files (recursively for folders) from a drop event. */
export async function filesFromDrop(dt: DataTransfer): Promise<PickedFile[]> {
  const out: PickedFile[] = [];
  const entries: FileSystemEntry[] = [];
  for (const item of Array.from(dt.items)) {
    if (item.kind !== 'file') continue;
    const entry = item.webkitGetAsEntry?.();
    if (entry) entries.push(entry);
    else {
      const f = item.getAsFile();
      if (f) out.push({ file: f, relativePath: f.name });
    }
  }
  async function walk(entry: FileSystemEntry, prefix: string) {
    if (entry.isFile) {
      const file = await new Promise<File>((res, rej) =>
        (entry as FileSystemFileEntry).file(res, rej)
      );
      out.push({ file, relativePath: prefix + file.name });
    } else if (entry.isDirectory) {
      const reader = (entry as FileSystemDirectoryEntry).createReader();
      for (;;) {
        const batch = await new Promise<FileSystemEntry[]>((res, rej) =>
          reader.readEntries(res, rej)
        );
        if (!batch.length) break;
        for (const e of batch) await walk(e, prefix + entry.name + '/');
      }
    }
  }
  for (const e of entries) await walk(e, '');
  return out;
}

export function filesFromInput(list: FileList | null): PickedFile[] {
  if (!list) return [];
  return Array.from(list).map((file) => ({
    file,
    relativePath: file.webkitRelativePath || file.name,
  }));
}
