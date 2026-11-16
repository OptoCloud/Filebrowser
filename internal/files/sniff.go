package files

import (
	"bytes"
	"io"
	"path"
	"strings"
	"unicode/utf8"
)

// A file name is whatever its uploader typed. Before a file is rendered or a
// preview of it is generated, its type is taken from its content: Sniff
// recognises formats by their magic bytes, and Verify reconciles that with
// the name.

// SniffLen is how much of a file Sniff wants to see.
const SniffLen = 8192

// Type is what a file's content says it is.
type Type struct {
	// Mime is the verified media type: what the content is, refined by the
	// name only where the content cannot tell (text flavours, and formats
	// built on a container such as zip).
	Mime string `json:"mime"`
	// Text reports content that is plain text, whatever the name.
	Text bool `json:"text"`
	// Mismatch reports a name that claims a different kind of file than the
	// content is, such as a script named photo.jpg.
	Mismatch bool `json:"mismatch"`
}

const octetStream = "application/octet-stream"

// Model types. STEP and FBX have magic bytes; OBJ is recognised by its lines.
const (
	MimeSTEP = "model/step"
	MimeFBX  = "application/vnd.autodesk.fbx"
	MimeOBJ  = "model/obj"
)

type signature struct {
	offset int
	magic  string
	mime   string
}

// Ordered: the first match wins.
var signatures = []signature{
	{0, "\xff\xd8\xff", "image/jpeg"},
	{0, "\x89PNG\r\n\x1a\n", "image/png"},
	{0, "GIF87a", "image/gif"},
	{0, "GIF89a", "image/gif"},
	{0, "BM", "image/bmp"},
	{0, "II*\x00", "image/tiff"},
	{0, "MM\x00*", "image/tiff"},
	{0, "\x00\x00\x01\x00", "image/x-icon"},
	{0, "%PDF-", "application/pdf"},
	{0, "PK\x03\x04", "application/zip"},
	{0, "PK\x05\x06", "application/zip"},
	{0, "PK\x07\x08", "application/zip"},
	{0, "\x1f\x8b", "application/gzip"},
	{0, "BZh", "application/x-bzip2"},
	{0, "\xfd7zXZ\x00", "application/x-xz"},
	{0, "\x28\xb5\x2f\xfd", "application/zstd"},
	{0, "7z\xbc\xaf\x27\x1c", "application/x-7z-compressed"},
	{0, "Rar!\x1a\x07\x00", "application/vnd.rar"},
	{0, "Rar!\x1a\x07\x01\x00", "application/vnd.rar"},
	{257, "ustar", "application/x-tar"},
	{0, "fLaC", "audio/flac"},
	{0, "OggS", "audio/ogg"},
	{0, "ID3", "audio/mpeg"},
	{0, "\x1a\x45\xdf\xa3", "video/x-matroska"},
	{0, "FLV\x01", "video/x-flv"},
	{0, "\x30\x26\xb2\x75\x8e\x66\xcf\x11", "video/x-ms-asf"},
	{0, "\x00\x00\x01\xba", "video/mpeg"},
	{0, "\x00\x00\x01\xb3", "video/mpeg"},
	{0, "FORM", "audio/aiff"},
	{0, "Kaydara FBX Binary  \x00", MimeFBX},
	{0, "ISO-10303-21;", MimeSTEP},
	{0, "glTF", "model/gltf-binary"},
	{0, "\x7fELF", "application/x-executable"},
	{0, "MZ", "application/x-msdownload"},
	{0, "\x00asm", "application/wasm"},
	{0, "SQLite format 3\x00", "application/vnd.sqlite3"},
	{0, "wOFF", "font/woff"},
	{0, "wOF2", "font/woff2"},
}

// isoBrands maps ISO base media "ftyp" major brands to types; unknown brands
// are video/mp4.
var isoBrands = map[string]string{
	"avif": "image/avif", "avis": "image/avif",
	"heic": "image/heic", "heix": "image/heic", "hevc": "image/heic", "heim": "image/heic", "heis": "image/heic",
	"mif1": "image/heif", "msf1": "image/heif",
	"qt  ": "video/quicktime",
	"M4A ": "audio/mp4", "M4B ": "audio/mp4", "M4P ": "audio/mp4",
}

// Sniff identifies content from its first bytes (up to SniffLen). It returns
// "application/octet-stream" for binary data it does not know and
// "text/plain" for text; markup that browsers execute is named as such.
func Sniff(head []byte) string {
	for _, s := range signatures {
		if len(head) >= s.offset+len(s.magic) && string(head[s.offset:s.offset+len(s.magic)]) == s.magic {
			if len(s.magic) <= 3 && s.magic[0] < 0x7f && isText(head) {
				continue // prose that happens to start with "BM", "MZ", "ID3" or "BZh"
			}
			if s.mime == "audio/aiff" && (len(head) < 12 || !strings.HasPrefix(string(head[8:12]), "AIF")) {
				continue
			}
			if s.mime == "video/x-matroska" && bytes.Contains(head[:min(len(head), 64)], []byte("webm")) {
				return "video/webm"
			}
			return s.mime
		}
	}
	if len(head) >= 12 {
		switch {
		case string(head[4:8]) == "ftyp":
			if t, ok := isoBrands[string(head[8:12])]; ok {
				return t
			}
			return "video/mp4"
		case string(head[:4]) == "RIFF":
			switch string(head[8:12]) {
			case "WEBP":
				return "image/webp"
			case "WAVE":
				return "audio/wav"
			case "AVI ":
				return "video/x-msvideo"
			}
		}
	}
	// MPEG transport stream: sync bytes one packet apart.
	if len(head) > 188 && head[0] == 0x47 && head[188] == 0x47 {
		return "video/mp2t"
	}
	// MPEG audio frame sync (MP3 without an ID3 tag, or ADTS AAC).
	if len(head) >= 2 && head[0] == 0xff && head[1]&0xe0 == 0xe0 && !isText(head) {
		if head[1]&0x06 == 0 {
			return "audio/aac"
		}
		return "audio/mpeg"
	}
	if !isText(head) {
		return octetStream
	}
	return sniffText(head)
}

// isText reports content with no sign of being binary: valid UTF-8 (or UTF-16
// with a byte order mark) without control characters other than whitespace.
func isText(head []byte) bool {
	if len(head) == 0 {
		return true
	}
	if bytes.HasPrefix(head, []byte("\xff\xfe")) || bytes.HasPrefix(head, []byte("\xfe\xff")) {
		return true
	}
	// The sample may end in the middle of a rune.
	for i := 0; i < 3 && len(head) > 0 && !utf8.Valid(head); i++ {
		head = head[:len(head)-1]
	}
	if !utf8.Valid(head) {
		return false
	}
	for _, b := range head {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' && b != '\f' && b != 0x1b {
			return false
		}
	}
	return true
}

func sniffText(head []byte) string {
	s := strings.TrimLeft(strings.TrimPrefix(string(head), "\xef\xbb\xbf"), " \t\r\n")
	lower := strings.ToLower(s[:min(len(s), 1024)])
	switch {
	case strings.HasPrefix(lower, "<!doctype html"), strings.HasPrefix(lower, "<html"),
		strings.HasPrefix(lower, "<head"), strings.HasPrefix(lower, "<body"), strings.HasPrefix(lower, "<script"):
		return "text/html"
	case strings.HasPrefix(lower, "<svg"), strings.HasPrefix(lower, "<?xml") && strings.Contains(lower, "<svg"):
		return "image/svg+xml"
	case strings.HasPrefix(lower, "<?xml"):
		return "application/xml"
	case strings.HasPrefix(lower, "; fbx"):
		return MimeFBX
	}
	if looksLikeOBJ(s) {
		return MimeOBJ
	}
	return "text/plain"
}

// looksLikeOBJ recognises Wavefront OBJ, which has no magic: every line is a
// comment or starts with one of a few keywords, and there are vertices.
func looksLikeOBJ(s string) bool {
	lines := strings.Split(s, "\n")
	if len(lines) > 1 {
		lines = lines[:len(lines)-1] // the last line of the sample may be cut short
	}
	vertices := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		key, _, _ := strings.Cut(line, " ")
		switch key {
		case "v":
			vertices++
		case "vn", "vt", "vp", "f", "l", "p", "o", "g", "s", "mtllib", "usemtl", "usemap", "cstype", "deg", "curv", "surf", "parm", "end":
		default:
			return false
		}
	}
	return vertices > 0
}

// containers lists types that are, on disk, another format: for these the
// content confirms the container and the name says which flavour it is.
var containers = map[string][]string{
	"application/zip": {
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/epub+zip", "application/java-archive", "application/vnd.android.package-archive",
		"application/vnd.oasis.opendocument.text", "application/vnd.oasis.opendocument.spreadsheet",
		"application/vnd.oasis.opendocument.presentation", "model/3mf",
	},
	"video/x-matroska":         {"audio/x-matroska", "video/webm"},
	"video/webm":               {"audio/webm", "video/x-matroska"},
	"video/mp4":                {"audio/mp4", "video/quicktime", "video/3gpp"},
	"video/quicktime":          {"video/mp4"},
	"audio/mp4":                {"video/mp4"},
	"audio/ogg":                {"video/ogg", "application/ogg"},
	"video/x-ms-asf":           {"video/x-ms-wmv", "audio/x-ms-wma"},
	"audio/mpeg":               {"audio/aac"},
	"audio/aac":                {"audio/mpeg"},
	"image/heif":               {"image/heic", "image/avif"},
	"application/x-executable": {"application/x-sharedlib"},
}

// sniffable lists the types Sniff recognises by magic. A name claiming one of
// them over content without that magic is a mismatch.
var sniffable = func() map[string]bool {
	m := map[string]bool{"image/webp": true, "audio/wav": true, "video/x-msvideo": true, "video/mp4": true,
		"video/webm": true, "video/mp2t": true}
	for _, s := range signatures {
		m[s.mime] = true
	}
	for _, t := range isoBrands {
		m[t] = true
	}
	return m
}()

// textTypes are non-"text/" types whose files are text.
var textTypes = map[string]bool{
	"application/json": true, "application/xml": true, "application/x-subrip": true, "image/svg+xml": true,
	"application/javascript": true, "application/x-sh": true, "application/toml": true, "application/yaml": true,
	"application/x-yaml": true, "application/sql": true, MimeOBJ: true, MimeSTEP: true, MimeFBX: true,
}

// IsTextType reports whether files of type t are text.
func IsTextType(t string) bool { return strings.HasPrefix(t, "text/") || textTypes[t] }

// Verify decides what a file is from its first bytes and its name.
func Verify(name string, head []byte) Type {
	claimed, sniffed := MimeType(name), Sniff(head)
	switch {
	case sniffed == claimed:
		return Type{Mime: sniffed, Text: IsTextType(sniffed) && isText(head)}
	case sniffed == "text/plain":
		// Text of no particular shape: the name picks the flavour, unless it
		// claims a binary format.
		if IsTextType(claimed) && claimed != "text/html" && claimed != "image/svg+xml" && claimed != MimeFBX {
			return Type{Mime: claimed, Text: true}
		}
		return Type{Mime: "text/plain", Text: true, Mismatch: claimed != octetStream && !IsTextType(claimed) && !ambiguous(name)}
	case sniffed == octetStream:
		// Unknown binary: trust the name only for formats we cannot check.
		if sniffable[claimed] || IsTextType(claimed) {
			return Type{Mime: octetStream, Mismatch: !ambiguous(name)}
		}
		return Type{Mime: claimed}
	}
	for _, flavour := range containers[sniffed] {
		if claimed == flavour {
			return Type{Mime: claimed}
		}
	}
	// The content is a known format the name does not claim. Text formats
	// found under a generic text name (an .xml that is XML, a .txt that is
	// HTML) are not a contradiction; they are still served as what they are.
	text := IsTextType(sniffed) && isText(head)
	mismatch := claimed != octetStream && !(text && (claimed == "text/plain" || claimed == "application/xml" && sniffed == "image/svg+xml"))
	return Type{Mime: sniffed, Text: text, Mismatch: mismatch}
}

// ambiguous reports extensions shared by unrelated formats (TypeScript and
// MPEG transport streams, Wavefront models and compiler output): content
// that is the other one is not a contradiction.
func ambiguous(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".ts", ".obj":
		return true
	}
	return false
}

// VerifyFile is Verify for an open file; it reads the head without moving
// the file offset.
func VerifyFile(name string, f io.ReaderAt) (Type, error) {
	head, err := ReadHead(f)
	if err != nil {
		return Type{}, err
	}
	return Verify(name, head), nil
}

// ReadHead returns the first SniffLen bytes of f.
func ReadHead(f io.ReaderAt) ([]byte, error) {
	buf := make([]byte, SniffLen)
	n, err := f.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return nil, mapErr(err)
	}
	return buf[:n], nil
}
