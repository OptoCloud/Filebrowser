package media

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
)

// flights runs one shared call per key for concurrent callers, like
// singleflight, but the call's context is not tied to any one caller: it is
// cancelled only when every caller waiting for it has given up. So one client
// that disconnects does not fail the others, and work nobody waits for any
// more (thumbnails scrolled past) still stops.
type flights[T any] struct {
	mu sync.Mutex
	m  map[string]*flight[T]
}

type flight[T any] struct {
	done    chan struct{}
	val     T
	err     error
	waiters int
	cancel  context.CancelFunc
}

func (g *flights[T]) do(ctx context.Context, key string, fn func(context.Context) (T, error)) (T, error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = map[string]*flight[T]{}
	}
	f, ok := g.m[key]
	if !ok {
		fctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		f = &flight[T]{done: make(chan struct{}), cancel: cancel}
		g.m[key] = f
		go func() {
			f.val, f.err = guard(func() (T, error) { return fn(fctx) })
			cancel()
			g.forget(key, f)
			close(f.done)
		}()
	}
	f.waiters++
	g.mu.Unlock()

	select {
	case <-f.done:
		return f.val, f.err
	case <-ctx.Done():
		g.mu.Lock()
		if f.waiters--; f.waiters == 0 {
			f.cancel()
			// Later callers start afresh instead of joining a cancelled call.
			if g.m[key] == f {
				delete(g.m, key)
			}
		}
		g.mu.Unlock()
		var zero T
		return zero, ctx.Err()
	}
}

// guard runs fn, turning a panic into an error: background work has no
// request recoverer above it, and one decoder bug triggered by a hostile file
// must not take the whole server down.
func guard[T any](fn func() (T, error)) (v T, err error) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("panic in media work", "panic", p, "stack", string(debug.Stack()))
			err = fmt.Errorf("%w: internal error", ErrConvert)
		}
	}()
	return fn()
}

func (g *flights[T]) forget(key string, f *flight[T]) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m[key] == f {
		delete(g.m, key)
	}
}
