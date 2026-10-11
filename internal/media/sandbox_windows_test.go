package media

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppContainerSandbox(t *testing.T) {
	s, root := newService(t, "appcontainer")
	ctx := context.Background()
	if err := s.SelfTest(ctx); err != nil {
		t.Fatal(err)
	}
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc=duration=2:size=160x120:rate=10", "-c:v", "libx264", "-preset", "ultrafast",
		filepath.Join(root, "v.mp4"))
	if _, err := s.Probe(ctx, "v.mp4"); err != nil {
		t.Fatal(err)
	}
	thumb, err := s.Thumbnail(ctx, "v.mp4")
	if err != nil {
		t.Fatal(err)
	}
	checkJPEG(t, thumb, 160, 120)

	// The container can neither read files it was not handed nor write
	// outside its job directory.
	secret := filepath.Join(t.TempDir(), "secret.srt")
	if err := os.WriteFile(secret, []byte("1\n00:00:00,000 --> 00:00:01,000\nCANARY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	jobDir, done, err := s.jobDir()
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	out := filepath.Join(t.TempDir(), "escaped.wav")
	cmd := s.runner.Command(ctx, "ffmpeg", nil, jobDir, func(string) []string {
		return []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", secret, filepath.Join(jobDir, "x.vtt")}
	})
	b, err := run(cmd)
	if err == nil || strings.Contains(string(b), "CANARY") || !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("read a file outside the sandbox: %v", err)
	}
	cmd = s.runner.Command(ctx, "ffmpeg", nil, jobDir, func(string) []string {
		return []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=duration=0.1", out}
	})
	if _, err := run(cmd); err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("wrote outside the job directory: %v", err)
	}
	// No network capability.
	cmd = s.runner.Command(ctx, "ffmpeg", nil, jobDir, func(string) []string {
		return []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-rw_timeout", "5000000", "-i", "http://1.1.1.1/", "-f", "null", "-"}
	})
	if _, err := run(cmd); err == nil {
		t.Error("network reachable from the sandbox")
	} else {
		t.Log("network:", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("escaped file exists")
	}
}
