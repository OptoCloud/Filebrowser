<script lang="ts">
  import { errorMessage, get, qs } from '#lib/api.js';
  import { formatDate } from '#lib/format.js';
  import type { AuditEntry } from '#lib/types.js';

  // The audit log, newest first, a page at a time.
  let err = $state('');

  let audit = $state<AuditEntry[]>([]);
  let auditFilter = $state('');
  let auditMore = $state(false);
  const AUDIT_PAGE = 100;
  async function loadAudit(more = false) {
    try {
      const before = more && audit.length ? audit[audit.length - 1].id : undefined;
      const list =
        (await get<AuditEntry[] | null>(
          '/api/admin/audit' + qs({ before, action: auditFilter || undefined, limit: AUDIT_PAGE })
        )) ?? [];
      audit = more ? [...audit, ...list] : list;
      auditMore = list.length === AUDIT_PAGE;
    } catch (e) {
      err = errorMessage(e);
    }
  }

  function detailsText(d: unknown): string {
    if (d === null || d === undefined) return '';
    const s = JSON.stringify(d);
    return s.length > 200 ? s.slice(0, 200) + '…' : s;
  }
  $effect(() => {
    void loadAudit();
  });
</script>

{#if err}<div class="error" role="alert">{err}</div>{/if}
<form class="row bar" onsubmit={(e) => (e.preventDefault(), loadAudit())}>
  <input
    bind:value={auditFilter}
    placeholder="Filter by action prefix (e.g. auth., share.)"
    class="filter"
  />
  <button>Filter</button>
</form>
<div class="table-wrap">
  <table class="data">
    <thead>
      <tr>
        <th>Time</th><th>User</th><th>Action</th><th>Target</th><th class="hide-mobile"> IP </th><th
          class="hide-mobile"
        >
          Details
        </th>
      </tr>
    </thead>
    <tbody>
      {#each audit as a (a.id)}
        <tr>
          <td class="small nowrap">{formatDate(a.ts)}</td>
          <td class="small">{a.username ?? ''}</td>
          <td class="small mono">{a.action}</td>
          <td class="small wrap target">{a.target}</td>
          <td class="small hide-mobile">{a.ip}</td>
          <td class="small mono hide-mobile wrap muted">{detailsText(a.details)}</td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>
{#if auditMore}<p><button onclick={() => loadAudit(true)}>Load older</button></p>{/if}
