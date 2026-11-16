package files

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strings"
	"time"
)

// Archive describes an archive file without extracting anything from it.
type Archive struct {
	// Format is zip, tar, tar.gz, tar.bz2, 7z, rar or unitypackage (whose
	// entries are the project paths of its assets).
	Format string `json:"format"`
	// Encrypted says whether a password is needed: "no", "entries" (the
	// index is readable, some or all contents are not), "headers" (not even
	// the index is readable) or "unknown".
	Encrypted string `json:"encrypted"`
	// Listable reports formats whose index can be read here.
	Listable bool `json:"listable"`
	// Count is the number of entries seen (all of them unless Truncated).
	Count     int            `json:"count"`
	Truncated bool           `json:"truncated,omitempty"`
	Entries   []ArchiveEntry `json:"entries,omitempty"`
}

type ArchiveEntry struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	IsDir     bool      `json:"is_dir,omitempty"`
	ModTime   time.Time `json:"mtime"`
	Encrypted bool      `json:"encrypted,omitempty"`
}

// Limits on reading an untrusted archive's index.
const (
	maxZipDirectory = 64 << 20  // central directory bytes held in memory
	maxTarScan      = 512 << 20 // compressed bytes read looking for tar headers
	max7zHeader     = 1 << 20
)

// IsArchive reports verified types InspectArchive understands.
func IsArchive(mime string) bool {
	switch mime {
	case "application/zip", "application/x-tar", "application/gzip", "application/x-bzip2",
		"application/x-7z-compressed", "application/vnd.rar":
		return true
	}
	return false
}

// InspectArchive reads the index of the archive f (size bytes, of the
// verified type mime), returning up to limit entries; with limit 0 it only
// summarises, and does not walk formats that have no central index (tar).
// The archive is untrusted: nothing is decompressed into memory, and scans
// stop at ctx's deadline.
func InspectArchive(ctx context.Context, f io.ReaderAt, size int64, mime string, limit int) (*Archive, error) {
	switch mime {
	case "application/zip":
		return inspectZip(f, size, limit)
	case "application/x-tar":
		return inspectTar(ctx, io.NewSectionReader(f, 0, size), "tar", limit)
	case "application/gzip":
		zr, err := gzip.NewReader(io.NewSectionReader(f, 0, min(size, maxTarScan)))
		if err != nil {
			return nil, ErrBadArchive
		}
		return inspectTar(ctx, zr, "tar.gz", limit)
	case "application/x-bzip2":
		return inspectTar(ctx, bzip2.NewReader(io.NewSectionReader(f, 0, min(size, maxTarScan))), "tar.bz2", limit)
	case "application/x-7z-compressed":
		return inspect7z(f, size)
	case "application/vnd.rar":
		return inspectRar(f)
	}
	return nil, ErrBadArchive
}

// ErrBadArchive reports a file that is not an archive that can be inspected.
var ErrBadArchive = errors.New("not a readable archive")

func inspectZip(f io.ReaderAt, size int64, limit int) (*Archive, error) {
	a := &Archive{Format: "zip", Encrypted: "no"}
	// archive/zip loads the whole central directory: check its size first.
	if n, ok := zipDirectorySize(f, size); !ok || n > maxZipDirectory {
		a.Encrypted, a.Truncated = "unknown", true
		return a, nil
	}
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return nil, ErrBadArchive
	}
	a.Listable, a.Count = true, len(zr.File)
	for _, zf := range zr.File {
		enc := zf.Flags&0x1 != 0
		if enc {
			a.Encrypted = "entries"
		}
		if zf.Flags&0x2000 != 0 { // central directory encryption: names are masked
			a.Encrypted, a.Listable = "headers", false
			a.Entries = nil
			return a, nil
		}
		if len(a.Entries) >= limit {
			a.Truncated = limit > 0 && a.Count > limit
			continue // keep looking for encrypted entries
		}
		a.Entries = append(a.Entries, ArchiveEntry{Name: strings.ToValidUTF8(zf.Name, "�"),
			Size:  int64(min(zf.UncompressedSize64, 1<<62)), // #nosec G115 -- bounded
			IsDir: zf.FileInfo().IsDir(), ModTime: zf.Modified.UTC(), Encrypted: enc})
	}
	return a, nil
}

// zipDirectorySize finds the end-of-central-directory record and returns the
// directory's size. Zip64 archives report false (too large to index here).
func zipDirectorySize(f io.ReaderAt, size int64) (int64, bool) {
	const eocdLen, maxComment = 22, 65535
	n := min(size, eocdLen+maxComment)
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, size-n); err != nil && err != io.EOF {
		return 0, false
	}
	i := bytes.LastIndex(buf, []byte("PK\x05\x06"))
	if i < 0 || len(buf)-i < eocdLen {
		return 0, false
	}
	dir := binary.LittleEndian.Uint32(buf[i+12:])
	if dir == 0xffffffff || binary.LittleEndian.Uint16(buf[i+10:]) == 0xffff {
		return 0, false
	}
	return int64(dir), true
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func inspectTar(ctx context.Context, r io.Reader, format string, limit int) (*Archive, error) {
	a := &Archive{Format: format, Encrypted: "no", Listable: true}
	tr := tar.NewReader(ctxReader{ctx, r})
	if limit == 0 {
		// Summary only: confirm it is a tar, without walking it.
		h, err := tr.Next()
		for err == nil && tarName(h) == "" {
			h, err = tr.Next()
		}
		if err != nil && err != io.EOF {
			return nil, ErrBadArchive
		}
		if err == nil {
			if _, _, ok := unityMember(tarName(h)); ok {
				a.Format = unityFormat
			}
		}
		a.Truncated = true
		return a, nil
	}
	// A Unity package is a tar.gz of one directory per asset, named by the
	// asset's GUID; it is recognised by that shape and indexed by the paths
	// its assets have in a project, not by GUID. While every member fits
	// the shape, the walk goes on past limit members: an asset is several.
	unity := unityAssets{}
	for {
		h, err := tr.Next()
		if err != nil {
			if err != io.EOF {
				if a.Count == 0 && ctx.Err() == nil {
					return nil, ErrBadArchive // e.g. a .gz that holds something else
				}
				a.Truncated = true // deadline, scan limit, or a damaged archive
			}
			break
		}
		name := tarName(h)
		if name == "" {
			continue
		}
		a.Count++
		if unity != nil {
			if guid, member, ok := unityMember(name); !ok {
				unity = nil
			} else if a.Count > 6*limit {
				a.Truncated = true
				break
			} else {
				unity.add(guid, member, h, tr)
			}
		}
		if len(a.Entries) < limit {
			a.Entries = append(a.Entries, ArchiveEntry{Name: strings.ToValidUTF8(name, "\ufffd"), Size: h.Size,
				IsDir: h.Typeflag == tar.TypeDir, ModTime: h.ModTime.UTC()})
		} else if unity == nil {
			a.Truncated = true
			break
		}
	}
	if entries := unity.entries(); len(entries) > 0 {
		a.Format, a.Count = unityFormat, len(entries)
		if len(entries) > limit {
			entries, a.Truncated = entries[:limit], true
		}
		a.Entries = entries
	} else if len(a.Entries) < a.Count {
		a.Truncated = true
	}
	return a, nil
}

// tarName is a member's name without the "./" some writers put in front;
// the archive's own root directory is "".
func tarName(h *tar.Header) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(h.Name, "./"), "/"), "/")
}

const unityFormat = "unitypackage"

// A Unity package holds, for each asset, a directory named by its GUID with:
//
//	pathname    the asset's path in the project ("Assets/Models/ship.fbx")
//	asset       its content; absent for a folder
//	asset.meta  its import settings
//	preview.png an optional thumbnail
type unityAsset struct {
	path    string
	size    int64
	mtime   time.Time
	content bool
}

type unityAssets map[string]*unityAsset

// unityMember splits "<guid>" or "<guid>/<member>" and reports whether name
// has the shape of a Unity package member.
func unityMember(name string) (guid, member string, ok bool) {
	guid, member, _ = strings.Cut(name, "/")
	if len(guid) != 32 || strings.Trim(guid, "0123456789abcdef") != "" {
		return "", "", false
	}
	switch member {
	case "", "asset", "asset.meta", "pathname", "preview.png", "metaData":
		return guid, member, true
	}
	return "", "", false
}

func (u unityAssets) add(guid, member string, h *tar.Header, content io.Reader) {
	as := u[guid]
	if as == nil {
		as = &unityAsset{}
		u[guid] = as
	}
	switch member {
	case "asset":
		as.content, as.size, as.mtime = true, h.Size, h.ModTime.UTC()
	case "pathname":
		// The path is the first line; some versions add a second.
		buf := make([]byte, 4096)
		n, _ := io.ReadFull(content, buf)
		line, _, _ := strings.Cut(string(buf[:n]), "\n")
		as.path = strings.ToValidUTF8(strings.TrimSpace(line), "\ufffd")
		if as.mtime.IsZero() {
			as.mtime = h.ModTime.UTC()
		}
	}
}

// entries lists the assets by project path; it is empty unless u looks like
// a package (nil when a member did not fit).
func (u unityAssets) entries() []ArchiveEntry {
	var out []ArchiveEntry
	for _, as := range u {
		if as.path != "" {
			out = append(out, ArchiveEntry{Name: as.path, Size: as.size, IsDir: !as.content, ModTime: as.mtime})
		}
	}
	slices.SortFunc(out, func(a, b ArchiveEntry) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// aesCoder is the 7z coder id of AES-256 + SHA-256, which marks the streams
// it is applied to as password protected.
var aesCoder = []byte{0x06, 0xf1, 0x07, 0x01}

// inspect7z reads the 7z end header. Listing would need its (usually LZMA
// compressed) index decoded; what can be told without that is whether the
// index itself is encrypted.
func inspect7z(f io.ReaderAt, size int64) (*Archive, error) {
	a := &Archive{Format: "7z", Encrypted: "unknown"}
	var sig [32]byte
	if _, err := f.ReadAt(sig[:], 0); err != nil {
		return nil, ErrBadArchive
	}
	off, n := binary.LittleEndian.Uint64(sig[12:]), binary.LittleEndian.Uint64(sig[20:])
	if n == 0 || n > max7zHeader || off > uint64(size) { // #nosec G115 -- size is non-negative
		return a, nil
	}
	hdr := make([]byte, n)
	if _, err := f.ReadAt(hdr, 32+int64(off)); err != nil { // #nosec G115 -- off <= size
		return a, nil
	}
	const kHeader, kEncodedHeader = 0x01, 0x17
	aes := bytes.Contains(hdr, aesCoder)
	switch hdr[0] {
	case kHeader: // plain index: it names the coders of every stream
		a.Encrypted = "no"
		if aes {
			a.Encrypted = "entries"
		}
	case kEncodedHeader: // the index is packed; only its own coders are visible
		if aes {
			a.Encrypted = "headers"
		}
	}
	return a, nil
}

// inspectRar reads the first headers of a RAR archive to see whether a
// password is needed.
func inspectRar(f io.ReaderAt) (*Archive, error) {
	a := &Archive{Format: "rar", Encrypted: "unknown"}
	buf := make([]byte, 64)
	n, _ := f.ReadAt(buf, 0)
	buf = buf[:n]
	switch {
	case bytes.HasPrefix(buf, []byte("Rar!\x1a\x07\x01\x00")) && len(buf) > 16:
		// RAR5 block: CRC32, then size and type as variable-length integers.
		// Type 4 is the archive encryption header: everything after it,
		// file names included, is encrypted.
		p := buf[8+4:]
		_, sz := binary.Uvarint(p)
		if sz <= 0 || sz >= len(p) {
			return a, nil
		}
		if typ, _ := binary.Uvarint(p[sz:]); typ == 4 {
			a.Encrypted = "headers"
		}
	case bytes.HasPrefix(buf, []byte("Rar!\x1a\x07\x00")) && len(buf) >= 14:
		// RAR4 main header: CRC16, type 0x73, flags, size.
		main := buf[7:]
		if main[2] != 0x73 {
			return a, nil
		}
		if binary.LittleEndian.Uint16(main[3:])&0x0080 != 0 {
			a.Encrypted = "headers"
			return a, nil
		}
		// The first file header says whether that file is encrypted.
		var fh [7]byte
		at := 7 + int64(binary.LittleEndian.Uint16(main[5:]))
		if _, err := f.ReadAt(fh[:], at); err == nil && fh[2] == 0x74 {
			a.Encrypted = "no"
			if binary.LittleEndian.Uint16(fh[3:])&0x0004 != 0 {
				a.Encrypted = "entries"
			}
		}
	default:
		return nil, ErrBadArchive
	}
	return a, nil
}
