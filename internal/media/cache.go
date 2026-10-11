package media

import (
	"io/fs"
	"os"
	"time"
)

// Thumbnails, subtitles and keyframe lists are small and not tracked by size like remuxed
// variants. Using one refreshes its modification time (at most daily), and
// files unused for staleAfter are deleted; they are regenerated on demand.
const staleAfter = 60 * 24 * time.Hour

// cached reports whether the cache file p exists, marking it as used.
func cached(p string) bool {
	fi, err := os.Stat(p)
	if err != nil {
		return false
	}
	if now := time.Now(); now.Sub(fi.ModTime()) > 24*time.Hour {
		_ = os.Chtimes(p, now, now)
	}
	return true
}

// evictStale deletes thumbnails, subtitles and keyframe lists unused for staleAfter. It walks
// the cache at most once a day.
func (s *Service) evictStale() {
	s.jobsMu.Lock()
	due := time.Since(s.lastStaleEvict) > 24*time.Hour
	if due {
		s.lastStaleEvict = time.Now()
	}
	s.jobsMu.Unlock()
	if !due {
		return
	}
	root, err := os.OpenRoot(s.cacheDir)
	if err != nil {
		return
	}
	defer root.Close()
	cutoff := time.Now().Add(-staleAfter)
	for _, kind := range []string{"thumbs", "subs", "keyframes"} {
		_ = fs.WalkDir(root.FS(), kind, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if fi, err := d.Info(); err == nil && fi.ModTime().Before(cutoff) {
				_ = root.Remove(p)
			}
			return nil
		})
	}
}
