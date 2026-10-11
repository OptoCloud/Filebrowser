package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"filebrowser/internal/files"
)

// ErrConvert reports that ffmpeg could not convert the input.
var ErrConvert = errors.New("media conversion failed")

// IsSidecar reports whether name has a subtitle file extension.
func IsSidecar(name string) bool {
	_, ok := sidecarExts[strings.ToLower(path.Ext(name))]
	return ok
}

var sidecarExts = map[string]string{".srt": "subrip", ".vtt": "webvtt", ".ass": "ass", ".ssa": "ssa"}

// Sidecars finds subtitle files next to a video: "Movie.srt", "Movie.en.srt", ...
func (s *Service) Sidecars(a *files.Access, videoPath string) []Subtitle {
	dir := files.Parent(videoPath)
	stem := strings.TrimSuffix(files.Base(videoPath), path.Ext(videoPath))
	entries, _, err := s.fs.List(a, dir)
	if err != nil {
		return nil
	}
	var out []Subtitle
	for _, e := range entries {
		ext := strings.ToLower(path.Ext(e.Name))
		codec, ok := sidecarExts[ext]
		if e.IsDir || !ok || !strings.HasPrefix(e.Name, stem+".") {
			continue
		}
		lang := strings.TrimPrefix(strings.TrimSuffix(e.Name, path.Ext(e.Name)), stem)
		lang = strings.TrimPrefix(lang, ".")
		out = append(out, Subtitle{Track: -1, Sidecar: e.Name, Language: lang, Codec: codec})
	}
	return out
}

// Subtitle returns a cached WebVTT file for an embedded track (track >= 0)
// or a sidecar subtitle file at subPath.
func (s *Service) Subtitle(ctx context.Context, srcPath string, track int) (string, error) {
	fi, err := s.fs.Stat(srcPath)
	if err != nil {
		return "", err
	}
	if fi.IsDir() || fi.Size() > 4<<30 {
		return "", files.ErrBadPath
	}
	if track < 0 && fi.Size() > 20<<20 {
		return "", files.ErrBadPath
	}
	if track >= 0 {
		p, err := s.Probe(ctx, srcPath)
		if err != nil {
			return "", err
		}
		if !hasStream(p, track, "subtitle") {
			return "", files.ErrNotFound
		}
	}
	key := cacheKey(srcPath, fi, "sub"+strconv.Itoa(track))
	out, err := s.cachePath("subs", key, ".vtt")
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	_, err, _ = s.subSF.Do(key, func() (any, error) {
		release, err := s.runner.AcquireLight(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		in, _, err := s.open(srcPath)
		if err != nil {
			return nil, err
		}
		defer in.File.Close()
		tctx, cancel := context.WithTimeout(ctx, s.cfg.ProbeTimeout.Duration*4)
		defer cancel()
		jobDir, done, err := s.jobDir()
		if err != nil {
			return nil, err
		}
		defer done()
		tmp := filepath.Join(jobDir, "out.vtt")
		cmd := s.runner.Command(tctx, "ffmpeg", in, jobDir, func(input string) []string {
			args := append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}, inputArgs(input)...)
			if track >= 0 {
				args = append(args, "-map", "0:"+strconv.Itoa(track))
			}
			return append(args, "-c:s", "webvtt", "-f", "webvtt", tmp)
		})
		if _, err := run(cmd); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrConvert, err)
		}
		return nil, publish(tmp, out, func(f *os.File) error {
			head := make([]byte, 6)
			if _, err := io.ReadFull(f, head); err != nil || string(head) != "WEBVTT" {
				return errors.New("not WebVTT")
			}
			return nil
		})
	})
	return out, err
}
