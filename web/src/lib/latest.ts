/**
 * "Latest request wins": every next() aborts the signal handed out by the
 * previous call, so a slow response can never overwrite the result of a newer
 * request. Callers pass the signal to fetch (to cancel the network request
 * too) and check `signal.aborted` after every await before touching state.
 */
export class LatestRequest {
  private ctrl: AbortController | null = null;

  /** Starts a new request, superseding the previous one. */
  next(): AbortSignal {
    this.ctrl?.abort();
    this.ctrl = new AbortController();
    return this.ctrl.signal;
  }

  /** Aborts the current request, if any (e.g. on teardown). */
  abort() {
    this.ctrl?.abort();
    this.ctrl = null;
  }
}
