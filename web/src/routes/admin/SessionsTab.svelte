<script lang="ts">
  import { goto } from '$app/navigation';
  import { resolve } from '$app/paths';
  import { del, errorMessage, get } from '#lib/api.js';
  import { formatDate } from '#lib/format.js';
  import type { Session } from '#lib/types.js';

  // Everyone's sessions, to end one.
  let err = $state('');

  let sessions = $state<Session[]>([]);
  async function loadSessions() {
    try {
      sessions = (await get<Session[] | null>('/api/admin/sessions')) ?? [];
    } catch (e) {
      err = errorMessage(e);
    }
  }
  async function revokeSession(s: Session) {
    try {
      await del('/api/admin/sessions/' + s.id);
    } catch (x) {
      err = errorMessage(x);
    }
    if (s.current) void goto(resolve('/login'), { invalidateAll: true });
    else void loadSessions();
  }

  $effect(() => {
    void loadSessions();
  });
</script>

{#if err}<div class="error" role="alert">{err}</div>{/if}
<div class="table-wrap">
  <table class="data">
    <thead>
      <tr>
        <th>User</th><th class="hide-mobile">Client</th><th>IP</th><th class="hide-mobile">
          Method
        </th><th>Last seen</th><th></th>
      </tr>
    </thead>
    <tbody>
      {#each sessions as s (s.id)}
        <tr>
          <td>
            {s.username}
            {#if s.current}<span class="badge accent">you</span>{/if}
          </td>
          <td class="hide-mobile small muted ua">{s.user_agent}</td>
          <td class="small">{s.ip}</td>
          <td class="hide-mobile small">{s.auth_method}{s.mfa ? ' + 2FA' : ''}</td>
          <td class="small">{formatDate(s.last_seen_at)}</td>
          <td><button class="small danger" onclick={() => revokeSession(s)}>Revoke</button></td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>
