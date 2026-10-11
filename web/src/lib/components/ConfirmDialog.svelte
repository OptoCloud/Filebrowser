<script lang="ts">
  import type { Snippet } from 'svelte';
  import Modal from './Modal.svelte';

  // Confirmation for a destructive action: Cancel / <confirmLabel>.
  let {
    title,
    confirmLabel,
    onconfirm,
    onclose,
    children,
  }: {
    title: string;
    confirmLabel: string;
    onconfirm: () => void;
    onclose: () => void;
    children: Snippet;
  } = $props();
</script>

<Modal {title} {onclose}>
  {@render children()}
  {#snippet footer()}
    <button onclick={onclose}>Cancel</button>
    <button class="danger solid" onclick={onconfirm}>{confirmLabel}</button>
  {/snippet}
</Modal>
