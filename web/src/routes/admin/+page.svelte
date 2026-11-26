<script lang="ts">
  import './admin.css';
  import TokensCard from '#lib/components/TokensCard.svelte';
  import AuditTab from './AuditTab.svelte';
  import InvitesTab from './InvitesTab.svelte';
  import SessionsTab from './SessionsTab.svelte';
  import UsersTab from './UsersTab.svelte';

  // Each tab is its own component and loads what it shows when it is opened.
  const tabs = ['users', 'sessions', 'invites', 'tokens', 'audit'] as const;
  let tab = $state<(typeof tabs)[number]>('users');
</script>

<svelte:head><title>Admin</title></svelte:head>

<div class="page admin">
  <h1>Administration</h1>
  <div class="tabs">
    {#each tabs as t (t)}
      <button class:active={tab === t} onclick={() => (tab = t)}>
        {t[0].toUpperCase() + t.slice(1)}
      </button>
    {/each}
  </div>

  {#if tab === 'users'}
    <UsersTab />
  {:else if tab === 'sessions'}
    <SessionsTab />
  {:else if tab === 'invites'}
    <InvitesTab />
  {:else if tab === 'tokens'}
    <TokensCard admin />
  {:else}
    <AuditTab />
  {/if}
</div>
