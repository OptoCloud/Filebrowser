<script lang="ts">
  import '../app.css';
  import { page } from '$app/state';
  import { logout } from '#lib/api.js';
  import { appUploader } from '#lib/upload.svelte.js';
  import Icon from '#lib/components/Icon.svelte';
  import UploadPanel from '#lib/components/UploadPanel.svelte';

  let { data, children } = $props();

  const me = $derived(data.me);
  const shareMode = $derived(page.url.pathname.startsWith('/s/'));
  let menuOpen = $state(false);

  function signOut() {
    appUploader.cancelAll();
    appUploader.clearFinished();
    logout();
  }

  // Closing the tab would abort running uploads (only ever started on the app origin).
  function onbeforeunload(e: BeforeUnloadEvent) {
    if (appUploader.busy) e.preventDefault();
  }

  function active(prefix: string) {
    const p = page.url.pathname;
    return p === prefix || p.startsWith(prefix + '/');
  }
</script>

{#if shareMode || !me}
  {@render children()}
{:else}
  <nav class="top">
    <a class="brand" href="/files"><Icon name="folder" size={22} /> Files</a>
    <button class="icon menu-btn" onclick={() => (menuOpen = !menuOpen)} aria-label="Menu"
      ><Icon name="menu" /></button
    >
    <div class="links" class:open={menuOpen}>
      {#if !me.restricted}
        <a href="/files" class:active={active('/files')} onclick={() => (menuOpen = false)}>Files</a
        >
        <a href="/shares" class:active={active('/shares')} onclick={() => (menuOpen = false)}
          >Shares</a
        >
        <a href="/trash" class:active={active('/trash')} onclick={() => (menuOpen = false)}>Trash</a
        >
        {#if me.user.role === 'admin'}
          <a href="/admin" class:active={active('/admin')} onclick={() => (menuOpen = false)}
            >Admin</a
          >
        {/if}
      {/if}
      <span class="spacer"></span>
      <a href="/settings" class:active={active('/settings')} onclick={() => (menuOpen = false)}
        >{me.user.username}</a
      >
      <button class="small" onclick={signOut}>Log out</button>
    </div>
  </nav>
  {@render children()}
  <UploadPanel uploader={appUploader} />
{/if}

<svelte:window {onbeforeunload} />

<style>
  .top {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.5rem 1rem;
    background: var(--surface);
    border-bottom: 1px solid var(--border);
    position: sticky;
    top: 0;
    z-index: 30;
  }
  .brand {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    font-weight: 700;
    color: var(--text);
  }
  .links {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    flex: 1;
  }
  .links a {
    color: var(--text);
    padding: 0.35rem 0.6rem;
    border-radius: var(--radius);
  }
  .links a:hover {
    background: var(--surface-2);
    text-decoration: none;
  }
  .links a.active {
    color: var(--accent);
    background: var(--accent-soft);
  }
  .menu-btn {
    display: none;
    margin-left: auto;
  }
  @media (max-width: 700px) {
    .menu-btn {
      display: inline-flex;
    }
    .top {
      flex-wrap: wrap;
    }
    .links {
      display: none;
      flex-basis: 100%;
      flex-direction: column;
      align-items: stretch;
    }
    .links.open {
      display: flex;
    }
    .links .spacer {
      display: none;
    }
  }
</style>
