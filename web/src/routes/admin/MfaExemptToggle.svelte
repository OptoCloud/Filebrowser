<script lang="ts">
  // Lifts the two-factor requirement that write access otherwise brings.
  let { checked = $bindable() }: { checked: boolean } = $props();
</script>

<label class="inline">
  <input type="checkbox" bind:checked /> Do not require two-factor authentication (even with write access)
</label>
