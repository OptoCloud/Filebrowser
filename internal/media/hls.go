package media

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"filebrowser/internal/files"
)

// Live HLS: video is copied and audio transcoded while the file plays, in
// segments generated on demand, instead of converting the whole file before
// playback starts.
//
// Segments are cut at video keyframes, so with the video copied the playlist
// is planned from the keyframe list (Matroska cues, else a packet scan). One
// ffmpeg run per session writes fragmented MP4 to a pipe with a fragment per
// keyframe and absolute timestamps; the fragments are grouped into the planned
// segments by their timestamps. A request for a segment the run will not reach
// soon (a seek) restarts it there, and a run stops when it gets far ahead of
// playback, reaches segments that already exist, or nobody fetches any more.
const (
	hlsTarget = 6.0 // preferred segment length, seconds
	// hlsTSOffset is added to output timestamps so that B-frame decode times
	// and AAC priming at the start of the file are never negative.
	hlsTSOffset = 10.0
	hlsMaxAhead = 120.0 // seconds of media a run may produce beyond playback
	hlsSeekGap  = 20.0  // a request further ahead than this restarts the run
	hlsRefill   = 60.0  // restart a stopped run when playback gets this close to a gap
	hlsIdle     = time.Minute
	hlsTTL      = 30 * time.Minute // segments of an unused session are deleted
	hlsWait     = 45 * time.Second
)

type hlsSession struct {
	key, path, variant string
	dir                string
	spec               variantSpec
	probe              *Probe
	video              *Stream
	keyframes          []float64
	bounds             []float64 // segment start times, plus the end

	// Guarded by Service.hlsMu.
	have     []bool
	init     bool
	run      *hlsRun
	err      error
	lastReq  int
	lastUsed time.Time
	changed  chan struct{} // closed on every change
}

type hlsRun struct {
	userID int64
	start  int
	next   int // first segment not yet produced
	cancel context.CancelFunc
	done   chan struct{}
}

func (h *hlsSession) segments() int { return len(h.bounds) - 1 }

func (h *hlsSession) segPath(n int) string {
	return filepath.Join(h.dir, "s"+strconv.Itoa(n)+".m4s")
}

// broadcast wakes waiters; the caller holds hlsMu.
func (h *hlsSession) broadcast() {
	close(h.changed)
	h.changed = make(chan struct{})
}

// hlsSession returns the session for path and variant, planning it (which
// may need a keyframe scan) on first use.
func (s *Service) hlsSession(ctx context.Context, userID int64, path, variant string) (*hlsSession, error) {
	spec, err := parseVariant(variant)
	if err != nil || spec.audio < 0 {
		return nil, files.ErrBadPath
	}
	fi, err := s.fs.Stat(path)
	if err != nil {
		return nil, err
	}
	key := cacheKey(path, fi, "hls-"+variant)
	s.hlsMu.Lock()
	h := s.hls[key]
	if h != nil {
		h.lastUsed = time.Now()
	}
	s.hlsMu.Unlock()
	if h != nil {
		return h, nil
	}
	return s.hlsPlans.do(ctx, key, func(ctx context.Context) (*hlsSession, error) {
		probe, err := s.Probe(ctx, path)
		if err != nil {
			return nil, err
		}
		var video *Stream
		nth := 0
		for i := range probe.Streams {
			if st := &probe.Streams[i]; st.Type == "video" && !st.AttachedPic {
				video = st
				break
			} else if st.Type == "video" {
				nth++
			}
		}
		if video == nil || !hasStream(probe, spec.audio, "audio") {
			return nil, files.ErrBadPath
		}
		kf, err := s.keyframes(ctx, userID, path, video.Index, nth)
		if err != nil {
			return nil, err
		}
		dir := filepath.Join(s.cacheDir, "hls", key)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
		bounds := planSegments(kf, probe.Duration)
		h := &hlsSession{
			key: key, path: path, variant: variant, dir: dir, spec: spec, probe: probe, video: video,
			keyframes: kf, bounds: bounds, have: make([]bool, len(bounds)-1), lastUsed: time.Now(), changed: make(chan struct{}),
		}
		s.hlsMu.Lock()
		if old := s.hls[key]; old != nil {
			h = old
		} else {
			s.hls[key] = h
		}
		s.hlsMu.Unlock()
		return h, nil
	})
}

// planSegments groups keyframes into segments of about hlsTarget seconds and
// returns their start times followed by the end of the media.
func planSegments(kf []float64, duration float64) []float64 {
	bounds := []float64{kf[0]}
	for i := 1; i < len(kf); i++ {
		d := kf[i] - bounds[len(bounds)-1]
		next := math.Inf(1)
		if i+1 < len(kf) {
			next = kf[i+1] - bounds[len(bounds)-1]
		}
		// Cut at the target length, or earlier when the next keyframe would
		// make the segment too long.
		if d >= hlsTarget || (d >= hlsTarget/2 && next > hlsTarget*1.5) {
			bounds = append(bounds, kf[i])
		}
	}
	end := max(duration, bounds[len(bounds)-1]+0.1)
	// A final sliver joins the previous segment.
	if len(bounds) > 1 && end-bounds[len(bounds)-1] < 1 {
		bounds = bounds[:len(bounds)-1]
	}
	return append(bounds, end)
}

// keyframes returns the keyframe times of the video stream (the nth video
// stream), cached on disk.
func (s *Service) keyframes(ctx context.Context, userID int64, path string, index, nth int) ([]float64, error) {
	in, fi, err := s.open(path)
	if err != nil {
		return nil, err
	}
	defer in.File.Close()
	cache, err := s.cachePath("keyframes", cacheKey(path, fi, "keyframes-"+strconv.Itoa(index)), ".json")
	if err != nil {
		return nil, err
	}
	var kf []float64
	if b, err := os.ReadFile(cache); err == nil && json.Unmarshal(b, &kf) == nil && len(kf) > 0 { // #nosec G304 -- server cache path
		cached(cache)
		return kf, nil
	}
	if kf, err = matroskaKeyframes(in.File, nth); err != nil || !coversDuration(kf, s.probeDuration(ctx, path)) {
		// No (complete) index: read every packet of the video stream.
		release, err := s.runner.Acquire(ctx, userID)
		if err != nil {
			return nil, err
		}
		kf, err = s.scanKeyframes(ctx, path, index)
		release()
		if err != nil {
			return nil, err
		}
	}
	if len(kf) == 0 {
		return nil, fmt.Errorf("%w: no keyframes", ErrConvert)
	}
	if b, err := json.Marshal(kf); err == nil {
		_ = os.WriteFile(cache, b, 0o600)
	}
	return kf, nil
}

func (s *Service) probeDuration(ctx context.Context, path string) float64 {
	if p, err := s.Probe(ctx, path); err == nil {
		return p.Duration
	}
	return 0
}

// coversDuration rejects cue lists that leave long stretches without a
// keyframe (incomplete indexes), which would make huge segments.
func coversDuration(kf []float64, duration float64) bool {
	if len(kf) == 0 || kf[0] > 5 {
		return false
	}
	prev := kf[0]
	for _, t := range append(kf[1:], duration) {
		if t-prev > 30 {
			return false
		}
		prev = t
	}
	return true
}

// scanKeyframes lists the keyframe timestamps of a stream with ffprobe.
func (s *Service) scanKeyframes(ctx context.Context, path string, index int) ([]float64, error) {
	in, _, err := s.open(path)
	if err != nil {
		return nil, err
	}
	defer in.File.Close()
	ctx, cancel := context.WithTimeout(ctx, s.cfg.JobTimeout.Duration)
	defer cancel()
	cmd := s.runner.Command(ctx, "ffprobe", in, "", func(input string) []string {
		return append([]string{"-v", "error", "-select_streams", strconv.Itoa(index),
			"-show_entries", "packet=pts_time,flags", "-of", "csv=p=0"}, headerProbeArgs(input)...)
	})
	out, err := run(cmd)
	if err != nil {
		return nil, fmt.Errorf("%w: keyframe scan: %v", ErrConvert, err)
	}
	var kf []float64
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		ts, flags, ok := strings.Cut(sc.Text(), ",")
		if !ok || !strings.HasPrefix(flags, "K") {
			continue
		}
		if t, err := strconv.ParseFloat(ts, 64); err == nil {
			kf = append(kf, t)
		}
	}
	sort.Float64s(kf)
	return kf, nil
}

// HLSPlaylist returns the media playlist. segURL and initURL give the
// (relative) URLs of the segments and the initialization section.
func (s *Service) HLSPlaylist(ctx context.Context, userID int64, path, variant string, initURL string, segURL func(int) string) ([]byte, error) {
	h, err := s.hlsSession(ctx, userID, path, variant)
	if err != nil {
		return nil, err
	}
	longest := 0.0
	for i := range h.segments() {
		longest = max(longest, h.bounds[i+1]-h.bounds[i])
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:%d\n#EXT-X-PLAYLIST-TYPE:VOD\n"+
		"#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-INDEPENDENT-SEGMENTS\n#EXT-X-MAP:URI=%q\n", int(math.Ceil(longest)), initURL)
	for i := range h.segments() {
		fmt.Fprintf(&b, "#EXTINF:%.6f,\n%s\n", h.bounds[i+1]-h.bounds[i], segURL(i))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.Bytes(), nil
}

// HLSInit returns the initialization section (ftyp and moov).
func (s *Service) HLSInit(ctx context.Context, userID int64, path, variant string) (*os.File, error) {
	h, err := s.hlsSession(ctx, userID, path, variant)
	if err != nil {
		return nil, err
	}
	return s.hlsWait(ctx, h, userID, -1)
}

// HLSSegment returns media segment n, generating it if needed.
func (s *Service) HLSSegment(ctx context.Context, userID int64, path, variant string, n int) (*os.File, error) {
	h, err := s.hlsSession(ctx, userID, path, variant)
	if err != nil {
		return nil, err
	}
	if n < 0 || n >= h.segments() {
		return nil, files.ErrNotFound
	}
	return s.hlsWait(ctx, h, userID, n)
}

// hlsWait waits for segment n (or the init section, n < 0) to exist,
// starting or redirecting the run as needed.
func (s *Service) hlsWait(ctx context.Context, h *hlsSession, userID int64, n int) (*os.File, error) {
	timeout := time.NewTimer(hlsWait)
	defer timeout.Stop()
	started := false
	s.hlsMu.Lock()
	h.lastUsed = time.Now()
	if n >= 0 {
		h.lastReq = n
	}
	for {
		if n < 0 && h.init {
			s.hlsMu.Unlock()
			return os.Open(filepath.Join(h.dir, "init.mp4")) // #nosec G304 -- server cache path
		}
		if n >= 0 && h.have[n] {
			s.refill(h, userID, n)
			s.hlsMu.Unlock()
			return os.Open(h.segPath(n)) // #nosec G304 -- server cache path
		}
		if started && h.run == nil && h.err != nil {
			err := h.err
			s.hlsMu.Unlock()
			return nil, err
		}
		want := max(n, 0)
		if n < 0 {
			want = h.lastReq
		}
		if r := h.run; r == nil || (n >= 0 && (n < r.start || h.bounds[n]-h.bounds[min(r.next, n)] > hlsSeekGap)) {
			s.startRun(h, userID, firstMissing(h, want))
			started = true
		}
		ch := h.changed
		s.hlsMu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout.C:
			return nil, ErrBusy
		}
		s.hlsMu.Lock()
	}
}

func firstMissing(h *hlsSession, from int) int {
	for i := from; i < len(h.have); i++ {
		if !h.have[i] {
			return i
		}
	}
	return from
}

// refill restarts a stopped run when playback approaches the next missing
// segment. The caller holds hlsMu.
func (s *Service) refill(h *hlsSession, userID int64, n int) {
	if h.run != nil {
		return
	}
	m := firstMissing(h, n)
	if !h.have[m] && h.bounds[m]-h.bounds[n] < hlsRefill {
		s.startRun(h, userID, m)
	}
}

// startRun replaces the session's run with one starting at segment start.
// It also stops the user's runs for other streams: a user watches one at a
// time (switching audio tracks starts another), and they hold the user's job
// slots. The caller holds hlsMu.
func (s *Service) startRun(h *hlsSession, userID int64, start int) {
	var old []*hlsRun
	for _, o := range s.hls {
		if r := o.run; r != nil && (o == h || r.userID == userID) {
			r.cancel()
			old = append(old, r)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.JobTimeout.Duration)
	r := &hlsRun{userID: userID, start: start, next: start, cancel: cancel, done: make(chan struct{})}
	h.run, h.err = r, nil
	go func() { // #nosec G118 -- the run outlives the request that started it
		defer close(r.done)
		defer cancel()
		var err error
		for _, o := range old {
			<-o.done // their job slots must be released first
		}
		if ctx.Err() == nil {
			_, err = guard(func() (struct{}, error) { return struct{}{}, s.hlsRun(ctx, h, r, userID) })
		}
		s.hlsMu.Lock()
		if h.run == r {
			h.run = nil
			if err != nil && !errors.Is(err, errStopRun) {
				h.err = err
				slog.Error("hls run failed", "path", h.path, "variant", h.variant, "segment", r.start, "err", err)
			}
		}
		h.broadcast()
		s.hlsMu.Unlock()
	}()
}

var errStopRun = errors.New("run no longer needed")

func (s *Service) hlsRun(ctx context.Context, h *hlsSession, r *hlsRun, userID int64) error {
	release, err := s.runner.Acquire(ctx, userID)
	if err != nil {
		if ctx.Err() != nil {
			return errStopRun
		}
		return err
	}
	defer release()
	in, _, err := s.open(h.path)
	if err != nil {
		return err
	}
	defer in.File.Close()
	// -seek_timestamp: -ss is a timestamp, as are the keyframe times; the
	// demuxer seeks to a keyframe at or before it, and earlier fragments are
	// dropped below.
	ss := strconv.FormatFloat(h.bounds[r.start]+0.001, 'f', 6, 64)
	cmd := s.runner.Command(ctx, "ffmpeg", in, "", func(input string) []string {
		args := ffmpegArgs(input, "-copyts", "-seek_timestamp", "1", "-ss", ss)
		// Stereo: AAC encoding must stay well ahead of playback on slow CPUs,
		// and ffmpeg's encoder is several times slower for 5.1.
		args = append(args, streamArgs(h.probe, h.video, h.spec, 2)...)
		return append(args, "-sn", "-dn", "-map_chapters", "-1", "-map_metadata", "-1",
			"-avoid_negative_ts", "disabled", "-output_ts_offset", strconv.FormatFloat(hlsTSOffset, 'f', -1, 64),
			"-use_editlist", "0", "-f", "mp4",
			"-movflags", "+frag_keyframe+empty_moov+default_base_moof+frag_discont", "pipe:1")
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
	waited := false
	// wait is called at the end of the output: a killed ffmpeg also ends it
	// at a box boundary, so the last segment is complete only if ffmpeg
	// succeeded.
	wait := func() error {
		waited = true
		if err := cmd.Wait(); err != nil {
			if ctx.Err() != nil {
				return errStopRun
			}
			return fmt.Errorf("%w: ffmpeg: %v: %s", ErrConvert, err, stderr.buf.String())
		}
		return nil
	}
	err = s.hlsSplit(h, r, bufio.NewReaderSize(stdout, 1<<20), wait)
	if !waited {
		r.cancel() // stop ffmpeg; its exit status no longer matters
		_ = cmd.Wait()
	}
	if err != nil && ctx.Err() != nil {
		return errStopRun
	}
	return err
}

// hlsSplit reads ffmpeg's fragmented MP4 output and writes the planned
// segments. It returns errStopRun when the run should stop early; at the end
// of the output it calls wait before keeping the last segment.
func (s *Service) hlsSplit(h *hlsSession, r *hlsRun, out io.Reader, wait func() error) error {
	ftyp, err := readBox(out, 1<<10)
	if err != nil || ftyp.typ != "ftyp" {
		return errBadMP4
	}
	moov, err := readBox(out, 4<<20)
	if err != nil || moov.typ != "moov" {
		return errBadMP4
	}
	track, timescale, err := mp4VideoTrack(moov.raw)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrConvert, err)
	}
	s.hlsMu.Lock()
	needInit := !h.init
	s.hlsMu.Unlock()
	if needInit {
		// Every run writes the same initialization section.
		if err := writeAtomic(filepath.Join(h.dir, "init.mp4"), append(ftyp.raw, moov.raw...)); err != nil {
			return err
		}
		s.hlsMu.Lock()
		h.init = true
		h.broadcast()
		s.hlsMu.Unlock()
	}

	cur := -1 // segment being written, -1 while skipping pre-roll
	var f *os.File
	defer func() {
		if f != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	finish := func() error {
		if f == nil {
			return nil
		}
		name := f.Name()
		err := f.Close()
		f = nil
		if err == nil {
			err = os.Rename(name, h.segPath(cur))
		}
		if err != nil {
			_ = os.Remove(name)
			return err
		}
		s.hlsMu.Lock()
		defer s.hlsMu.Unlock()
		h.have[cur] = true
		r.next = cur + 1
		h.broadcast()
		return nil
	}
	for {
		box, err := readBox(out, 64<<20)
		if errors.Is(err, io.EOF) {
			if err := wait(); err != nil {
				return err
			}
			return finish()
		} else if err != nil {
			return err
		}
		if box.typ == "moof" {
			if ticks, ok := moofVideoPTS(box.raw, track); ok {
				t := snapKeyframe(h.keyframes, float64(ticks)/float64(timescale)-hlsTSOffset)
				i := segmentAt(h.bounds, t)
				if i != cur {
					if err := finish(); err != nil {
						return err
					}
					if i < r.start {
						cur = -1
					} else {
						if err := s.hlsContinue(h, i); err != nil {
							return err
						}
						cur = i
						if f, err = os.CreateTemp(h.dir, "s"+strconv.Itoa(i)+"-*.tmp"); err != nil {
							return err
						}
					}
				}
			}
		}
		if f == nil || (box.typ != "moof" && box.typ != "mdat") {
			if box.typ == "mdat" {
				if _, err := io.CopyN(io.Discard, out, box.size); err != nil {
					return errBadMP4
				}
			}
			continue
		}
		if _, err := f.Write(box.raw); err != nil {
			return err
		}
		if box.typ == "mdat" {
			if _, err := io.CopyN(f, out, box.size); err != nil {
				return err
			}
		}
	}
}

// hlsContinue decides whether the run should go on to produce segment i.
func (s *Service) hlsContinue(h *hlsSession, i int) error {
	s.hlsMu.Lock()
	defer s.hlsMu.Unlock()
	switch {
	case h.have[i]:
		return errStopRun // produced by an earlier run
	case h.bounds[i]-h.bounds[h.lastReq] > hlsMaxAhead:
		return errStopRun // far enough ahead of playback; refill restarts it
	case time.Since(h.lastUsed) > hlsIdle:
		return errStopRun // nobody is watching
	}
	return nil
}

// snapKeyframe returns the keyframe time nearest to t, if close. Demuxers
// can disagree by a few milliseconds (ffprobe's packet scan and ffmpeg read
// MP4 edit lists differently), which must not move a fragment across a
// segment boundary.
func snapKeyframe(kf []float64, t float64) float64 {
	best, dist := t, 0.05
	i := sort.SearchFloat64s(kf, t)
	for _, j := range []int{i - 1, i} {
		if j >= 0 && j < len(kf) && math.Abs(kf[j]-t) <= dist {
			best, dist = kf[j], math.Abs(kf[j]-t)
		}
	}
	return best
}

// segmentAt returns the segment containing time t.
func segmentAt(bounds []float64, t float64) int {
	const eps = 0.005 // keyframe times are exact to the millisecond
	i := sort.Search(len(bounds)-1, func(i int) bool { return bounds[i] > t+eps }) - 1
	return max(i, 0)
}

func writeAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), path)
}

// evictHLS stops idle runs and deletes sessions unused for hlsTTL.
func (s *Service) evictHLS() {
	s.hlsMu.Lock()
	defer s.hlsMu.Unlock()
	for k, h := range s.hls {
		if time.Since(h.lastUsed) > hlsTTL {
			if h.run != nil {
				h.run.cancel()
				continue // deleted next time, once the run has exited
			}
			delete(s.hls, k)
			_ = os.RemoveAll(h.dir)
		}
	}
}
