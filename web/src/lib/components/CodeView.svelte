<script lang="ts">
  import { highlight } from '#lib/highlight.js';

  // Text with syntax highlighting where the file name gives a language.
  let { name, text }: { name: string; text: string } = $props();

  let html = $state<string | null>(null);
  let wrap = $state(false);
  const lines = $derived(text.length ? text.split('\n').length - (text.endsWith('\n') ? 1 : 0) : 0);

  $effect(() => {
    const [n, t] = [name, text];
    html = null;
    let stale = false;
    highlight(n, t)
      .then((h) => {
        if (!stale) html = h;
      })
      .catch(() => {});
    return () => {
      stale = true;
    };
  });
</script>

<div class="code">
  <div class="bar">
    <span class="muted small">{lines} {lines === 1 ? 'line' : 'lines'}</span>
    <label class="small"><input type="checkbox" bind:checked={wrap} /> Wrap lines</label>
  </div>
  <div class="scroll">
    <div class="gutter" aria-hidden="true">
      {#each { length: lines }, i (i)}<span>{i + 1}</span>{/each}
    </div>
    <!-- eslint-disable-next-line svelte/no-at-html-tags -- highlight.js output: escaped text in its own spans -->
    <pre class:wrap>{#if html !== null}<code class="hljs">{@html html}</code>{:else}<code
          >{text}</code
        >{/if}</pre>
  </div>
</div>

<style>
  .code {
    width: 100%;
    height: 100%;
    display: flex;
    flex-direction: column;
    background: var(--surface);
    color: var(--text);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .bar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 0.3rem 0.75rem;
    border-bottom: 1px solid var(--border);
  }
  .scroll {
    flex: 1;
    min-height: 0;
    overflow: auto;
    display: flex;
    align-items: flex-start;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 0.82rem;
    line-height: 1.5;
  }
  .gutter {
    position: sticky;
    left: 0;
    display: flex;
    flex-direction: column;
    text-align: right;
    padding: 0.75rem 0.6rem;
    color: var(--muted);
    background: var(--surface-2);
    user-select: none;
    min-height: 100%;
  }
  pre {
    margin: 0;
    padding: 0.75rem;
    flex: 1;
    font: inherit;
    tab-size: 4;
    white-space: pre;
  }
  /* Wrapped lines no longer line up with the gutter numbers, so it is hidden. */
  pre.wrap {
    white-space: pre-wrap;
    word-break: break-word;
  }
  .scroll:has(pre.wrap) .gutter {
    display: none;
  }
  code {
    font: inherit;
  }
  /* A small theme on the app's own palette; follows light and dark. */
  .code :global(.hljs-comment),
  .code :global(.hljs-quote),
  .code :global(.hljs-meta) {
    color: var(--muted);
    font-style: italic;
  }
  .code :global(.hljs-keyword),
  .code :global(.hljs-selector-tag),
  .code :global(.hljs-literal),
  .code :global(.hljs-built_in),
  .code :global(.hljs-doctag) {
    color: light-dark(#a626a4, #c792ea);
  }
  .code :global(.hljs-string),
  .code :global(.hljs-regexp),
  .code :global(.hljs-addition),
  .code :global(.hljs-attribute) {
    color: light-dark(#2f7d32, #a5d6a7);
  }
  .code :global(.hljs-number),
  .code :global(.hljs-symbol),
  .code :global(.hljs-bullet),
  .code :global(.hljs-variable),
  .code :global(.hljs-template-variable) {
    color: light-dark(#b7600a, #f6b26b);
  }
  .code :global(.hljs-title),
  .code :global(.hljs-section),
  .code :global(.hljs-name),
  .code :global(.hljs-selector-id),
  .code :global(.hljs-selector-class) {
    color: light-dark(#1d5fd1, #82aaff);
  }
  .code :global(.hljs-type),
  .code :global(.hljs-attr),
  .code :global(.hljs-params) {
    color: light-dark(#0b7285, #7fdbca);
  }
  .code :global(.hljs-deletion) {
    color: light-dark(#c62828, #ef9a9a);
  }
  .code :global(.hljs-emphasis) {
    font-style: italic;
  }
  .code :global(.hljs-strong) {
    font-weight: 600;
  }
</style>
