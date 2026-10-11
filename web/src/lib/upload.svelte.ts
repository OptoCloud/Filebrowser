import * as tus from 'tus-js-client';

export interface PickedFile {
  file: File;
  /** Path relative to the drop target, including the file name ("a/b/c.txt"). */
  relativePath: string;
}

export interface UploadItem {
  id: number;
  name: string;
  relativePath: string;
  size: number;
  sent: number;
  status: 'queued' | 'uploading' | 'done' | 'error' | 'cancelled';
  error?: string;
  storedName?: string;
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
  if (status === 401) return 'Not authorized (session expired or password required)';
  if (status === 429) return 'Too many requests, try again later';
  if (status === 413) return body.trim() || 'File too large or quota exceeded';
  return body.trim() || `Upload failed (HTTP ${status})`;
}

/** A queue of tus uploads with reactive progress. */
export class Uploader {
  items = $state<UploadItem[]>([]);
  // Internal bookkeeping of in-flight tus uploads; never read by the UI, so no reactivity needed.
  // eslint-disable-next-line svelte/prefer-svelte-reactivity
  private uploads = new Map<number, tus.Upload>();
  private queue: { item: UploadItem; file: File; dir: string }[] = [];
  private active = 0;
  private endpoint: string;
  private headers: Record<string, string>;
  private onFileDone?: (item: UploadItem) => void;

  constructor(
    endpoint: string,
    headers: Record<string, string> = {},
    onFileDone?: (item: UploadItem) => void
  ) {
    this.endpoint = endpoint;
    this.headers = headers;
    this.onFileDone = onFileDone;
  }

  get busy(): boolean {
    return this.items.some((i) => i.status === 'queued' || i.status === 'uploading');
  }

  add(files: PickedFile[], dir: string) {
    for (const f of files) {
      this.items.push({
        id: nextId++,
        name: f.file.name,
        relativePath: f.relativePath || f.file.name,
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
    const finish = () => {
      this.active--;
      this.uploads.delete(item.id);
      this.pump();
    };
    const upload = new tus.Upload(file, {
      endpoint: this.endpoint,
      chunkSize: CHUNK,
      retryDelays: [0, 2000, 5000, 10000],
      headers: this.headers,
      removeFingerprintOnSuccess: true,
      metadata: {
        filename: file.name,
        dir,
        relativePath: item.relativePath,
        filetype: file.type || 'application/octet-stream',
      },
      onProgress: (sent) => {
        item.sent = sent;
      },
      onAfterResponse: (_req, res) => {
        const name = res.getHeader('X-Fb-Name') || res.getHeader('X-Fb-Path');
        if (name) item.storedName = name.split('/').pop();
      },
      onSuccess: () => {
        item.status = 'done';
        item.sent = item.size;
        finish();
        this.onFileDone?.(item);
      },
      onError: (err) => {
        if (item.status === 'cancelled') return;
        item.status = 'error';
        item.error = tusError(err);
        finish();
      },
    });
    this.uploads.set(item.id, upload);
    upload
      .findPreviousUploads()
      .then((prev) => {
        if (prev.length) upload.resumeFromPreviousUpload(prev[0]);
        upload.start();
      })
      .catch(() => upload.start());
  }

  cancel(id: number) {
    const item = this.items.find((i) => i.id === id);
    if (!item || (item.status !== 'queued' && item.status !== 'uploading')) return;
    const wasActive = item.status === 'uploading';
    item.status = 'cancelled';
    const up = this.uploads.get(id);
    if (up) {
      up.abort(true).catch(() => {});
      this.uploads.delete(id);
    }
    if (wasActive) {
      this.active--;
      this.pump();
    }
  }

  cancelAll() {
    for (const i of this.items) this.cancel(i.id);
  }

  clearFinished() {
    this.items = this.items.filter((i) => i.status === 'queued' || i.status === 'uploading');
  }
}

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
