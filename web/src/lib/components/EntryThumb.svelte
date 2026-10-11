<script lang="ts">
  import { thumbable } from '#lib/format.js';
  import EntryIcon from './EntryIcon.svelte';

  // Square thumbnail for a grid tile, falling back to the type icon.
  let {
    entry,
    src,
    size = 48,
  }: { entry: { is_dir: boolean; mime?: string }; src: string; size?: number } = $props();

  // Remembered per URL, so a changed file (new src) gets another try.
  let failedSrc = $state('');
</script>

<div class="thumb">
  {#if thumbable(entry) && failedSrc !== src}
    <img loading="lazy" alt="" {src} onerror={() => (failedSrc = src)} />
  {:else}
    <EntryIcon {entry} {size} />
  {/if}
</div>

<style>
  .thumb {
    aspect-ratio: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--surface-2);
    overflow: hidden;
  }
  img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
</style>
