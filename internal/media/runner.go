// Package media runs ffmpeg/ffprobe in a sandbox to probe, thumbnail, remux
// and transcode media.
package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"

	"filebrowser/internal/config"
	"filebrowser/internal/sandbox"
)

var ErrBusy = errors.New("too many media jobs")

// Runner executes media tools with resource limits and concurrency caps.
type Runner struct {
	cfg    config.Media
	self   string // this executable, for the AppContainer launcher
	global chan struct{}
	// light bounds short tasks (probe, thumbnails, subtitles), which do not
	// count against the per-user job limit.
	light chan struct{}

	mu      sync.Mutex
	perUser map[int64]int
}

func NewRunner(cfg config.Media) *Runner {
	self, _ := os.Executable()
	return &Runner{cfg: cfg, self: self, global: make(chan struct{}, max(cfg.MaxJobs, 1)), light: make(chan struct{}, 4), perUser: map[int64]int{}}
}

// AcquireLight reserves a slot for a short task.
func (r *Runner) AcquireLight(ctx context.Context) (func(), error) {
	select {
	case r.light <- struct{}{}:
		return func() { <-r.light }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Acquire reserves a job slot for userID, waiting for the global slot. It
// fails immediately if the user already has too many jobs.
func (r *Runner) Acquire(ctx context.Context, userID int64) (func(), error) {
	r.mu.Lock()
	if r.perUser[userID] >= max(r.cfg.MaxJobsPerUser, 1) {
		r.mu.Unlock()
		return nil, ErrBusy
	}
	r.perUser[userID]++
	r.mu.Unlock()
	release := func() {
		r.mu.Lock()
		if r.perUser[userID]--; r.perUser[userID] <= 0 {
			delete(r.perUser, userID)
		}
		r.mu.Unlock()
	}
	select {
	case r.global <- struct{}{}:
		return func() { <-r.global; release() }, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

// Input is a media file opened through os.Root. On Linux it is handed to
// the tool as an inherited file descriptor so the subprocess cannot be
// pointed at anything outside files_root.
type Input struct {
	File *os.File
	Path string // absolute path, used where fd passing is unavailable
}

// Command builds a sandboxed command. writeDir (may be "") is the only
// writable directory besides a private /tmp.
func (r *Runner) Command(ctx context.Context, tool string, in *Input, writeDir string, args func(input string) []string) *exec.Cmd {
	bin := r.cfg.FFmpeg
	if tool == "ffprobe" {
		bin = r.cfg.FFprobe
	}
	inputArg := ""
	var extra []*os.File
	var stdin *os.File
	if in != nil {
		inputArg, extra, stdin = inputSpec(in)
	}
	var argv []string
	argv = append(argv, r.cfg.Wrapper...)
	switch r.cfg.Sandbox {
	case "bwrap":
		argv = append(argv, bwrapArgs(bin, writeDir)...)
	case "appcontainer":
		// This executable in launcher mode (see internal/sandbox). The launcher
		// runs with a minimal PATH, so resolve the tool here.
		if p, err := exec.LookPath(bin); err == nil {
			bin = p
		}
		argv = append(argv, r.self)
	}
	if runtime.GOOS == "linux" && (r.cfg.MemoryLimitMB > 0 || r.cfg.CPUSeconds > 0) {
		argv = append(argv, "prlimit")
		if r.cfg.MemoryLimitMB > 0 {
			argv = append(argv, "--as="+strconv.FormatInt(int64(r.cfg.MemoryLimitMB)<<20, 10))
		}
		if r.cfg.CPUSeconds > 0 {
			argv = append(argv, "--cpu="+strconv.Itoa(r.cfg.CPUSeconds))
		}
		argv = append(argv, "--")
	}
	argv = append(argv, bin)
	argv = append(argv, args(inputArg)...)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- binaries come from config; user files are passed as fds/clean paths
	cmd.ExtraFiles = extra
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LC_ALL=C"}
	if v, ok := os.LookupEnv("SYSTEMROOT"); ok { // required on Windows
		cmd.Env = append(cmd.Env, "SYSTEMROOT="+v)
	}
	if r.cfg.Sandbox == "appcontainer" {
		cmd.Env = append(cmd.Env, sandbox.EnvExec+"=1", sandbox.EnvWrite+"="+writeDir,
			sandbox.EnvMem+"="+strconv.FormatInt(int64(r.cfg.MemoryLimitMB)<<20, 10),
			sandbox.EnvCPU+"="+strconv.Itoa(r.cfg.CPUSeconds))
	}
	setProcAttrs(cmd)
	return cmd
}

func bwrapArgs(bin, writeDir string) []string {
	a := []string{"bwrap",
		"--unshare-all", "--die-with-parent", "--new-session", "--clearenv",
		"--setenv", "PATH", "/usr/local/bin:/usr/bin:/bin",
		"--ro-bind", "/usr", "/usr",
		"--symlink", "usr/lib", "/lib",
		"--symlink", "usr/lib64", "/lib64",
		"--symlink", "usr/bin", "/bin",
		"--ro-bind-try", "/etc/ld.so.cache", "/etc/ld.so.cache",
		"--ro-bind-try", "/etc/alternatives", "/etc/alternatives",
		"--ro-bind-try", "/usr/lib/jellyfin-ffmpeg", "/usr/lib/jellyfin-ffmpeg",
		"--dev", "/dev", "--tmpfs", "/tmp",
	}
	if bin != "" && bin[0] == '/' {
		a = append(a, "--ro-bind", bin, bin)
	}
	if writeDir != "" {
		a = append(a, "--bind", writeDir, writeDir)
	}
	return append(a, "--")
}

// run executes cmd, returning stdout and a trimmed stderr in errors.
func run(cmd *exec.Cmd) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &limitedBuffer{max: 8 << 10, buf: &stderr}
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("%s: %w: %s", cmd.Args[0], err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}

type limitedBuffer struct {
	max int
	buf *bytes.Buffer
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// demuxers are the input formats ffmpeg/ffprobe may use. Playlist and
// concatenation demuxers (hls, concat, ...) are excluded because they open
// further files or URLs named inside the input.
// Image formats the Go decoders handle (JPEG, PNG, GIF, WebP, BMP) are left
// out so ffmpeg never parses them.
const demuxers = "matroska,webm,mov,mp4,m4a,3gp,3g2,mj2,avi,mpegts,mpeg,mpegvideo,flv,ogg,flac,mp3,wav,aac," +
	"asf,aiff,w64,wv,ape,dsf,amr,h264,hevc,ivf,obu,tiff_pipe,srt,ass,webvtt,subviewer,microdvd,sami"

// decoders are the codecs ffmpeg/ffprobe may decode: the mainstream audio,
// video and text-subtitle codecs. Everything else (exotic and legacy codecs,
// image subtitles, PNG cover art) is where most decoder memory-safety bugs
// are found, and is reported by ffprobe but never decoded.
const decoders = "h264,hevc,av1,libdav1d,vp8,vp9,libvpx,libvpx-vp9,mpeg1video,mpeg2video,mpeg4,msmpeg4v2,msmpeg4v3," +
	"h263,wmv1,wmv2,wmv3,vc1,theora,mjpeg,tiff," +
	"aac,aac_fixed,aac_latm,mp1,mp1float,mp2,mp2float,mp3,mp3float,opus,libopus,flac,vorbis,libvorbis," +
	"ac3,ac3_fixed,eac3,dca,truehd,mlp,alac,wavpack,ape,amrnb,amrwb,wmav1,wmav2,wmapro," +
	"pcm_s8,pcm_u8,pcm_s16le,pcm_s16be,pcm_s24le,pcm_s24be,pcm_s32le,pcm_s32be,pcm_f32le,pcm_f32be,pcm_f64le," +
	"pcm_alaw,pcm_mulaw,pcm_bluray,pcm_dvd,dsd_lsbf,dsd_msbf,dsd_lsbf_planar,dsd_msbf_planar," +
	"subrip,srt,ass,ssa,webvtt,mov_text,subviewer,microdvd,sami,text"

// fdInput is the input URL for a file passed as a descriptor (inputFD).
const fdInput = "fd:"

// inputOpts restricts how the single input may be read: only the passed fd
// (or local file in development), known demuxers only.
func inputOpts(input string) []string {
	if input == fdInput {
		return []string{"-fd", inputFD, "-protocol_whitelist", "fd", "-format_whitelist", demuxers}
	}
	return []string{"-protocol_whitelist", "file", "-format_whitelist", demuxers}
}

// inputArgs is inputOpts plus the decoder whitelist, for ffmpeg.
func inputArgs(input string) []string {
	return append(inputOpts(input), "-codec_whitelist", decoders, "-i", input)
}

// probeInputArgs is inputArgs for ffprobe, which takes the input positionally.
func probeInputArgs(input string) []string {
	return append(inputOpts(input), "-codec_whitelist", decoders, input)
}

// headerProbeArgs reads only container headers, so it needs no decoder
// whitelist (ffprobe aborts if a stream's decoder is not whitelisted).
func headerProbeArgs(input string) []string {
	return append(inputOpts(input), "-nofind_stream_info", input)
}
