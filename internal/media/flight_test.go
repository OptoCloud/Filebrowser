package media

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFlightsShareOneCall(t *testing.T) {
	var g flights[int]
	var calls atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			v, err := g.do(context.Background(), "k", func(context.Context) (int, error) {
				calls.Add(1)
				<-release
				return 42, nil
			})
			if v != 42 || err != nil {
				t.Errorf("do = %d, %v", v, err)
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}

func TestFlightsSurviveOneCallerLeaving(t *testing.T) {
	var g flights[string]
	started := make(chan struct{})
	release := make(chan struct{})
	fn := func(ctx context.Context) (string, error) {
		close(started)
		select {
		case <-release:
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	quitter, quit := context.WithCancel(context.Background())
	quitErr := make(chan error, 1)
	go func() { _, err := g.do(quitter, "k", fn); quitErr <- err }()
	<-started
	stayed := make(chan string, 1)
	go func() { v, _ := g.do(context.Background(), "k", fn); stayed <- v }()
	time.Sleep(20 * time.Millisecond)
	quit()
	if err := <-quitErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("leaving caller got %v", err)
	}
	close(release)
	if v := <-stayed; v != "ok" {
		t.Fatalf("remaining caller got %q; the shared call was cancelled", v)
	}
}

func TestFlightsCancelWhenEveryoneLeaves(t *testing.T) {
	var g flights[int]
	cancelled := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = g.do(ctx, "k", func(c context.Context) (int, error) {
			<-c.Done()
			close(cancelled)
			return 0, c.Err()
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("shared call not cancelled after its only caller left")
	}
	// A new caller starts a fresh call rather than joining the cancelled one.
	v, err := g.do(context.Background(), "k", func(context.Context) (int, error) { return 7, nil })
	if v != 7 || err != nil {
		t.Fatalf("fresh call = %d, %v", v, err)
	}
}
