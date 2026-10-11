package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestPlanSegments(t *testing.T) {
	// Keyframes every 2 s with a scene cut at 7.5 s.
	kf := []float64{0, 2, 4, 6, 7.5, 8, 10, 12, 14, 16, 18}
	got := planSegments(kf, 19)
	want := []float64{0, 6, 12, 18, 19}
	if len(got) != len(want) {
		t.Fatalf("bounds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bounds = %v, want %v", got, want)
		}
	}
	// Sparse keyframes cut early rather than making very long segments.
	if got := planSegments([]float64{0, 4, 10, 14}, 20); got[1] != 4 {
		t.Errorf("sparse bounds = %v", got)
	}
	// A final sliver joins the previous segment.
	if got := planSegments([]float64{0, 6, 12}, 12.5); len(got) != 3 || got[2] != 12.5 {
		t.Errorf("sliver bounds = %v", got)
	}
	kf = []float64{0, 2, 6.5}
	for _, c := range [][2]float64{{0.006, 0}, {-0.083, 0}, {1.9, 2}, {2.15, 2}, {2.3, 2.3}, {6.45, 6.5}, {7, 7}} {
		if got := snapKeyframe(kf, c[0]); got != c[1] {
			t.Errorf("snapKeyframe(%v) = %v, want %v", c[0], got, c[1])
		}
	}
	b := []float64{0, 6, 12, 19}
	for _, c := range []struct {
		t    float64
		want int
	}{{-0.04, 0}, {0, 0}, {5.9, 0}, {5.999, 1}, {6, 1}, {18.5, 2}, {30, 2}} {
		if got := segmentAt(b, c.t); got != c.want {
			t.Errorf("segmentAt(%v) = %d, want %d", c.t, got, c.want)
		}
	}
}

func TestMatroskaKeyframes(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	mkv := filepath.Join(dir, "a.mkv")
	// Two video tracks: the second one's keyframes are every 3 s.
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc=duration=12:size=160x120:rate=25", "-f", "lavfi", "-i", "sine=duration=12",
		"-map", "0", "-map", "1", "-map", "0", "-c:v", "libx264", "-preset", "ultrafast",
		"-g:v:0", "50", "-g:v:1", "75", "-c:a", "ac3", mkv)
	f, err := os.Open(mkv)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for nth, want := range [][]float64{{0, 2, 4, 6, 8, 10}, {0, 3, 6, 9}} {
		kf, err := matroskaKeyframes(f, nth)
		if err != nil {
			t.Fatal(err)
		}
		if len(kf) != len(want) {
			t.Fatalf("track %d: keyframes %v, want %v", nth, kf, want)
		}
		for i := range want {
			if math.Abs(kf[i]-want[i]) > 0.001 {
				t.Fatalf("track %d: keyframes %v, want %v", nth, kf, want)
			}
		}
	}
	if _, err := matroskaKeyframes(f, 2); err == nil {
		t.Error("found a third video track")
	}
	// Not Matroska.
	mp4 := filepath.Join(dir, "a.mp4")
	ffmpeg(t, "-i", mkv, "-map", "0:0", "-c", "copy", mp4)
	g, _ := os.Open(mp4)
	defer g.Close()
	if _, err := matroskaKeyframes(g, 0); err == nil {
		t.Error("cues found in an MP4")
	}
}

func TestHLS(t *testing.T) {
	s, root := newService(t, "none")
	ctx := context.Background()
	// 40 s with B-frames, irregular keyframes (scene cuts), AC3 and AAC audio.
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc2=duration=40:size=320x240:rate=24", "-f", "lavfi", "-i", "sine=duration=40",
		"-map", "0", "-map", "1", "-map", "1",
		"-c:v", "libx264", "-preset", "veryfast", "-g", "72", "-keyint_min", "12", "-force_key_frames", "7.5,21.3",
		"-pix_fmt", "yuv420p", "-c:a:0", "ac3", "-c:a:1", "aac", filepath.Join(root, "movie.mkv"))
	// The same as MP4, which has no cues: keyframes come from a packet scan.
	ffmpeg(t, "-i", filepath.Join(root, "movie.mkv"), "-c", "copy", filepath.Join(root, "movie.mp4"))

	for _, c := range []struct{ name, variant string }{
		{"movie.mkv", "aac-a1"}, {"movie.mp4", "aac-a1"},
		{"movie.mkv", "remux-a1"}, // AC3 copied
		{"movie.mkv", "remux-a2"}, // AAC copied
		{"movie.mkv", "remux-an"}, // no audio
	} {
		name, variant := c.name, c.variant
		t.Run(name+"/"+variant, func(t *testing.T) {
			pl, err := s.HLSPlaylist(ctx, 1, name, variant, "init", func(n int) string { return "seg" + strconv.Itoa(n) })
			if err != nil {
				t.Fatal(err)
			}
			var durs []float64
			for line := range strings.SplitSeq(string(pl), "\n") {
				if v, ok := strings.CutPrefix(line, "#EXTINF:"); ok {
					d, _ := strconv.ParseFloat(strings.TrimSuffix(v, ","), 64)
					durs = append(durs, d)
				}
			}
			total := 0.0
			for _, d := range durs {
				total += d
			}
			if len(durs) < 5 || math.Abs(total-40) > 0.5 || !strings.Contains(string(pl), "#EXT-X-ENDLIST") {
				t.Fatalf("playlist:\n%s", pl)
			}

			read := func(f *os.File, err error) []byte {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				b, err := io.ReadAll(f)
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			// Seek first (a run starting mid-file), then play from the start.
			seg := map[int][]byte{}
			seg[4] = read(s.HLSSegment(ctx, 1, name, variant, 4))
			init := read(s.HLSInit(ctx, 1, name, variant))
			for i := range durs {
				if seg[i] == nil {
					seg[i] = read(s.HLSSegment(ctx, 1, name, variant, i))
				}
			}
			if _, err := s.HLSSegment(ctx, 1, name, variant, len(durs)); err == nil {
				t.Error("segment past the end")
			}

			// Each segment starts at its planned time (offset included).
			_, timescale, err := mp4VideoTrack(firstBox(t, init, "moov"))
			if err != nil {
				t.Fatal(err)
			}
			start := 0.0
			for i, d := range durs {
				moof := firstBox(t, seg[i], "moof")
				ticks, ok := moofVideoPTS(moof, 1)
				if !ok {
					t.Fatalf("segment %d has no video", i)
				}
				// Within the keyframe snapping tolerance (MP4 edit lists).
				if got := float64(ticks)/float64(timescale) - hlsTSOffset; math.Abs(got-start) > 0.1 {
					t.Errorf("segment %d starts at %.3f, want %.3f", i, got, start)
				}
				start += d
			}

			// The concatenation is one valid stream covering the whole file.
			var all bytes.Buffer
			all.Write(init)
			for i := range durs {
				all.Write(seg[i])
			}
			out := filepath.Join(t.TempDir(), "all.mp4")
			if err := os.WriteFile(out, all.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			streams := []string{"v:0", "a:0"}
			if variant == "remux-an" {
				streams = streams[:1]
			}
			for _, st := range streams {
				b, err := exec.Command("ffprobe", "-v", "error", "-select_streams", st, "-show_entries", "packet=pts_time",
					"-of", "csv=p=0", out).Output()
				if err != nil {
					t.Fatal(err)
				}
				var first, last float64
				n := 0
				for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
					v, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
					if err != nil {
						continue
					}
					if n == 0 || v < first {
						first = v
					}
					last = max(last, v)
					n++
				}
				if span := last - first; span < 39.5 || span > 40.5 {
					t.Errorf("%s spans %.2f s (%d packets)", st, span, n)
				}
				if st == "v:0" && n != 40*24 {
					t.Errorf("video has %d packets, want %d (lost or duplicated frames)", n, 40*24)
				}
			}
		})
	}
}

func firstBox(t *testing.T, b []byte, typ string) []byte {
	t.Helper()
	r := bytes.NewReader(b)
	for {
		box, err := readBox(r, 64<<20)
		if err != nil {
			t.Fatalf("no %s box", typ)
		}
		if box.typ == typ {
			return box.raw
		}
		if box.typ == "mdat" {
			_, _ = io.CopyN(io.Discard, r, box.size)
		}
	}
}

// TestHLSSandboxed streams through the platform's sandbox: ffmpeg writes to
// a pipe and needs no writable directory.
func TestHLSSandboxed(t *testing.T) {
	var sb string
	switch runtime.GOOS {
	case "linux":
		if err := exec.Command("bwrap", "--ro-bind", "/", "/", "--unshare-all", "true").Run(); err != nil {
			t.Skip("bwrap unavailable:", err)
		}
		sb = "bwrap"
	case "windows":
		sb = "appcontainer"
	default:
		t.Skip("no sandbox on " + runtime.GOOS)
	}
	s, root := newService(t, sb)
	ctx := context.Background()
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc=duration=12:size=160x120:rate=25", "-f", "lavfi", "-i", "sine=duration=12",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-c:a", "ac3", filepath.Join(root, "v.mkv"))
	pl, err := s.HLSPlaylist(ctx, 1, "v.mkv", "aac-a1", "init", func(n int) string { return "seg" + strconv.Itoa(n) })
	if err != nil {
		t.Fatal(err)
	}
	n := strings.Count(string(pl), "#EXTINF:")
	frames := 0
	for i := range n {
		f, err := s.HLSSegment(ctx, 1, "v.mkv", "aac-a1", i)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(f)
		f.Close()
		frames += sampleCount(t, b, 1)
	}
	if frames != 12*25 {
		t.Errorf("%d video frames in %d segments, want %d", frames, n, 12*25)
	}
}

// sampleCount counts the samples of a track in the moof boxes of b.
func sampleCount(t *testing.T, b []byte, track uint32) int {
	t.Helper()
	r := bytes.NewReader(b)
	n := 0
	for {
		box, err := readBox(r, 64<<20)
		if err != nil {
			return n
		}
		switch box.typ {
		case "mdat":
			_, _ = io.CopyN(io.Discard, r, box.size)
		case "moof":
			mp4Children(box.raw[8:], func(typ string, traf []byte) {
				var id uint32
				mp4Children(traf, func(typ string, b []byte) {
					switch {
					case typ == "tfhd" && len(b) >= 8:
						id = binary.BigEndian.Uint32(b[4:])
					case typ == "trun" && len(b) >= 8 && id == track:
						n += int(binary.BigEndian.Uint32(b[4:]))
					}
				})
			})
		}
	}
}
