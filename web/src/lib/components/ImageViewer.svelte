<script lang="ts">
  // An image that can be zoomed (wheel, pinch, double click, + and -), dragged
  // while zoomed, and rotated. Scale 1 fits the image to the stage.
  let { src, alt }: { src: string; alt: string } = $props();

  const MIN = 1;
  const MAX = 32;
  let stage = $state<HTMLDivElement>();
  let scale = $state(1);
  let tx = $state(0);
  let ty = $state(0);
  let turns = $state(0);
  let failed = $state(false);
  let dragging = $state(false);

  // Bookkeeping for gestures, not state the markup reads.
  // eslint-disable-next-line svelte/prefer-svelte-reactivity
  const pointers = new Map<number, { x: number; y: number }>();
  let pinchDist = 0;

  function reset() {
    scale = 1;
    tx = ty = 0;
  }

  // Keeps the image from being dragged entirely out of view.
  function clamp() {
    if (!stage) return;
    const mx = (stage.clientWidth * (scale - 1)) / 2;
    const my = (stage.clientHeight * (scale - 1)) / 2;
    tx = Math.min(mx, Math.max(-mx, tx));
    ty = Math.min(my, Math.max(-my, ty));
  }

  /** Zooms by factor, keeping the stage point (cx, cy) under the cursor. */
  function zoomAt(factor: number, cx?: number, cy?: number) {
    if (!stage) return;
    const r = stage.getBoundingClientRect();
    const px = (cx ?? r.left + r.width / 2) - (r.left + r.width / 2);
    const py = (cy ?? r.top + r.height / 2) - (r.top + r.height / 2);
    const next = Math.min(MAX, Math.max(MIN, scale * factor));
    const k = next / scale;
    tx = px - (px - tx) * k;
    ty = py - (py - ty) * k;
    scale = next;
    if (scale === 1) tx = ty = 0;
    clamp();
  }

  function onwheel(e: WheelEvent) {
    e.preventDefault();
    zoomAt(Math.exp(-e.deltaY * (e.deltaMode === 1 ? 0.05 : 0.0015)), e.clientX, e.clientY);
  }

  function onpointerdown(e: PointerEvent) {
    if (e.button !== 0) return;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    dragging = true;
    if (pointers.size === 2) {
      const [a, b] = [...pointers.values()];
      pinchDist = Math.hypot(a.x - b.x, a.y - b.y);
    }
  }

  function onpointermove(e: PointerEvent) {
    const prev = pointers.get(e.pointerId);
    if (!prev) return;
    pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (pointers.size === 2) {
      const [a, b] = [...pointers.values()];
      const d = Math.hypot(a.x - b.x, a.y - b.y);
      if (pinchDist > 0) zoomAt(d / pinchDist, (a.x + b.x) / 2, (a.y + b.y) / 2);
      pinchDist = d;
    } else if (scale > 1) {
      tx += e.clientX - prev.x;
      ty += e.clientY - prev.y;
      clamp();
    }
  }

  function onpointerup(e: PointerEvent) {
    pointers.delete(e.pointerId);
    pinchDist = 0;
    dragging = pointers.size > 0;
  }

  function ondblclick(e: MouseEvent) {
    if (scale > 1) reset();
    else zoomAt(2.5, e.clientX, e.clientY);
  }

  function onkeydown(e: KeyboardEvent) {
    const t = e.target as HTMLElement | null;
    if (t && ['INPUT', 'SELECT', 'TEXTAREA'].includes(t.tagName)) return;
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.key === '+' || e.key === '=') zoomAt(1.5);
    else if (e.key === '-') zoomAt(1 / 1.5);
    else if (e.key === '0') reset();
    else if (e.key === 'r') turns++;
  }
</script>

<svelte:window {onkeydown} />

<div class="viewer">
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    class="stage"
    class:zoomed={scale > 1}
    class:dragging
    bind:this={stage}
    {onwheel}
    {onpointerdown}
    {onpointermove}
    {onpointerup}
    onpointercancel={onpointerup}
    {ondblclick}
  >
    {#if failed}
      <p class="muted">This image could not be displayed.</p>
    {:else}
      <img
        {src}
        {alt}
        draggable="false"
        onerror={() => (failed = true)}
        style:transform="translate({tx}px, {ty}px) scale({scale}) rotate({turns * 90}deg)"
        class:animate={!dragging}
      />
    {/if}
  </div>
  {#if !failed}
    <div class="tools" role="toolbar" aria-label="Image view">
      <button onclick={() => zoomAt(1 / 1.5)} disabled={scale <= MIN} aria-label="Zoom out"
        >−</button
      >
      <button class="pct" onclick={reset} title="Fit to window (0)"
        >{Math.round(scale * 100)}%</button
      >
      <button onclick={() => zoomAt(1.5)} disabled={scale >= MAX} aria-label="Zoom in">+</button>
      <button onclick={() => turns++} aria-label="Rotate" title="Rotate (r)">⟳</button>
    </div>
  {/if}
</div>

<style>
  .viewer {
    position: relative;
    width: 100%;
    height: 100%;
    min-height: 0;
  }
  .stage {
    width: 100%;
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    overflow: hidden;
    touch-action: none;
    user-select: none;
    cursor: zoom-in;
  }
  .stage.zoomed {
    cursor: grab;
  }
  .stage.dragging.zoomed {
    cursor: grabbing;
  }
  img {
    max-width: 100%;
    max-height: 100%;
    object-fit: contain;
    transform-origin: center;
    will-change: transform;
  }
  img.animate {
    transition: transform 0.12s ease-out;
  }
  .tools {
    position: absolute;
    bottom: 0.5rem;
    left: 50%;
    transform: translateX(-50%);
    display: flex;
    gap: 2px;
    background: rgba(0, 0, 0, 0.6);
    border-radius: 999px;
    padding: 2px 6px;
    z-index: 3;
  }
  .tools button {
    background: none;
    border: none;
    color: #fff;
    font-size: 1.1rem;
    min-width: 2.2rem;
    padding: 0.3rem 0.5rem;
    border-radius: 999px;
  }
  .tools button:hover:not(:disabled) {
    background: rgba(255, 255, 255, 0.18);
  }
  .tools button:disabled {
    opacity: 0.4;
  }
  .tools .pct {
    font-size: 0.85rem;
    min-width: 3.6rem;
    font-variant-numeric: tabular-nums;
  }
  @media (prefers-reduced-motion: reduce) {
    img.animate {
      transition: none;
    }
  }
</style>
