package media

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStaleCacheEviction(t *testing.T) {
	dir := t.TempDir()
	s := &Service{cacheDir: dir}
	old, fresh, used := filepath.Join(dir, "thumbs", "ab", "old.jpg"), filepath.Join(dir, "subs", "cd", "fresh.vtt"),
		filepath.Join(dir, "thumbs", "ef", "used.jpg")
	longAgo := time.Now().Add(-staleAfter - time.Hour)
	for _, p := range []string{old, fresh, used} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	os.Chtimes(old, longAgo, longAgo)
	os.Chtimes(used, longAgo, longAgo)
	if !cached(used) {
		t.Fatal("cached(existing) = false")
	}
	if cached(filepath.Join(dir, "thumbs", "missing.jpg")) {
		t.Fatal("cached(missing) = true")
	}
	s.evictStale()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("stale file kept")
	}
	for _, p := range []string{fresh, used} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s evicted: %v", filepath.Base(p), err)
		}
	}
	// At most one sweep a day.
	os.WriteFile(old, []byte("x"), 0o644)
	os.Chtimes(old, longAgo, longAgo)
	s.evictStale()
	if _, err := os.Stat(old); err != nil {
		t.Error("second sweep ran within a day")
	}
}
