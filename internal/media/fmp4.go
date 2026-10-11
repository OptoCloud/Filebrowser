package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

var errBadMP4 = fmt.Errorf("%w: malformed fragmented MP4", ErrConvert)

// mp4Box is a top-level box read from ffmpeg's fragmented MP4 output. The
// payload of an mdat is not read: body is nil and size says how much follows.
type mp4Box struct {
	typ  string
	raw  []byte // header and body (nil for mdat)
	size int64  // payload bytes still to read (mdat only)
}

// readBox reads the next top-level box, keeping at most limit bytes in memory.
func readBox(r io.Reader, limit int64) (mp4Box, error) {
	var h [16]byte
	if _, err := io.ReadFull(r, h[:8]); err != nil {
		return mp4Box{}, err // io.EOF at a box boundary is the normal end
	}
	size := int64(binary.BigEndian.Uint32(h[:4]))
	typ := string(h[4:8])
	hdr := int64(8)
	if size == 1 {
		if _, err := io.ReadFull(r, h[8:16]); err != nil {
			return mp4Box{}, errBadMP4
		}
		size = int64(binary.BigEndian.Uint64(h[8:16])) // #nosec G115 -- checked below
		hdr = 16
	}
	if size < hdr {
		return mp4Box{}, errBadMP4
	}
	if typ == "mdat" {
		return mp4Box{typ: typ, raw: append([]byte(nil), h[:hdr]...), size: size - hdr}, nil
	}
	if size > limit {
		return mp4Box{}, errBadMP4
	}
	raw := make([]byte, size)
	copy(raw, h[:hdr])
	if _, err := io.ReadFull(r, raw[hdr:]); err != nil {
		return mp4Box{}, errBadMP4
	}
	return mp4Box{typ: typ, raw: raw}, nil
}

// mp4Children calls fn for each child box in b (a box body).
func mp4Children(b []byte, fn func(typ string, body []byte)) {
	for len(b) >= 8 {
		size := uint64(binary.BigEndian.Uint32(b))
		hdr := uint64(8)
		if size == 1 && len(b) >= 16 {
			size, hdr = binary.BigEndian.Uint64(b[8:]), 16
		}
		if size < hdr || size > uint64(len(b)) {
			return
		}
		fn(string(b[4:8]), b[hdr:size])
		b = b[size:]
	}
}

// mp4VideoTrack returns the ID and timescale of the video track in a moov box.
func mp4VideoTrack(moov []byte) (id, timescale uint32, err error) {
	mp4Children(moov[8:], func(typ string, trak []byte) {
		if typ != "trak" || id != 0 {
			return
		}
		var tid, scale uint32
		video := false
		mp4Children(trak, func(typ string, b []byte) {
			switch typ {
			case "tkhd":
				// version 0: flags, ctime, mtime (4 each), track_ID; version 1: 8-byte times.
				if len(b) >= 24 && b[0] == 1 {
					tid = binary.BigEndian.Uint32(b[20:])
				} else if len(b) >= 16 {
					tid = binary.BigEndian.Uint32(b[12:])
				}
			case "mdia":
				mp4Children(b, func(typ string, b []byte) {
					switch typ {
					case "mdhd":
						if len(b) >= 24 && b[0] == 1 {
							scale = binary.BigEndian.Uint32(b[20:])
						} else if len(b) >= 16 {
							scale = binary.BigEndian.Uint32(b[12:])
						}
					case "hdlr":
						video = len(b) >= 12 && string(b[8:12]) == "vide"
					}
				})
			}
		})
		if video && tid != 0 && scale != 0 {
			id, timescale = tid, scale
		}
	})
	if id == 0 {
		return 0, 0, errors.New("no video track in output")
	}
	return id, timescale, nil
}

// moofVideoPTS returns the presentation time, in track units, of the first
// sample of the given track in a moof box: tfdt plus the first composition
// offset (ffmpeg writes no edit list with -use_editlist 0).
func moofVideoPTS(moof []byte, track uint32) (int64, bool) {
	var pts int64
	found := false
	mp4Children(moof[8:], func(typ string, traf []byte) {
		if typ != "traf" || found {
			return
		}
		var id uint32
		var dts int64
		var cto int64
		haveDTS := false
		mp4Children(traf, func(typ string, b []byte) {
			switch typ {
			case "tfhd":
				if len(b) >= 8 {
					id = binary.BigEndian.Uint32(b[4:])
				}
			case "tfdt":
				if len(b) >= 12 && b[0] == 1 {
					dts, haveDTS = int64(binary.BigEndian.Uint64(b[4:])), true // #nosec G115 -- ffmpeg output, offset keeps it positive
				} else if len(b) >= 8 {
					dts, haveDTS = int64(binary.BigEndian.Uint32(b[4:])), true
				}
			case "trun":
				if len(b) < 8 || binary.BigEndian.Uint32(b[4:]) == 0 {
					return
				}
				flags := binary.BigEndian.Uint32(b) & 0xFFFFFF
				p := 8
				for _, f := range []uint32{0x1, 0x4} { // data offset, first sample flags
					if flags&f != 0 {
						p += 4
					}
				}
				for _, f := range []uint32{0x100, 0x200, 0x400} { // first sample's duration, size, flags
					if flags&f != 0 {
						p += 4
					}
				}
				if flags&0x800 != 0 && len(b) >= p+4 {
					v := binary.BigEndian.Uint32(b[p:])
					if b[0] == 1 {
						cto = int64(int32(v)) // #nosec G115 -- signed in version 1
					} else {
						cto = int64(v)
					}
				}
			}
		})
		if id == track && haveDTS {
			pts, found = dts+cto, true
		}
	})
	return pts, found
}
