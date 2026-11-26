/** Bytes in a gigabyte, as sizes are entered in the forms. */
export const GB = 1024 ** 3;

export function formatSize(n: number | undefined | null): string {
  if (n === undefined || n === null) return '';
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB', 'PB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

const dtf = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });

export function formatDate(s: string | null | undefined): string {
  if (!s) return '';
  const d = new Date(s);
  if (isNaN(d.getTime())) return '';
  return dtf.format(d);
}

export function relativeTime(s: string): string {
  const diff = (new Date(s).getTime() - Date.now()) / 1000;
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
  const abs = Math.abs(diff);
  if (abs < 60) return rtf.format(Math.round(diff), 'second');
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute');
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour');
  return rtf.format(Math.round(diff / 86400), 'day');
}

/** Slash-separated relative path → /files/... URL. */
export function parentPath(p: string): string {
  const i = p.lastIndexOf('/');
  return i < 0 ? '' : p.slice(0, i);
}

export function baseName(p: string): string {
  const i = p.lastIndexOf('/');
  return i < 0 ? p : p.slice(i + 1);
}

/** Breadcrumbs for a slash-separated path: one { name, path } per segment. */
export function pathCrumbs(p: string): { name: string; path: string }[] {
  if (!p) return [];
  const parts = p.split('/');
  return parts.map((name, i) => ({ name, path: parts.slice(0, i + 1).join('/') }));
}

export function joinPath(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name;
}

export type Kind =
  'dir' | 'image' | 'video' | 'audio' | 'pdf' | 'text' | 'archive' | 'model' | 'file';

/** 3D model types the viewer can draw. */
export const MODEL_TYPES = ['model/obj', 'application/vnd.autodesk.fbx', 'model/stl'];

export function kindOf(e: { is_dir: boolean; mime?: string }): Kind {
  if (e.is_dir) return 'dir';
  const m = e.mime || '';
  if (m.startsWith('image/') && m !== 'image/svg+xml') return 'image';
  if (m.startsWith('video/')) return 'video';
  if (m.startsWith('audio/')) return 'audio';
  if (m === 'application/pdf') return 'pdf';
  if (MODEL_TYPES.includes(m)) return 'model';
  if (
    m.startsWith('text/') ||
    m === 'application/json' ||
    m === 'application/xml' ||
    m === 'application/x-subrip' ||
    m === 'model/step'
  )
    return 'text';
  if (/zip|tar|gzip|bzip2|7z|rar/.test(m) && !m.includes('officedocument')) return 'archive';
  return 'file';
}

/** Image types browsers render and the server serves inline. */
export function isViewableImage(mime?: string): boolean {
  return ['image/jpeg', 'image/png', 'image/gif', 'image/webp', 'image/avif', 'image/bmp'].includes(
    mime || ''
  );
}

/** Whether the server may produce a thumbnail. */
export function thumbable(e: { is_dir: boolean; mime?: string }): boolean {
  if (e.is_dir) return false;
  const m = e.mime || '';
  return (
    [
      'image/jpeg',
      'image/png',
      'image/gif',
      'image/webp',
      'image/bmp',
      'image/heic',
      'image/heif',
      'image/avif',
      'image/tiff',
    ].includes(m) || m.startsWith('video/')
  );
}

const MB = 1024 * 1024;

/**
 * Previews that load a whole file into the page. Up to `auto` bytes they
 * open on their own; above that the viewer asks first; above `max` there is
 * no preview.
 */
export const PREVIEW_LIMITS: Partial<Record<Kind, { auto: number; max: number; why: string }>> = {
  text: {
    auto: MB,
    max: 32 * MB,
    why: 'Large text files can make the page slow, and are shown without highlighting.',
  },
  model: {
    auto: 32 * MB,
    max: 1024 * MB,
    why: 'Showing a 3D model downloads the whole file and needs several times its size in memory.',
  },
  image: {
    auto: 64 * MB,
    max: Infinity,
    why: 'Showing an image this large downloads the whole file.',
  },
};
