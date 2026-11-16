<script lang="ts">
  import { formatSize } from '#lib/format.js';

  // A 3D model (Wavefront OBJ or Autodesk FBX) on an orbitable stage. The
  // format comes from the server's check of the file's content, not its name.
  let { url, mime, size }: { url: string; mime: string; size: number } = $props();

  /** Larger models are not loaded into the page. */
  const MAX = 150 * 1024 * 1024;

  let canvas = $state<HTMLCanvasElement>();
  let status = $state('Loading model…');
  let failed = $state('');
  let wire = $state(false);
  let setWire: (on: boolean) => void = () => {};
  let resetView: () => void = () => {};

  $effect(() => setWire(wire));

  $effect(() => {
    const [u, m, el] = [url, mime, canvas];
    if (!el) return;
    failed = '';
    status = 'Loading model…';
    if (size > MAX) {
      failed = `This model is too large to preview (${formatSize(size)}).`;
      return;
    }
    const ctrl = new AbortController();
    let dispose = () => {};
    (async () => {
      const [THREE, { OrbitControls }] = await Promise.all([
        import('three'),
        import('three/examples/jsm/controls/OrbitControls.js'),
      ]);
      const res = await fetch(u, { credentials: 'same-origin', signal: ctrl.signal });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.arrayBuffer();
      status = 'Preparing model…';

      // Models may name textures and materials in other files. None are
      // fetched: every such reference resolves to an empty resource.
      const manager = new THREE.LoadingManager();
      manager.setURLModifier(() => 'data:,');
      let object: import('three').Object3D;
      if (m === 'model/obj') {
        const { OBJLoader } = await import('three/examples/jsm/loaders/OBJLoader.js');
        object = new OBJLoader(manager).parse(new TextDecoder().decode(data));
      } else {
        const { FBXLoader } = await import('three/examples/jsm/loaders/FBXLoader.js');
        object = new FBXLoader(manager).parse(data, '');
      }
      if (ctrl.signal.aborted) return;

      const renderer = new THREE.WebGLRenderer({ canvas: el, antialias: true, alpha: true });
      renderer.setPixelRatio(Math.min(devicePixelRatio, 2));
      const scene = new THREE.Scene();
      scene.add(new THREE.HemisphereLight(0xffffff, 0x555566, 2.2));
      const key = new THREE.DirectionalLight(0xffffff, 1.6);
      scene.add(key);

      const fallback = new THREE.MeshStandardMaterial({
        color: 0x9aa7b8,
        roughness: 0.75,
        metalness: 0.05,
      });
      // eslint-disable-next-line svelte/prefer-svelte-reactivity -- three.js bookkeeping, not UI state
      const materials = new Set<import('three').Material>([fallback]);
      let triangles = 0;
      object.traverse((o) => {
        const mesh = o as import('three').Mesh;
        if (!mesh.isMesh) return;
        const g = mesh.geometry;
        if (!g.attributes.normal) g.computeVertexNormals();
        triangles += (g.index ? g.index.count : g.attributes.position.count) / 3;
        // OBJ without a material library gets one plain material; FBX keeps its own.
        if (m === 'model/obj') mesh.material = fallback;
        for (const mat of Array.isArray(mesh.material) ? mesh.material : [mesh.material])
          materials.add(mat);
      });
      scene.add(object);

      const box = new THREE.Box3().setFromObject(object);
      const sphere = box.getBoundingSphere(new THREE.Sphere());
      const radius = sphere.radius || 1;
      const camera = new THREE.PerspectiveCamera(45, 1, radius / 100, radius * 100);
      const controls = new OrbitControls(camera, el);
      controls.enableDamping = true;
      const home = () => {
        camera.position
          .copy(sphere.center)
          .add(new THREE.Vector3(1, 0.7, 1).normalize().multiplyScalar(radius * 2.6));
        controls.target.copy(sphere.center);
        controls.update();
      };
      home();
      resetView = home;
      setWire = (on) => {
        for (const mat of materials) (mat as import('three').MeshStandardMaterial).wireframe = on;
      };
      setWire(wire);

      const fit = () => {
        const w = el.clientWidth || 1;
        const h = el.clientHeight || 1;
        renderer.setSize(w, h, false);
        camera.aspect = w / h;
        camera.updateProjectionMatrix();
      };
      const ro = new ResizeObserver(fit);
      ro.observe(el);
      fit();
      renderer.setAnimationLoop(() => {
        controls.update();
        key.position.copy(camera.position);
        renderer.render(scene, camera);
      });
      status = `${Math.round(triangles).toLocaleString()} triangles`;

      dispose = () => {
        ro.disconnect();
        renderer.setAnimationLoop(null);
        controls.dispose();
        object.traverse((o) => (o as import('three').Mesh).geometry?.dispose());
        for (const mat of materials) mat.dispose();
        renderer.dispose();
        setWire = resetView = () => {};
      };
      if (ctrl.signal.aborted) dispose();
    })().catch((x) => {
      if (!ctrl.signal.aborted)
        failed = 'This model could not be displayed.' + (x?.message ? ` (${x.message})` : '');
    });
    return () => {
      ctrl.abort();
      dispose();
    };
  });
</script>

<div class="model">
  {#if failed}
    <p class="muted msg">{failed}</p>
  {:else}
    <canvas bind:this={canvas} aria-label="3D model. Drag to rotate, scroll to zoom."></canvas>
    <div class="bar">
      <span class="small">{status}</span>
      <label class="small"><input type="checkbox" bind:checked={wire} /> Wireframe</label>
      <button class="small" onclick={() => resetView()}>Reset view</button>
    </div>
  {/if}
</div>

<style>
  .model {
    position: relative;
    width: 100%;
    height: 100%;
    min-height: 0;
    border-radius: var(--radius);
    overflow: hidden;
    background: radial-gradient(circle at 50% 40%, #3a4250, #15181d);
    color: #e6e9ee;
  }
  canvas {
    width: 100%;
    height: 100%;
    display: block;
    touch-action: none;
    cursor: grab;
  }
  .bar {
    position: absolute;
    left: 0.5rem;
    bottom: 0.5rem;
    display: flex;
    gap: 0.75rem;
    align-items: center;
    background: rgba(0, 0, 0, 0.55);
    border-radius: var(--radius);
    padding: 0.3rem 0.6rem;
  }
  .msg {
    padding: 2rem;
    text-align: center;
    color: #bbb;
  }
</style>
