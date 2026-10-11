<script lang="ts">
  import { kindOf } from '#lib/format.js';
  import Icon from './Icon.svelte';

  let { entry, size = 20 }: { entry: { is_dir: boolean; mime?: string }; size?: number } = $props();

  // Icon names match the kinds, except for folders.
  const name = $derived(entry.is_dir ? 'folder' : kindOf(entry));
</script>

<span class="ic" class:dir={entry.is_dir}><Icon {name} {size} /></span>

<style>
  .ic {
    color: var(--muted);
    display: inline-flex;
  }
  .ic.dir {
    color: #e0a526;
  }
</style>
