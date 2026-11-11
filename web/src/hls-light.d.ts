// hls.js publishes no types for its `light` entry point; its API is the full
// build's minus features we do not use (EME/DRM, alternate audio, in-stream
// subtitles, CMCD), so reuse those declarations.
declare module 'hls.js/light' {
  export * from 'hls.js';
  export { default } from 'hls.js';
}
