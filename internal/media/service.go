package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"filebrowser/internal/config"
	"filebrowser/internal/files"
	"filebrowser/internal/store"
)

type Service struct {
	cfg       config.Media
	store     *store.Store
	fs        *files.FS
	filesRoot string
	cacheDir  string
	runner    *Runner

	probes flights[*Probe]
	thumbs flights[string]
	subs   flights[string]
	// thumbSem bounds pure-Go image decoding (memory heavy).
	thumbSem chan struct{}

	jobsMu         sync.Mutex
	jobs           map[string]*Job
	lastStaleEvict time.Time

	hlsMu    sync.Mutex
	hls      map[string]*hlsSession
	hlsPlans flights[*hlsSession]
}

func NewService(cfg config.Media, st *store.Store, fsys *files.FS, filesRoot, cacheDir string) (*Service, error) {
	abs, err := filepath.Abs(filesRoot)
	if err != nil {
		return nil, err
	}
	cacheAbs, err := filepath.Abs(cacheDir)
	if err != nil {
		return nil, err
	}
	// Leftovers of interrupted jobs and live streams.
	_ = os.RemoveAll(filepath.Join(cacheAbs, ".jobs"))
	_ = os.RemoveAll(filepath.Join(cacheAbs, "hls"))
	for _, d := range []string{"thumbs", "media", "subs", ".jobs", "hls"} {
		if err := os.MkdirAll(filepath.Join(cacheAbs, d), 0o750); err != nil {
			return nil, err
		}
	}
	return &Service{
		cfg: cfg, store: st, fs: fsys, filesRoot: abs, cacheDir: cacheAbs,
		runner:   NewRunner(cfg),
		thumbSem: make(chan struct{}, 2), // up to 80 MP each, about 320 MB decoded
		jobs:     map[string]*Job{},
		hls:      map[string]*hlsSession{},
	}, nil
}

// SelfTest runs ffmpeg through the configured sandbox once, so a sandbox that
// cannot start (for example bubblewrap in an LXC container without nesting)
// or an ffmpeg without the fd: protocol (older than 6.0) fails at startup
// instead of on every request.
func (s *Service) SelfTest(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := run(s.runner.Command(ctx, "ffmpeg", nil, "", func(string) []string {
		return []string{"-hide_banner", "-protocols"}
	}))
	if err != nil {
		return fmt.Errorf("media self-test (sandbox %q): %w", s.cfg.Sandbox, err)
	}
	if runtime.GOOS == "linux" && !hasInputProtocol(string(out), "fd") {
		return errors.New("media self-test: ffmpeg lacks the fd: protocol; ffmpeg 6.0 or newer is required")
	}
	return nil
}

func hasInputProtocol(list, name string) bool {
	in := false
	for line := range strings.SplitSeq(list, "\n") {
		switch line = strings.TrimSpace(line); line {
		case "Input:":
			in = true
		case "Output:":
			in = false
		case name:
			if in {
				return true
			}
		}
	}
	return false
}

// open opens a regular file through the confined root.
func (s *Service) open(path string) (*Input, fs.FileInfo, error) {
	f, fi, err := s.fs.OpenFile(path)
	if err != nil {
		return nil, nil, err
	}
	return &Input{File: f, Path: filepath.Join(s.filesRoot, filepath.FromSlash(path))}, fi, nil
}

func cacheKey(path string, fi fs.FileInfo, extra string) string {
	h := sha256.Sum256([]byte(path + "\x00" + strconv.FormatInt(fi.Size(), 10) + "\x00" +
		strconv.FormatInt(fi.ModTime().UnixNano(), 10) + "\x00" + extra))
	return hex.EncodeToString(h[:16])
}

// shard spreads cache files over 256 subdirectories.
func (s *Service) cachePath(kind, key, ext string) (string, error) {
	dir := filepath.Join(s.cacheDir, kind, key[:2])
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	return filepath.Join(dir, key+ext), nil
}

// Job is a remux/transcode in progress (or finished).
type Job struct {
	Path     string  `json:"path"`
	Variant  string  `json:"variant"`
	State    string  `json:"state"` // queued, running, done, failed
	Progress float64 `json:"progress"`
	Error    string  `json:"error,omitempty"`

	key      string
	userID   int64
	finished time.Time
}

// Prepare ensures the variant exists in the cache, starting a background job
// if necessary. It returns the current job state.
func (s *Service) Prepare(ctx context.Context, userID int64, path, variant string) (*Job, error) {
	spec, err := parseVariant(variant)
	if err != nil {
		return nil, files.ErrBadPath
	}
	fi, err := s.fs.Stat(path)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.MediaCache(ctx, path, fi.Size(), fi.ModTime().UnixNano(), variant); err == nil {
		return &Job{Path: path, Variant: variant, State: "done", Progress: 1}, nil
	}
	key := cacheKey(path, fi, variant)
	if j := s.job(key); j != nil && j.State != "failed" {
		return j, nil
	}
	probe, err := s.Probe(ctx, path)
	if err != nil {
		return nil, err
	}
	if spec.audio >= 0 && !hasStream(probe, spec.audio, "audio") {
		return nil, files.ErrBadPath
	}
	in, fi, err := s.open(path)
	if err != nil {
		return nil, err
	}

	s.jobsMu.Lock()
	if j, ok := s.jobs[key]; ok && j.State != "failed" {
		snap := *j
		s.jobsMu.Unlock()
		_ = in.File.Close()
		return &snap, nil
	}
	active := 0
	for _, j := range s.jobs {
		if j.userID == userID && (j.State == "queued" || j.State == "running") {
			active++
		}
	}
	if active >= max(s.cfg.MaxJobsPerUser, 1) {
		s.jobsMu.Unlock()
		_ = in.File.Close()
		return nil, ErrBusy
	}
	j := &Job{Path: path, Variant: variant, State: "queued", key: key, userID: userID}
	s.jobs[key] = j
	snap := *j
	s.jobsMu.Unlock()

	go func() { // #nosec G118 -- conversion jobs intentionally outlive the request
		defer in.File.Close()
		release, err := s.runner.Acquire(context.Background(), userID)
		if err == nil {
			s.setState(j, "running")
			_, err = guard(func() (struct{}, error) { return struct{}{}, s.runJob(j, in, fi, probe, spec) })
			release()
		}
		s.jobsMu.Lock()
		j.finished = time.Now()
		if err != nil {
			j.State, j.Error = "failed", "conversion failed"
			slog.Error("media job failed", "path", path, "variant", variant, "err", err)
		} else {
			j.State, j.Progress = "done", 1
		}
		s.jobsMu.Unlock()
		s.Evict(context.Background())
	}()
	return &snap, nil
}

func (s *Service) job(key string) *Job {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if j, ok := s.jobs[key]; ok {
		snap := *j
		return &snap
	}
	return nil
}

func (s *Service) setState(j *Job, state string) {
	s.jobsMu.Lock()
	j.State = state
	s.jobsMu.Unlock()
}

func hasStream(p *Probe, idx int, typ string) bool {
	for _, st := range p.Streams {
		if st.Index == idx && st.Type == typ {
			return true
		}
	}
	return false
}

// JobStatus returns the state of a job started by Prepare.
func (s *Service) JobStatus(ctx context.Context, path, variant string) (*Job, error) {
	fi, err := s.fs.Stat(path)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.MediaCache(ctx, path, fi.Size(), fi.ModTime().UnixNano(), variant); err == nil {
		return &Job{Path: path, Variant: variant, State: "done", Progress: 1}, nil
	}
	if j := s.job(cacheKey(path, fi, variant)); j != nil {
		return j, nil
	}
	return nil, files.ErrNotFound
}

func (s *Service) runJob(j *Job, in *Input, fi fs.FileInfo, probe *Probe, spec variantSpec) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.JobTimeout.Duration)
	defer cancel()
	out, err := s.cachePath("media", j.key, ".mp4")
	if err != nil {
		return err
	}
	jobDir, done, err := s.jobDir()
	if err != nil {
		return err
	}
	defer done()
	tmp := filepath.Join(jobDir, "out.mp4")

	var video *Stream
	for i := range probe.Streams {
		if st := probe.Streams[i]; st.Type == "video" && !st.AttachedPic {
			video = &probe.Streams[i]
			break
		}
	}
	cmd := s.runner.Command(ctx, "ffmpeg", in, jobDir, func(input string) []string {
		args := append(ffmpegArgs(input), streamArgs(probe, video, spec, 6)...)
		return append(args, "-sn", "-dn", "-map_chapters", "-1", "-movflags", "+faststart",
			"-f", "mp4", "-progress", "pipe:1", "-nostats", tmp)
	})
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr limitedBuffer
	stderr.max, stderr.buf = 8<<10, new(bytesBuffer)
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	trackProgress(stdout, probe.Duration, func(p float64) {
		s.jobsMu.Lock()
		j.Progress = p
		s.jobsMu.Unlock()
	})
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, stderr.buf.String())
	}
	if err := publish(tmp, out, nil); err != nil {
		return err
	}
	st, err := os.Stat(out)
	if err != nil {
		return err
	}
	old, err := s.store.PutMediaCache(ctx, &store.MediaCacheEntry{
		SourcePath: j.Path, SourceSize: fi.Size(), SourceMtime: fi.ModTime().UnixNano(), Variant: j.Variant,
		CachePath: out, Bytes: st.Size(), LastUsedAt: time.Now(),
	})
	if old != "" {
		_ = os.Remove(old)
	}
	return err
}

// streamArgs maps the video stream (copied, or none) and the variant's audio
// stream (copied, or transcoded to AAC with at most maxChannels channels).
func streamArgs(probe *Probe, video *Stream, spec variantSpec, maxChannels int) []string {
	var args []string
	if video != nil {
		args = append(args, "-map", "0:"+strconv.Itoa(video.Index), "-c:v", "copy")
		if video.Codec == "hevc" {
			args = append(args, "-tag:v", "hvc1") // required by Safari
		}
	} else {
		args = append(args, "-vn")
	}
	if spec.audio >= 0 {
		args = append(args, "-map", "0:"+strconv.Itoa(spec.audio))
		if spec.transcodeAudio {
			ch := 2
			for _, st := range probe.Streams {
				if st.Index == spec.audio && st.Channels > 2 {
					ch = min(st.Channels, maxChannels)
				}
			}
			args = append(args, "-c:a", "aac", "-ac", strconv.Itoa(ch), "-b:a", strconv.Itoa(96*ch)+"k")
		} else {
			args = append(args, "-c:a", "copy")
		}
	}
	return args
}

// OpenVariant opens a cached variant for streaming.
func (s *Service) OpenVariant(ctx context.Context, path, variant string) (*os.File, error) {
	if _, err := parseVariant(variant); err != nil {
		return nil, files.ErrBadPath
	}
	fi, err := s.fs.Stat(path)
	if err != nil {
		return nil, err
	}
	e, err := s.store.MediaCache(ctx, path, fi.Size(), fi.ModTime().UnixNano(), variant)
	if errors.Is(err, store.ErrNotFound) {
		return nil, files.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	f, err := os.Open(e.CachePath)
	if err != nil {
		_ = s.store.DeleteMediaCache(ctx, path, variant)
		return nil, files.ErrNotFound
	}
	if time.Since(e.LastUsedAt) > time.Minute {
		_ = s.store.TouchMediaCache(ctx, path, variant, time.Now())
	}
	return f, nil
}

// Evict deletes least recently used variants until the cache fits its cap,
// stale thumbnails and subtitles, and forgets finished job records.
func (s *Service) Evict(ctx context.Context) {
	s.evictStale()
	s.evictHLS()
	s.jobsMu.Lock()
	for k, j := range s.jobs {
		if !j.finished.IsZero() && time.Since(j.finished) > 10*time.Minute {
			delete(s.jobs, k)
		}
	}
	s.jobsMu.Unlock()
	if s.cfg.CacheMaxBytes <= 0 {
		return
	}
	total, err := s.store.MediaCacheTotal(ctx)
	if err != nil {
		return
	}
	for total > s.cfg.CacheMaxBytes {
		lru, err := s.store.MediaCacheLRU(ctx, 50)
		if err != nil || len(lru) == 0 {
			return
		}
		for _, e := range lru {
			if total <= s.cfg.CacheMaxBytes {
				return
			}
			_ = os.Remove(e.CachePath)
			if err := s.store.DeleteMediaCache(ctx, e.SourcePath, e.Variant); err != nil {
				return
			}
			total -= e.Bytes
		}
	}
}

// jobDir creates a private scratch directory for one ffmpeg run. Only this
// directory is writable inside the sandbox; results are validated and moved
// into the shared cache by publish, so a compromised ffmpeg cannot read or
// overwrite other cache entries.
func (s *Service) jobDir() (string, func(), error) {
	dir, err := os.MkdirTemp(filepath.Join(s.cacheDir, ".jobs"), "job")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// publish checks a job output and moves it into the cache.
func publish(tmp, out string, check func(*os.File) error) error {
	fi, err := os.Lstat(tmp)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Size() == 0 {
		return fmt.Errorf("%w: invalid output", ErrConvert)
	}
	if check != nil {
		f, err := os.Open(tmp) // #nosec G304 -- path inside the server's job dir
		if err != nil {
			return err
		}
		err = check(f)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrConvert, err)
		}
	}
	return os.Rename(tmp, out)
}
