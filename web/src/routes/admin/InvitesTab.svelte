<script lang="ts">
  import { del, errorMessage, get, post } from '#lib/api.js';
  import { formatDate } from '#lib/format.js';
  import type { Invite } from '#lib/types.js';
  import CopyField from '#lib/components/CopyField.svelte';
  import Modal from '#lib/components/Modal.svelte';
  import MfaExemptToggle from './MfaExemptToggle.svelte';

  // Links with which someone makes their own account.
  let err = $state('');
  let modalErr = $state('');

  let invites = $state<Invite[]>([]);
  let inviteOpen = $state(false);
  let inv = $state({
    role: 'user',
    grantPath: '',
    grantPerm: 'read',
    withGrant: false,
    days: 7,
    mfaExempt: false,
  });
  let inviteURL = $state('');
  async function loadInvites() {
    try {
      invites = (await get<Invite[] | null>('/api/admin/invites')) ?? [];
    } catch (e) {
      err = errorMessage(e);
    }
  }
  async function createInvite(e: Event) {
    e.preventDefault();
    modalErr = '';
    try {
      const r = await post<{ invite: Invite; url: string }>('/api/admin/invites', {
        role: inv.role,
        grant_path: inv.withGrant ? inv.grantPath : undefined,
        grant_perm: inv.withGrant ? inv.grantPerm : undefined,
        expires_in: inv.days * 86400,
        mfa_exempt: inv.role !== 'admin' && inv.mfaExempt,
      });
      inviteURL = r.url;
      void loadInvites();
    } catch (x) {
      modalErr = errorMessage(x);
    }
  }
  async function deleteInvite(i: Invite) {
    try {
      await del('/api/admin/invites/' + i.id);
    } catch (x) {
      err = errorMessage(x);
    }
    void loadInvites();
  }

  $effect(() => {
    void loadInvites();
  });
</script>

{#if err}<div class="error" role="alert">{err}</div>{/if}
<div class="row bar">
  <span class="muted small">Invite links let someone create their own account.</span>
  <span class="spacer"></span>
  <button class="primary" onclick={() => ((inviteOpen = true), (inviteURL = ''), (modalErr = ''))}>
    New invite
  </button>
</div>
<div class="table-wrap">
  <table class="data">
    <thead>
      <tr>
        <th>Created</th><th>Role</th><th>Access</th><th>Expires</th><th>Status</th><th></th>
      </tr>
    </thead>
    <tbody>
      {#each invites as i (i.id)}
        <tr>
          <td class="small">{formatDate(i.created_at)}</td>
          <td>{i.role}</td>
          <td class="small">
            {i.grant_path !== null
              ? `/${i.grant_path} (${i.grant_perm})`
              : 'none'}{#if i.mfa_exempt}<span
                class="badge"
                title="Two-factor authentication will not be required"
              >
                no 2FA
              </span>{/if}
          </td>
          <td class="small">{formatDate(i.expires_at)}</td>
          <td>
            {#if i.used_at}<span class="badge">used</span>
            {:else if new Date(i.expires_at).getTime() < Date.now()}<span class="badge bad">
                expired
              </span>
            {:else}<span class="badge ok">open</span>{/if}
          </td>
          <td><button class="small danger" onclick={() => deleteInvite(i)}>Delete</button></td>
        </tr>
      {:else}
        <tr><td colspan="6" class="muted">No invites.</td></tr>
      {/each}
    </tbody>
  </table>
</div>

{#if inviteOpen}
  <Modal title="New invite" onclose={() => (inviteOpen = false)}>
    {#if inviteURL}
      <p>Send this link to the person you are inviting:</p>
      <CopyField value={inviteURL} />
      <p class="warn small">It is shown only once.</p>
    {:else}
      <form id="invite-form" onsubmit={createInvite}>
        <label class="field">
          Role
          <select bind:value={inv.role}>
            <option value="user">user</option><option value="admin">admin</option>
          </select>
        </label>
        <label class="inline">
          <input type="checkbox" bind:checked={inv.withGrant} /> Grant folder access
        </label>
        {#if inv.role !== 'admin'}
          <MfaExemptToggle bind:checked={inv.mfaExempt} />
        {/if}
        {#if inv.withGrant}
          <div class="row grantrow">
            <input
              class="gpath"
              bind:value={inv.grantPath}
              placeholder="path (empty = everything)"
            />
            <select bind:value={inv.grantPerm}>
              <option value="read">read</option><option value="write">write</option>
            </select>
          </div>
        {/if}
        <label class="field">
          Valid for (days, max 30)<input type="number" min="1" max="30" bind:value={inv.days} />
        </label>
        {#if modalErr}<div class="error" role="alert">{modalErr}</div>{/if}
      </form>
    {/if}
    {#snippet footer()}
      {#if inviteURL}
        <button class="primary" onclick={() => (inviteOpen = false)}>Done</button>
      {:else}
        <button onclick={() => (inviteOpen = false)}>Cancel</button>
        <button class="primary" type="submit" form="invite-form">Create invite</button>
      {/if}
    {/snippet}
  </Modal>
{/if}
