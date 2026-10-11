package files

import (
	"mime"
	"path"
	"strings"
)

var mimeTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp", ".svg": "image/svg+xml",
	".heic": "image/heic", ".heif": "image/heif", ".tif": "image/tiff", ".tiff": "image/tiff", ".ico": "image/x-icon",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime", ".mkv": "video/x-matroska",
	".webm": "video/webm", ".avi": "video/x-msvideo", ".wmv": "video/x-ms-wmv", ".ts": "video/mp2t",
	".m2ts": "video/mp2t", ".mpg": "video/mpeg", ".mpeg": "video/mpeg", ".ogv": "video/ogg", ".flv": "video/x-flv",
	".mp3": "audio/mpeg", ".flac": "audio/flac", ".m4a": "audio/mp4", ".aac": "audio/aac", ".ogg": "audio/ogg",
	".oga": "audio/ogg", ".opus": "audio/ogg", ".wav": "audio/wav", ".wma": "audio/x-ms-wma", ".alac": "audio/mp4",
	".aiff": "audio/aiff", ".aif": "audio/aiff", ".mka": "audio/x-matroska",
	".pdf": "application/pdf", ".zip": "application/zip", ".gz": "application/gzip", ".tar": "application/x-tar",
	".7z": "application/x-7z-compressed", ".rar": "application/vnd.rar", ".json": "application/json",
	".xml": "application/xml", ".html": "text/html", ".htm": "text/html", ".css": "text/css",
	".js": "text/javascript", ".txt": "text/plain", ".md": "text/markdown", ".csv": "text/csv",
	".log": "text/plain", ".srt": "application/x-subrip", ".vtt": "text/vtt", ".ass": "text/x-ssa",
	".doc": "application/msword", ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls": "application/vnd.ms-excel", ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt": "application/vnd.ms-powerpoint", ".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
}

// MimeType guesses the media type from the file name.
func MimeType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if t, ok := mimeTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		t, _, _ = strings.Cut(t, ";")
		return t
	}
	return "application/octet-stream"
}

// inlineSafe lists types that may be rendered inline. Everything else (notably
// HTML, SVG, XML and anything scriptable) is forced to download.
var inlineSafe = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true, "image/avif": true, "image/bmp": true,
	"video/mp4": true, "video/webm": true, "video/ogg": true, "video/quicktime": true,
	"audio/mpeg": true, "audio/flac": true, "audio/mp4": true, "audio/aac": true, "audio/ogg": true, "audio/wav": true,
}

// InlineSafe reports whether type t may be served inline. PDF is only safe on
// the share origin, which holds no session.
func InlineSafe(t string, allowPDF bool) bool {
	return inlineSafe[t] || (allowPDF && t == "application/pdf")
}

// IsText reports whether a type is a plain-text format the UI may preview as text.
func IsText(t string) bool {
	return strings.HasPrefix(t, "text/") || t == "application/json" || t == "application/xml" || t == "application/x-subrip"
}
