package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"filebrowser/internal/config"
	"filebrowser/internal/files"
	"filebrowser/internal/sandbox"
	"filebrowser/internal/store/storetest"
)

func TestMain(m *testing.M) {
	sandbox.MaybeRun() // the test binary doubles as the AppContainer launcher
	os.Exit(m.Run())
}

func TestDecide(t *testing.T) {
	chrome := ParseCaps("c:mp4,c:webm,c:ogg,c:flac,c:mp3,v:h264,v:vp9,v:av1,a:aac,a:mp3,a:opus,a:flac,a:vorbis")
	mkv := func(v, a string) *Probe {
		return &Probe{Format: "matroska,webm", Duration: 60, Streams: []Stream{
			{Index: 0, Type: "video", Codec: v}, {Index: 1, Type: "audio", Codec: a, Channels: 6, Default: true},
			{Index: 2, Type: "subtitle", Codec: "subrip", Language: "eng"}, {Index: 3, Type: "subtitle", Codec: "hdmv_pgs_subtitle"},
		}}
	}
	cases := []struct {
		name    string
		probe   *Probe
		file    string
		mode    string
		variant string
	}{
		{"mp4 h264 aac", &Probe{Format: "mov,mp4,m4a,3gp,3g2,mj2", Streams: []Stream{{Index: 0, Type: "video", Codec: "h264"}, {Index: 1, Type: "audio", Codec: "aac"}}}, "a.mp4", "direct", ""},
		{"flac", &Probe{Format: "flac", Streams: []Stream{{Index: 0, Type: "audio", Codec: "flac"}, {Index: 1, Type: "video", Codec: "mjpeg", AttachedPic: true}}}, "a.flac", "direct", ""},
		{"mkv h264 aac", mkv("h264", "aac"), "a.mkv", "remux", "remux-a1"},
		{"mkv h264 ac3", mkv("h264", "ac3"), "a.mkv", "transcode", "aac-a1"},
		{"mkv hevc", mkv("hevc", "aac"), "a.mkv", "external", ""},
		{"webm vp9 opus", mkv("vp9", "opus"), "a.webm", "direct", ""},
		{"alac m4a", &Probe{Format: "mov,mp4,m4a,3gp,3g2,mj2", Streams: []Stream{{Index: 0, Type: "audio", Codec: "alac"}}}, "a.m4a", "transcode", "aac-a0"},
	}
	for _, c := range cases {
		plan := Decide(c.probe, c.file, chrome, -1)
		if plan.Mode != c.mode || plan.Variant != c.variant {
			t.Errorf("%s: got %s/%s, want %s/%s (%s)", c.name, plan.Mode, plan.Variant, c.mode, c.variant, plan.Reason)
		}
	}
	// With HLS, video plays while it is repackaged or its audio converted;
	// audio-only files are still converted ahead.
	hls := ParseCaps("c:mp4,c:hls,v:h264,a:aac")
	for _, c := range []struct {
		probe   *Probe
		audio   int
		variant string
	}{
		{mkv("h264", "ac3"), -1, "aac-a1"},
		{mkv("h264", "aac"), -1, "remux-a1"},
		{&Probe{Format: "mov,mp4,m4a,3gp,3g2,mj2", Streams: []Stream{{Index: 0, Type: "video", Codec: "h264"},
			{Index: 1, Type: "audio", Codec: "aac"}, {Index: 2, Type: "audio", Codec: "aac"}}}, 2, "remux-a2"},
		{&Probe{Format: "matroska,webm", Streams: []Stream{{Index: 0, Type: "video", Codec: "h264"}}}, -1, "remux-an"},
	} {
		if plan := Decide(c.probe, "a.mkv", hls, c.audio); plan.Mode != "hls" || plan.Variant != c.variant {
			t.Errorf("hls %s: got %s %s", c.variant, plan.Mode, plan.Variant)
		}
	}
	if plan := Decide(cases[6].probe, "a.m4a", hls, -1); plan.Mode != "transcode" {
		t.Errorf("audio-only hls: %s", plan.Mode)
	}
	plan := Decide(mkv("h264", "aac"), "a.mkv", chrome, -1)
	if len(plan.Subtitles) != 1 || plan.Subtitles[0].Track != 2 {
		t.Errorf("subtitles = %+v (image subs must be excluded)", plan.Subtitles)
	}
	// Selecting a non-default audio track forces a remux even for direct-playable files.
	p := &Probe{Format: "mov,mp4,m4a,3gp,3g2,mj2", Streams: []Stream{{Index: 0, Type: "video", Codec: "h264"},
		{Index: 1, Type: "audio", Codec: "aac"}, {Index: 2, Type: "audio", Codec: "aac"}}}
	if plan := Decide(p, "a.mp4", chrome, 2); plan.Mode != "remux" || plan.Variant != "remux-a2" {
		t.Errorf("track switch: %s %s", plan.Mode, plan.Variant)
	}
}

func TestParseVariant(t *testing.T) {
	for _, v := range []string{"remux-a1", "aac-a0", "remux-an"} {
		if _, err := parseVariant(v); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
	for _, v := range []string{"", "remux-a", "aac-an", "remux-a01", "remux-a-1", "x", "remux-a1;rm"} {
		if _, err := parseVariant(v); err == nil {
			t.Errorf("%s accepted", v)
		}
	}
}

func TestOrientation(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	out := orient(img, 6).(*image.RGBA) // rotate 90° clockwise
	if out.Bounds().Dx() != 2 || out.Bounds().Dy() != 4 {
		t.Fatalf("bounds %v", out.Bounds())
	}
	if out.RGBAAt(1, 0).R != 255 {
		t.Fatal("top-left pixel should move to top-right")
	}
}

// newService builds a Service with real ffmpeg; it skips without ffmpeg or a database.
func newService(t *testing.T, sandbox string) (*Service, string) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	st := storetest.New(t)
	root := t.TempDir()
	fsys, err := files.OpenFS(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fsys.Close() })
	cfg := config.Default().Media
	cfg.Sandbox = sandbox
	s, err := NewService(cfg, st, fsys, root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func ffmpeg(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

func TestPipelineWithFFmpeg(t *testing.T) {
	s, root := newService(t, "none")
	ctx := context.Background()
	mkv := filepath.Join(root, "movie.mkv")
	srt := filepath.Join(root, "subs.srt")
	os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:01,500\nHello world\n"), 0o644)
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc=duration=3:size=320x240:rate=25", "-f", "lavfi", "-i", "sine=duration=3",
		"-i", srt, "-map", "0", "-map", "1", "-map", "2", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "ac3", "-c:s", "srt", mkv)

	p, err := s.Probe(ctx, "movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if p.Duration < 2.5 || len(p.Streams) != 3 {
		t.Fatalf("probe = %+v", p)
	}
	// Cached on second call.
	if _, err := s.Probe(ctx, "movie.mkv"); err != nil {
		t.Fatal(err)
	}
	plan := Decide(p, "movie.mkv", ParseCaps("c:mp4,v:h264,a:aac"), -1)
	if plan.Mode != "transcode" || plan.Variant != "aac-a1" {
		t.Fatalf("plan = %+v", plan)
	}
	job, err := s.Prepare(ctx, 1, "movie.mkv", plan.Variant)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for job.State != "done" {
		if job.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("job = %+v", job)
		}
		time.Sleep(100 * time.Millisecond)
		if job, err = s.JobStatus(ctx, "movie.mkv", plan.Variant); err != nil {
			t.Fatal(err)
		}
	}
	f, err := s.OpenVariant(ctx, "movie.mkv", plan.Variant)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := s.Probe(ctx, files.Join("", "movie.mkv"))
	if err != nil || out == nil {
		t.Fatal(err)
	}

	vtt, err := s.Subtitle(ctx, "movie.mkv", 2)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(vtt); !bytes.Contains(b, []byte("WEBVTT")) || !bytes.Contains(b, []byte("Hello world")) {
		t.Fatalf("vtt = %q", b)
	}
	if _, err := s.Subtitle(ctx, "subs.srt", -1); err != nil {
		t.Fatal(err)
	}

	thumb, err := s.Thumbnail(ctx, "movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	checkJPEG(t, thumb, 320, 240)

	// Pure-Go image thumbnail.
	big := image.NewRGBA(image.Rect(0, 0, 1600, 800))
	var buf bytes.Buffer
	jpeg.Encode(&buf, big, nil)
	os.WriteFile(filepath.Join(root, "photo.jpg"), buf.Bytes(), 0o644)
	thumb, err = s.Thumbnail(ctx, "photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	checkJPEG(t, thumb, thumbSize, thumbSize/2)

	// Non-media files have no thumbnail; corrupt images fail cleanly.
	os.WriteFile(filepath.Join(root, "bad.png"), []byte("not a png"), 0o644)
	if _, err := s.Thumbnail(ctx, "bad.png"); err != ErrNoThumbnail {
		t.Fatalf("bad png: %v", err)
	}
	if _, err := s.Thumbnail(ctx, "subs.srt"); err != ErrNoThumbnail {
		t.Fatalf("srt: %v", err)
	}
}

func checkJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := jpeg.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != w || cfg.Height != h {
		t.Fatalf("thumbnail %dx%d, want %dx%d", cfg.Width, cfg.Height, w, h)
	}
}

func TestRejectsPlaylistInputs(t *testing.T) {
	s, root := newService(t, "none")
	os.WriteFile(filepath.Join(root, "secret.txt"), []byte("TOP SECRET"), 0o644)
	// An HLS playlist and a concat script disguised as videos must not be
	// opened with demuxers that read other files.
	os.WriteFile(filepath.Join(root, "evil.mkv"), []byte("#EXTM3U\n#EXTINF:1,\nsecret.txt\n#EXT-X-ENDLIST\n"), 0o644)
	os.WriteFile(filepath.Join(root, "evil2.mkv"), []byte("ffconcat version 1.0\nfile secret.txt\n"), 0o644)
	for _, name := range []string{"evil.mkv", "evil2.mkv"} {
		if p, err := s.Probe(context.Background(), name); err == nil {
			t.Errorf("%s probed as %+v", name, p)
		}
	}
}

func TestHasInputProtocol(t *testing.T) {
	list := "Supported file protocols:\nInput:\n  async\n  fd\n  file\nOutput:\n  fd\n  file\n"
	if !hasInputProtocol(list, "fd") || hasInputProtocol("Input:\n  file\nOutput:\n  fd\n", "fd") {
		t.Fatal("hasInputProtocol")
	}
}

func TestDecoderWhitelist(t *testing.T) {
	s, root := newService(t, "none")
	ctx := context.Background()
	// PNG-coded video: ffprobe still reports the codec, but ffmpeg must refuse
	// to decode it.
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc=duration=1:size=64x48:rate=5", "-c:v", "png", filepath.Join(root, "png.mkv"))
	p, err := s.Probe(ctx, "png.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Streams) != 1 || p.Streams[0].Codec != "png" {
		t.Fatalf("probe = %+v", p)
	}
	if _, err := s.Thumbnail(ctx, "png.mkv"); err == nil {
		t.Fatal("decoded a codec outside the whitelist")
	}
	// A normal video with an extra non-whitelisted stream still probes and
	// thumbnails; only the extra stream is never decoded.
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc=duration=2:size=64x48:rate=5", "-f", "lavfi", "-i", "testsrc=duration=1:size=32x24:rate=5",
		"-map", "0", "-map", "1", "-c:v:0", "libx264", "-preset", "ultrafast", "-c:v:1", "png", filepath.Join(root, "mixed.mkv"))
	if p, err := s.Probe(ctx, "mixed.mkv"); err != nil || len(p.Streams) != 2 || p.Streams[0].Codec != "h264" || p.Streams[0].Width != 64 {
		t.Fatalf("mixed probe = %+v, %v", p, err)
	}
	thumb, err := s.Thumbnail(ctx, "mixed.mkv")
	if err != nil {
		t.Fatal(err)
	}
	checkJPEG(t, thumb, 64, 48)
}

func TestBwrapSandbox(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bwrap is Linux-only")
	}
	if err := exec.Command("bwrap", "--ro-bind", "/", "/", "--unshare-all", "true").Run(); err != nil {
		t.Skip("bwrap unavailable:", err)
	}
	s, root := newService(t, "bwrap")
	if err := s.SelfTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc=duration=2:size=160x120:rate=10", "-c:v", "libx264", "-preset", "ultrafast",
		filepath.Join(root, "v.mkv"))
	if _, err := s.Probe(context.Background(), "v.mkv"); err != nil {
		t.Fatal(err)
	}
	thumb, err := s.Thumbnail(context.Background(), "v.mkv")
	if err != nil {
		t.Fatal(err)
	}
	checkJPEG(t, thumb, 160, 120)
}
