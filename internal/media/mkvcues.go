package media

import (
	"errors"
	"io"
	"slices"
)

// Matroska element IDs (with their length markers, as written).
const (
	mkvEBML           = 0x1A45DFA3
	mkvSegment        = 0x18538067
	mkvSeekHead       = 0x114D9B74
	mkvSeek           = 0x4DBB
	mkvSeekID         = 0x53AB
	mkvSeekPosition   = 0x53AC
	mkvInfo           = 0x1549A966
	mkvTimestampScale = 0x2AD7B1
	mkvTracks         = 0x1654AE6B
	mkvTrackEntry     = 0xAE
	mkvTrackNumber    = 0xD7
	mkvTrackType      = 0x83
	mkvCues           = 0x1C53BB6B
	mkvCuePoint       = 0xBB
	mkvCueTime        = 0xB3
	mkvCueTrackPos    = 0xB7
	mkvCueTrack       = 0xF7
	mkvCluster        = 0x1F43B675
)

var errNoCues = errors.New("no usable cues")

type ebmlElem struct {
	id         uint32
	data, size int64 // offset and size of the payload; size -1 if unknown
}

// ebmlHeader reads the element header at off.
func ebmlHeader(r io.ReaderAt, off int64) (ebmlElem, error) {
	var b [12]byte
	n, err := r.ReadAt(b[:], off)
	if n < 2 {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return ebmlElem{}, err
	}
	idLen := vintLen(b[0])
	if idLen == 0 || idLen > 4 || idLen >= n {
		return ebmlElem{}, errNoCues
	}
	var id uint32
	for _, c := range b[:idLen] {
		id = id<<8 | uint32(c)
	}
	szLen := vintLen(b[idLen])
	if szLen == 0 || idLen+szLen > n {
		return ebmlElem{}, errNoCues
	}
	size := int64(b[idLen] & (0xFF >> szLen))
	allOnes := size == int64(0xFF>>szLen)
	for _, c := range b[idLen+1 : idLen+szLen] {
		size = size<<8 | int64(c)
		allOnes = allOnes && c == 0xFF
	}
	if allOnes {
		size = -1
	}
	return ebmlElem{id: id, data: off + int64(idLen+szLen), size: size}, nil
}

// vintLen is the length of a variable-size integer from its first byte.
func vintLen(b byte) int {
	for i := range 8 {
		if b&(0x80>>i) != 0 {
			return i + 1
		}
	}
	return 0
}

// ebmlChildren parses the payload of a master element read into memory.
func ebmlChildren(b []byte, fn func(id uint32, payload []byte)) {
	r := bytesReaderAt(b)
	for off := int64(0); off < int64(len(b)); {
		e, err := ebmlHeader(r, off)
		if err != nil || e.size < 0 || e.data+e.size > int64(len(b)) {
			return
		}
		fn(e.id, b[e.data:e.data+e.size])
		off = e.data + e.size
	}
}

type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func ebmlUint(b []byte) uint64 {
	if len(b) > 8 {
		return 0
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func readPayload(r io.ReaderAt, e ebmlElem, limit int64) ([]byte, error) {
	if e.size < 0 || e.size > limit {
		return nil, errNoCues
	}
	b := make([]byte, e.size)
	if _, err := r.ReadAt(b, e.data); err != nil {
		return nil, err
	}
	return b, nil
}

// matroskaKeyframes returns the cue times in seconds of the nth video track
// (counting video tracks only, from 0). Muxers write a cue for the video
// keyframes, so these are keyframe timestamps, read without scanning the
// file. It fails when the file has no cues for that track.
func matroskaKeyframes(r io.ReaderAt, nthVideo int) ([]float64, error) {
	head, err := ebmlHeader(r, 0)
	if err != nil || head.id != mkvEBML || head.size < 0 {
		return nil, errNoCues
	}
	seg, err := ebmlHeader(r, head.data+head.size)
	if err != nil || seg.id != mkvSegment {
		return nil, errNoCues
	}
	found := map[uint32][]byte{}
	positions := map[uint32]int64{}
	// Top-level elements up to the first cluster, then the rest via SeekHead.
	off := seg.data
	for range 64 {
		e, err := ebmlHeader(r, off)
		if err != nil || e.id == mkvCluster || e.size < 0 {
			break
		}
		switch e.id {
		case mkvSeekHead, mkvInfo, mkvTracks, mkvCues:
			if _, ok := found[e.id]; !ok {
				if found[e.id], err = readPayload(r, e, 64<<20); err != nil {
					return nil, errNoCues
				}
			}
		}
		if e.id == mkvSeekHead {
			ebmlChildren(found[mkvSeekHead], func(id uint32, p []byte) {
				if id != mkvSeek {
					return
				}
				var target uint32
				pos := int64(-1)
				ebmlChildren(p, func(id uint32, p []byte) {
					switch id {
					case mkvSeekID:
						if len(p) <= 4 { // element IDs are at most 4 bytes
							target = uint32(ebmlUint(p)) // #nosec G115 -- at most 4 bytes
						}
					case mkvSeekPosition:
						pos = int64(ebmlUint(p)) // #nosec G115 -- bounded by the ReadAt below
					}
				})
				if _, ok := positions[target]; !ok && pos >= 0 {
					positions[target] = pos
				}
			})
		}
		off = e.data + e.size
	}
	for _, id := range []uint32{mkvInfo, mkvTracks, mkvCues} {
		if _, ok := found[id]; ok {
			continue
		}
		pos, ok := positions[id]
		if !ok {
			return nil, errNoCues
		}
		e, err := ebmlHeader(r, seg.data+pos)
		if err != nil || e.id != id {
			return nil, errNoCues
		}
		if found[id], err = readPayload(r, e, 64<<20); err != nil {
			return nil, errNoCues
		}
	}

	scale := uint64(1_000_000) // ns per timestamp unit
	ebmlChildren(found[mkvInfo], func(id uint32, p []byte) {
		if id == mkvTimestampScale {
			if v := ebmlUint(p); v > 0 {
				scale = v
			}
		}
	})
	track, n := uint64(0), 0
	ebmlChildren(found[mkvTracks], func(id uint32, p []byte) {
		if id != mkvTrackEntry || track != 0 {
			return
		}
		var num, typ uint64
		ebmlChildren(p, func(id uint32, p []byte) {
			switch id {
			case mkvTrackNumber:
				num = ebmlUint(p)
			case mkvTrackType:
				typ = ebmlUint(p)
			}
		})
		if typ == 1 { // video
			if n == nthVideo {
				track = num
			}
			n++
		}
	})
	if track == 0 {
		return nil, errNoCues
	}
	var times []float64
	ebmlChildren(found[mkvCues], func(id uint32, p []byte) {
		if id != mkvCuePoint {
			return
		}
		var t uint64
		ours := false
		ebmlChildren(p, func(id uint32, p []byte) {
			switch id {
			case mkvCueTime:
				t = ebmlUint(p)
			case mkvCueTrackPos:
				ebmlChildren(p, func(id uint32, p []byte) {
					if id == mkvCueTrack && ebmlUint(p) == track {
						ours = true
					}
				})
			}
		})
		if ours {
			times = append(times, float64(t)*float64(scale)/1e9)
		}
	})
	if len(times) == 0 {
		return nil, errNoCues
	}
	slices.Sort(times)
	return slices.Compact(times), nil
}
