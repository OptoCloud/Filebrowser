<script lang="ts">
  import { copyText } from '#lib/api.js';
  import Icon from './Icon.svelte';

  let { value }: { value: string } = $props();
  let copied = $state(false);

  async function copy() {
    copied = await copyText(value);
    if (copied) setTimeout(() => (copied = false), 1500);
  }
</script>

<div class="copy">
  <input readonly {value} onfocus={(e) => (e.currentTarget as HTMLInputElement).select()} />
  <button onclick={copy}><Icon name="copy" size={16} />{copied ? 'Copied' : 'Copy'}</button>
</div>

<style>
  .copy {
    display: flex;
    gap: 0.4rem;
  }
  input {
    flex: 1;
    font-family: ui-monospace, monospace;
    font-size: 0.85rem;
  }
</style>
