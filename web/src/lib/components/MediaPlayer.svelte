<script lang="ts">
  import { onDestroy } from 'svelte';
  import { ApiError, errorMessage, get, post, qs } from '#lib/api.js';
  import { playbackCaps } from '#lib/caps.js';
  import type { Job, Plan } from '#lib/types.js';
  import CopyField from './CopyField.svelte';
  import Icon from './Icon.svelte';

  let { path, mime }: { path: string; mime: string } = $props();

  const isAudio = $derived(mime.startsWith('audio/'));
  let plan = $state<Plan | null>(null);
  let src = $state('');
  let job = $state<Job | null>(null);
  let status = $state<'loading' | 'ready' | 'preparing' | 'external' | 'error'>('loading');
  let message = $state('');
  let audioIdx = $state(-1);
  let externalURL = $state('');
  let externalErr = $state('');
  let timer: ReturnType<typeof setTimeout> | undefined;
  let token = 0;

  const directURL = $derived('/api/files/download' + qs({ path, inline: 1 }));
  const pct = $derived(Math.round((job?.progress ?? 0) * 100));

  function streamURL(variant: string) {
    return '/api/media/stream' + qs({ path, variant });
  }

  async function load(audio: number) {
    const my = ++token;
    clearTimeout(timer);
    status = 'loading';
    src = '';
    job = null;
    externalURL = '';
    try {
      const res = await get<{ plan: Plan; job?: Job }>(
        '/api/media/info' +
          qs({ path, caps: playbackCaps(), audio: audio >= 0 ? audio : undefined })
      );
      if (my !== token) return;
      plan = res.plan;
      audioIdx = res.plan.audio_index;
      if (plan.mode === 'direct') {
        src = directURL;
        status = 'ready';
      } else if (plan.mode === 'remux' || plan.mode === 'transcode') {
        if (res.job?.state === 'done') {
          src = streamURL(plan.variant!);
          status = 'ready';
        } else {
          await prepare(my);
        }
      } else {
        status = 'external';
        message = plan.reason || 'This file cannot be played in the browser.';
      }
    } catch (err) {
      if (my !== token) return;
      // No probe (ffprobe missing or unreadable file): try the file as is.
      if (err instanceof ApiError && (err.status === 422 || err.status === 501)) {
        plan = null;
        src = directURL;
        status = 'ready';
        return;
      }
      status = 'error';
      message = errorMessage(err);
    }
  }

  async function prepare(my: number) {
    if (!plan?.variant) return;
    status = 'preparing';
    try {
      job = await post<Job>('/api/media/prepare', { path, variant: plan.variant });
      poll(my);
    } catch (err) {
      if (my !== token) return;
      status = 'error';
      message = errorMessage(err);
    }
  }

  function poll(my: number) {
    if (my !== token || !job || !plan?.variant) return;
    if (job.state === 'done') {
      src = streamURL(plan.variant);
      status = 'ready';
      return;
    }
    if (job.state === 'failed') {
      status = 'error';
      message = job.error || 'Conversion failed.';
      return;
    }
    timer = setTimeout(async () => {
      try {
        job = await get<Job>('/api/media/job' + qs({ path, variant: plan!.variant! }));
      } catch (err) {
        if (my !== token) return;
        status = 'error';
        message = errorMessage(err);
        return;
      }
      poll(my);
    }, 1500);
  }

  async function openExternal() {
    externalErr = '';
    try {
      const variant =
        status === 'ready' && plan && plan.mode !== 'direct' ? plan.variant : undefined;
      const res = await post<{ url: string }>('/api/media/external-url', { path, variant });
      externalURL = res.url;
    } catch (err) {
      externalErr = errorMessage(err);
    }
  }

  function subURL(s: { track: number; sidecar?: string }) {
    return (
      '/api/media/subtitle' +
      qs(s.sidecar ? { path, sidecar: s.sidecar } : { path, track: s.track })
    );
  }

  $effect(() => {
    void path;
    load(-1);
  });

  onDestroy(() => {
    token++;
    clearTimeout(timer);
  });
</script>

<div class="player">
  {#if status === 'loading'}
    <p class="muted">Loading…</p>
  {:else if status === 'preparing'}
    <div class="prep">
      <p>
        Preparing this file for playback{plan?.mode === 'transcode' ? ' (converting audio)' : ''}…
      </p>
      <div class="progress"><div style="width:{pct}%"></div></div>
      <p class="muted small">
        {job?.state === 'queued' ? 'Waiting for a free slot…' : `${pct}%`}. This only happens once;
        the result is cached.
      </p>
    </div>
  {:else if status === 'ready'}
    {#if isAudio && !plan?.video}
      <audio controls autoplay {src}></audio>
    {:else}
      <!-- svelte-ignore a11y_media_has_caption -->
      <video controls autoplay playsinline {src}>
        {#each plan?.subtitles ?? [] as s, i (i)}
          <track
            kind="subtitles"
            src={subURL(s)}
            srclang={s.language && s.language.length <= 3 ? s.language : undefined}
            label={s.title || s.language || s.sidecar || `Track ${s.track}`}
          />
        {/each}
      </video>
    {/if}
  {:else}
    <div class={status === 'error' ? 'error' : 'warn'}>{message}</div>
  {/if}

  <div class="controls row">
    {#if plan && plan.audio.length > 1}
      <label class="inline small">
        Audio
        <select
          value={audioIdx}
          onchange={(e) => load(Number((e.currentTarget as HTMLSelectElement).value))}
        >
          {#each plan.audio as a (a.index)}
            <option value={a.index}>
              {a.title || a.language || `Track ${a.index}`} ({a.codec}{a.channels
                ? `, ${a.channels}ch`
                : ''})
            </option>
          {/each}
        </select>
      </label>
    {/if}
    <span class="spacer"></span>
    <button class="small" onclick={openExternal}
      ><Icon name="external" size={16} />Open in VLC / stream URL</button
    >
  </div>
  {#if externalErr}<div class="error">{externalErr}</div>{/if}
  {#if externalURL}
    <div class="ext">
      <CopyField value={externalURL} />
      <p class="small muted">
        Valid for a few hours. <a href={'vlc://' + externalURL}>Open in VLC</a>, or paste it into
        VLC or mpv as a network stream.
      </p>
    </div>
  {/if}
</div>

<style>
  .player {
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  video {
    width: 100%;
    /* Reserve the player's size before metadata arrives (letterboxed after). */
    aspect-ratio: 16 / 9;
    max-height: 70vh;
    background: #000;
    border-radius: var(--radius);
  }
  audio {
    width: 100%;
  }
  .prep {
    padding: 2rem 1rem;
    text-align: center;
  }
  .ext {
    background: var(--surface-2);
    color: var(--text);
    padding: 0.6rem;
    border-radius: var(--radius);
  }
</style>
