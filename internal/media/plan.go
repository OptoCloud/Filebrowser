package media

import (
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Caps are the client's playback capabilities, reported by the browser.
type Caps struct {
	Containers map[string]bool
	Video      map[string]bool
	Audio      map[string]bool
}

// ParseCaps parses "c:mp4,c:webm,v:h264,a:aac,..." as sent by the frontend.
func ParseCaps(s string) Caps {
	c := Caps{Containers: map[string]bool{}, Video: map[string]bool{}, Audio: map[string]bool{}}
	for _, item := range strings.Split(s, ",") {
		kind, name, ok := strings.Cut(strings.TrimSpace(item), ":")
		if !ok || len(name) > 16 {
			continue
		}
		switch kind {
		case "c":
			c.Containers[name] = true
		case "v":
			c.Video[name] = true
		case "a":
			c.Audio[name] = true
		}
	}
	return c
}

// audioCodec normalizes ffprobe codec names to those the frontend tests for.
func audioCodec(ff string) string {
	if strings.HasPrefix(ff, "pcm_") {
		return "pcm"
	}
	return ff
}

func container(format, name string) string {
	ext := strings.ToLower(path.Ext(name))
	switch {
	case strings.Contains(format, "mp4") || strings.HasPrefix(format, "mov"):
		return "mp4"
	case format == "matroska,webm":
		if ext == ".webm" {
			return "webm"
		}
		return "mkv"
	case format == "ogg":
		return "ogg"
	case format == "flac", format == "mp3", format == "wav", format == "aac":
		return format
	}
	return format
}

// mp4Audio lists audio codecs that ffmpeg can copy into MP4 and that common
// browsers decode from MP4.
var mp4Audio = map[string]bool{"aac": true, "mp3": true, "opus": true, "flac": true, "ac3": true, "eac3": true}

// mp4Video lists video codecs that can be copied into MP4.
var mp4Video = map[string]bool{"h264": true, "hevc": true, "av1": true, "vp9": true}

type Plan struct {
	// Mode is direct, remux, transcode, external or unsupported.
	Mode      string     `json:"mode"`
	Variant   string     `json:"variant,omitempty"`
	Duration  float64    `json:"duration"`
	Video     *Stream    `json:"video,omitempty"`
	Audio     []Stream   `json:"audio"`
	AudioIdx  int        `json:"audio_index"` // -1 if none
	Subtitles []Subtitle `json:"subtitles"`
	Reason    string     `json:"reason,omitempty"`
}

type Subtitle struct {
	// Track is a stream index for embedded subs, -1 for sidecar files.
	Track    int    `json:"track"`
	Sidecar  string `json:"sidecar,omitempty"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Codec    string `json:"codec"`
}

var textSubs = map[string]bool{"subrip": true, "ass": true, "ssa": true, "webvtt": true, "mov_text": true, "text": true}

// Decide picks the cheapest way to play the file. audioIdx selects an audio
// stream by index (-1 = default track).
func Decide(p *Probe, name string, caps Caps, audioIdx int) Plan {
	plan := Plan{Duration: p.Duration, Audio: []Stream{}, Subtitles: []Subtitle{}, AudioIdx: -1}
	defaultAudio := -1
	for i := range p.Streams {
		s := p.Streams[i]
		switch s.Type {
		case "video":
			if !s.AttachedPic && plan.Video == nil {
				plan.Video = &s
			}
		case "audio":
			plan.Audio = append(plan.Audio, s)
			if defaultAudio < 0 || (s.Default && !streamDefault(p, defaultAudio)) {
				defaultAudio = s.Index
			}
		case "subtitle":
			if textSubs[s.Codec] {
				plan.Subtitles = append(plan.Subtitles, Subtitle{Track: s.Index, Language: s.Language, Title: s.Title, Codec: s.Codec})
			}
		}
	}
	firstAudio := -1
	if len(plan.Audio) > 0 {
		firstAudio = plan.Audio[0].Index
	}
	plan.AudioIdx = defaultAudio
	if audioIdx >= 0 {
		for _, a := range plan.Audio {
			if a.Index == audioIdx {
				plan.AudioIdx = audioIdx
			}
		}
	}
	var audio *Stream
	for i := range plan.Audio {
		if plan.Audio[i].Index == plan.AudioIdx {
			audio = &plan.Audio[i]
		}
	}
	if plan.Video == nil && audio == nil {
		plan.Mode, plan.Reason = "unsupported", "no audio or video streams"
		return plan
	}

	cont := container(p.Format, name)
	vOK := plan.Video == nil || caps.Video[plan.Video.Codec]
	aOK := audio == nil || caps.Audio[audioCodec(audio.Codec)]
	// Browsers play the first audio track; other tracks need a remux.
	if caps.Containers[cont] && vOK && aOK && plan.AudioIdx == firstAudio {
		plan.Mode = "direct"
		return plan
	}
	if plan.Video != nil && !(vOK && mp4Video[plan.Video.Codec]) {
		plan.Mode, plan.Reason = "external", fmt.Sprintf("video codec %s is not supported by this browser", plan.Video.Codec)
		return plan
	}
	if !caps.Containers["mp4"] {
		plan.Mode, plan.Reason = "external", "browser cannot play MP4"
		return plan
	}
	aIdx := "n"
	if audio != nil {
		aIdx = strconv.Itoa(audio.Index)
	}
	if audio == nil || (aOK && mp4Audio[audioCodec(audio.Codec)]) {
		plan.Mode, plan.Variant = "remux", "remux-a"+aIdx
		return plan
	}
	if !caps.Audio["aac"] {
		plan.Mode, plan.Reason = "external", "browser cannot play AAC"
		return plan
	}
	plan.Mode, plan.Variant = "transcode", "aac-a"+aIdx
	return plan
}

func streamDefault(p *Probe, idx int) bool {
	for _, s := range p.Streams {
		if s.Index == idx {
			return s.Default
		}
	}
	return false
}

// variantSpec is a parsed variant name.
type variantSpec struct {
	transcodeAudio bool
	audio          int // -1 = none
}

func parseVariant(v string) (variantSpec, error) {
	var spec variantSpec
	var rest string
	switch {
	case strings.HasPrefix(v, "remux-a"):
		rest = strings.TrimPrefix(v, "remux-a")
	case strings.HasPrefix(v, "aac-a"):
		spec.transcodeAudio = true
		rest = strings.TrimPrefix(v, "aac-a")
	default:
		return spec, fmt.Errorf("unknown variant %q", v)
	}
	if rest == "n" {
		if spec.transcodeAudio {
			return spec, fmt.Errorf("unknown variant %q", v)
		}
		spec.audio = -1
		return spec, nil
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 || n > 1000 || strconv.Itoa(n) != rest {
		return spec, fmt.Errorf("unknown variant %q", v)
	}
	spec.audio = n
	return spec, nil
}
