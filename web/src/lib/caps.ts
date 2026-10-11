// Playback capability detection, reported to the server as "c:mp4,v:h264,a:aac,...".

const containers: Record<string, string[]> = {
  mp4: ['video/mp4'],
  webm: ['video/webm'],
  ogg: ['audio/ogg', 'video/ogg'],
  flac: ['audio/flac'],
  mp3: ['audio/mpeg'],
  wav: ['audio/wav'],
  aac: ['audio/aac'],
  mkv: ['video/x-matroska', 'video/matroska'],
};

const video: Record<string, string[]> = {
  h264: ['video/mp4; codecs="avc1.640028"', 'video/mp4; codecs="avc1.42E01E"'],
  hevc: ['video/mp4; codecs="hvc1.1.6.L120.90"', 'video/mp4; codecs="hev1.1.6.L120.90"'],
  vp9: ['video/webm; codecs="vp9"', 'video/mp4; codecs="vp09.00.10.08"'],
  av1: ['video/mp4; codecs="av01.0.05M.08"', 'video/webm; codecs="av01.0.05M.08"'],
  vp8: ['video/webm; codecs="vp8"'],
};

const audio: Record<string, string[]> = {
  aac: ['audio/mp4; codecs="mp4a.40.2"'],
  mp3: ['audio/mpeg', 'audio/mp4; codecs="mp3"'],
  opus: ['audio/ogg; codecs="opus"', 'audio/webm; codecs="opus"', 'audio/mp4; codecs="opus"'],
  vorbis: ['audio/ogg; codecs="vorbis"', 'audio/webm; codecs="vorbis"'],
  flac: ['audio/flac', 'audio/mp4; codecs="flac"'],
  ac3: ['audio/mp4; codecs="ac-3"'],
  eac3: ['audio/mp4; codecs="ec-3"'],
  alac: ['audio/mp4; codecs="alac"'],
  pcm: ['audio/wav; codecs="1"', 'audio/wav'],
};

let cached: string | null = null;

function supported(types: string[], el: HTMLMediaElement): boolean {
  const MS = (window as unknown as { MediaSource?: { isTypeSupported(t: string): boolean } })
    .MediaSource;
  return types.some((t) => el.canPlayType(t) !== '' || (MS?.isTypeSupported?.(t) ?? false));
}

// Live HLS (fragmented MP4) plays with hls.js over Media Source Extensions, or
// natively where there is no MSE (iPhone Safari before ManagedMediaSource).
function hlsSupported(el: HTMLMediaElement): boolean {
  const w = window as unknown as { MediaSource?: unknown; ManagedMediaSource?: unknown };
  return (
    !!(w.MediaSource || w.ManagedMediaSource) ||
    el.canPlayType('application/vnd.apple.mpegurl') !== ''
  );
}

export function playbackCaps(): string {
  if (cached !== null) return cached;
  const el = document.createElement('video');
  const out: string[] = [];
  for (const [k, t] of Object.entries(containers)) if (supported(t, el)) out.push('c:' + k);
  if (hlsSupported(el)) out.push('c:hls');
  for (const [k, t] of Object.entries(video)) if (supported(t, el)) out.push('v:' + k);
  for (const [k, t] of Object.entries(audio)) if (supported(t, el)) out.push('a:' + k);
  cached = out.join(',');
  return cached;
}
