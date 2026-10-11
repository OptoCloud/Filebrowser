/// <reference types="vitest/config" />
import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [
    // SvelteKit 3 takes its configuration here (svelte.config.js is no longer read).
    sveltekit({
      preprocess: vitePreprocess(),
      // Single-page app embedded into the Go binary; index.html is the fallback for every route.
      adapter: adapter({ pages: 'build', assets: 'build', fallback: 'index.html', strict: true }),
      // Absolute asset URLs: the fallback page is served at arbitrary depths.
      paths: { relative: false },
    }),
  ],
  server: {
    // Dev: proxy the API to a locally running Go server (see README).
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false },
      '/_upload': { target: 'http://127.0.0.1:8080', changeOrigin: false },
    },
  },
  test: {
    // A test that runs no assertions fails instead of silently passing.
    expect: { requireAssertions: true },
    include: ['src/**/*.{test,spec}.{js,ts}'],
  },
});
