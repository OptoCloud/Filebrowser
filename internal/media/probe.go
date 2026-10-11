package media

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"filebrowser/internal/store"
)

// Probe is the subset of ffprobe output we keep.
type Probe struct {
	Format   string   `json:"format"` // ffprobe format_name, e.g. "matroska,webm"
	Duration float64  `json:"duration"`
	BitRate  int64    `json:"bit_rate"`
	Streams  []Stream `json:"streams"`
}

type Stream struct {
	Index       int    `json:"index"`
	Type        string `json:"type"` // video, audio, subtitle, attachment, data
	Codec       string `json:"codec"`
	Profile     string `json:"profile,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Channels    int    `json:"channels,omitempty"`
	Language    string `json:"language,omitempty"`
	Title       string `json:"title,omitempty"`
	Default     bool   `json:"default,omitempty"`
	AttachedPic bool   `json:"attached_pic,omitempty"`
}

type ffprobeOut struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
	Streams []struct {
		Index       int               `json:"index"`
		CodecType   string            `json:"codec_type"`
		CodecName   string            `json:"codec_name"`
		Profile     string            `json:"profile"`
		Width       int               `json:"width"`
		Height      int               `json:"height"`
		Channels    int               `json:"channels"`
		Tags        map[string]string `json:"tags"`
		Disposition map[string]int    `json:"disposition"`
	} `json:"streams"`
}

func parseFFprobe(raw []byte) (*Probe, error) {
	var out ffprobeOut
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	p := &Probe{Format: out.Format.FormatName}
	p.Duration, _ = strconv.ParseFloat(out.Format.Duration, 64)
	p.BitRate, _ = strconv.ParseInt(out.Format.BitRate, 10, 64)
	for _, s := range out.Streams {
		tag := func(k string) string {
			for tk, v := range s.Tags {
				if strings.EqualFold(tk, k) {
					return v
				}
			}
			return ""
		}
		p.Streams = append(p.Streams, Stream{
			Index: s.Index, Type: s.CodecType, Codec: s.CodecName, Profile: s.Profile,
			Width: s.Width, Height: s.Height, Channels: s.Channels,
			Language: tag("language"), Title: tag("title"),
			Default: s.Disposition["default"] == 1, AttachedPic: s.Disposition["attached_pic"] == 1,
		})
	}
	return p, nil
}

// Probe returns cached probe data for path, running ffprobe if needed.
func (s *Service) Probe(ctx context.Context, path string) (*Probe, error) {
	in, fi, err := s.open(path)
	if err != nil {
		return nil, err
	}
	defer in.File.Close()
	size, mtime := fi.Size(), fi.ModTime().UnixNano()
	if raw, err := s.store.Probe(ctx, path, size, mtime); err == nil {
		var p Probe
		if json.Unmarshal(raw, &p) == nil {
			return &p, nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	v, err, _ := s.probeSF.Do(path+"|"+strconv.FormatInt(mtime, 10), func() (any, error) {
		release, err := s.runner.AcquireLight(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		pctx, cancel := context.WithTimeout(ctx, s.cfg.ProbeTimeout.Duration)
		defer cancel()
		// ffprobe aborts when a stream's decoder is not whitelisted, so first
		// read only the container headers (no frames are decoded), and decode
		// frames for the full probe only when every stream may be decoded.
		p, err := s.ffprobe(pctx, path, false)
		if err != nil {
			return nil, err
		}
		if decodable(p) {
			if p, err = s.ffprobe(pctx, path, true); err != nil {
				return nil, err
			}
		}
		raw, _ := json.Marshal(p)
		if err := s.store.PutProbe(context.WithoutCancel(ctx), path, size, mtime, raw); err != nil {
			return nil, err
		}
		return p, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*Probe), nil
}

// ffprobe runs one probe pass. The header pass reads stream parameters from
// the container only; the full pass also decodes frames (whitelisted codecs).
func (s *Service) ffprobe(ctx context.Context, path string, full bool) (*Probe, error) {
	in, _, err := s.open(path)
	if err != nil {
		return nil, err
	}
	defer in.File.Close()
	cmd := s.runner.Command(ctx, "ffprobe", in, "", func(input string) []string {
		args := []string{"-v", "error", "-print_format", "json", "-show_format", "-show_streams"}
		if full {
			return append(args, probeInputArgs(input)...)
		}
		return append(args, headerProbeArgs(input)...)
	})
	out, err := run(cmd)
	if err != nil {
		return nil, err
	}
	return parseFFprobe(out)
}

var decoderSet = func() map[string]bool {
	m := map[string]bool{"dts": true} // codec "dts" is decoded by "dca"
	for _, d := range strings.Split(decoders, ",") {
		m[d] = true
	}
	return m
}()

// decodable reports whether every audio, video and subtitle stream uses a
// whitelisted decoder.
func decodable(p *Probe) bool {
	for _, st := range p.Streams {
		switch st.Type {
		case "video", "audio", "subtitle":
			if !decoderSet[st.Codec] {
				return false
			}
		}
	}
	return true
}
