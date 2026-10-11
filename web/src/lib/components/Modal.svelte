<script lang="ts">
  import type { Snippet } from 'svelte';
  import { modal } from '#lib/dialog.js';
  import Icon from './Icon.svelte';

  let {
    title,
    onclose,
    children,
    footer,
    wide = false,
  }: {
    title: string;
    onclose: () => void;
    children: Snippet;
    footer?: Snippet;
    wide?: boolean;
  } = $props();

  const id = $props.id();
  // Close on a click on the backdrop, but not when a drag merely ends there.
  let downOnBackdrop = false;
</script>

<!-- Escape is handled by the dialog's cancel event; the backdrop click is a mouse-only shortcut. -->
<dialog
  class="modal"
  class:wide
  aria-labelledby={id}
  {@attach modal(() => onclose())}
  onpointerdown={(e) => (downOnBackdrop = e.target === e.currentTarget)}
  onclick={(e) => downOnBackdrop && e.target === e.currentTarget && onclose()}
>
  <header>
    <h3 {id}>{title}</h3>
    <button class="icon" onclick={onclose} aria-label="Close"><Icon name="close" /></button>
  </header>
  <div class="body">{@render children()}</div>
  {#if footer}<footer>{@render footer()}</footer>{/if}
</dialog>

<style>
  .modal {
    border: none;
    padding: 0;
    background: var(--surface);
    color: var(--text);
    border-radius: var(--radius);
    box-shadow: var(--shadow);
    width: calc(100% - 2rem);
    max-width: 460px;
    max-height: calc(100vh - 2rem);
  }
  .modal[open] {
    display: flex;
    flex-direction: column;
  }
  .modal::backdrop {
    background: var(--overlay, rgba(10, 12, 16, 0.55));
  }
  .modal.wide {
    max-width: 760px;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.8rem 1rem;
    border-bottom: 1px solid var(--border);
  }
  h3 {
    margin: 0;
    font-size: 1.05rem;
  }
  .body {
    padding: 1rem;
    overflow-y: auto;
  }
  footer {
    padding: 0.75rem 1rem;
    border-top: 1px solid var(--border);
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
</style>
