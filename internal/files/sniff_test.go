package files

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"testing"
)

func TestVerify(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	cases := []struct {
		name, head, mime string
		text, mismatch   bool
	}{
		{"a.png", png, "image/png", false, false},
		{"a.jpg", png, "image/png", false, true},
		{"photo.jpg", "<script>alert(1)</script>", "text/html", true, true},
		{"photo.png", "<svg xmlns='http://www.w3.org/2000/svg'>", "image/svg+xml", true, true},
		{"notes.txt", "<!DOCTYPE html><html>", "text/html", true, false},
		{"a.pdf", "%PDF-1.7\n", "application/pdf", false, false},
		{"a.pdf", "just words", "text/plain", true, true},
		{"a.docx", "PK\x03\x04\x14\x00", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", false, false},
		{"a.jpg", "PK\x03\x04\x14\x00", "application/zip", false, true},
		{"main.zzsrc", "package main\n\nfunc main() {}\n", "text/plain", true, false},
		{"app.ts", "export const x: number = 1;\n", "text/plain", true, false},
		{"readme.md", "# Title\n", "text/markdown", true, false},
		{"data.json", `{"a": 1}`, "application/json", true, false},
		{"part.step", "ISO-10303-21;\nHEADER;\n", MimeSTEP, true, false},
		{"part.txt", "ISO-10303-21;\nHEADER;\n", MimeSTEP, true, false},
		{"m.fbx", "Kaydara FBX Binary  \x00\x1a\x00", MimeFBX, false, false},
		{"m.fbx", "; FBX 7.3.0 project file\n", MimeFBX, true, false},
		{"m.obj", "# cube\nv 0 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n", MimeOBJ, true, false},
		{"m.obj", "\x7fELF\x02\x01\x01\x00", "application/x-executable", false, true},
		{"movie.mkv", "\x1a\x45\xdf\xa3\x01\x00\x00\x00\x00\x00\x00\x1f\x42\x86\x81\x01matroska", "video/x-matroska", false, false},
		{"clip.mp4", "\x00\x00\x00\x20ftypisom\x00\x00\x02\x00", "video/mp4", false, false},
		{"pic.heic", "\x00\x00\x00\x20ftypheic\x00\x00\x00\x00", "image/heic", false, false},
		{"song.m4a", "\x00\x00\x00\x20ftypM4A \x00\x00\x00\x00", "audio/mp4", false, false},
		{"BMW.txt", "BMW makes cars.\n", "text/plain", true, false},
		{"x.bin", "\x00\x01\x02\x03", "application/octet-stream", false, false},
		{"x.png", "\x00\x01\x02\x03", "application/octet-stream", false, true},
		{"empty.txt", "", "text/plain", true, false},
		{"LICENSE", "MIT License\n", "text/plain", true, false},
		{"photo", png, "image/png", false, false},
	}
	for _, c := range cases {
		got := Verify(c.name, []byte(c.head))
		if got.Mime != c.mime || got.Text != c.text || got.Mismatch != c.mismatch {
			t.Errorf("Verify(%q, %q) = %+v; want mime %s text %v mismatch %v", c.name, c.head, got, c.mime, c.text, c.mismatch)
		}
	}
	// Nothing that is not what it claims may be rendered inline as an image.
	if InlineSafe(Verify("photo.jpg", []byte("<script>alert(1)</script>")).Mime, false) {
		t.Error("script named .jpg is inline-safe")
	}
}

func TestInspectArchive(t *testing.T) {
	ctx := context.Background()
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, _ := zw.Create("dir/a.txt")
	w.Write([]byte("hello"))
	// An entry flagged as encrypted (bit 0), as password-protected zips have.
	w, _ = zw.CreateRaw(&zip.FileHeader{Name: "secret.txt", Flags: 0x1, Method: zip.Store})
	w.Write([]byte("xxxx"))
	zw.Close()
	a, err := InspectArchive(ctx, bytes.NewReader(zb.Bytes()), int64(zb.Len()), "application/zip", 100)
	if err != nil || a.Count != 2 || a.Encrypted != "entries" || !a.Listable || !a.Entries[1].Encrypted || a.Entries[0].Encrypted {
		t.Fatalf("zip: %+v, %v", a, err)
	}
	if a, _ = InspectArchive(ctx, bytes.NewReader(zb.Bytes()), int64(zb.Len()), "application/zip", 0); a.Encrypted != "entries" || len(a.Entries) != 0 {
		t.Fatalf("zip summary: %+v", a)
	}
	if a, _ = InspectArchive(ctx, bytes.NewReader(zb.Bytes()), int64(zb.Len()), "application/zip", 1); len(a.Entries) != 1 || !a.Truncated || a.Encrypted != "entries" {
		t.Fatalf("zip limit: %+v", a)
	}

	var tb bytes.Buffer
	gz := gzip.NewWriter(&tb)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "x/y.txt", Size: 3, Mode: 0o644})
	tw.Write([]byte("abc"))
	tw.Close()
	gz.Close()
	if Sniff(tb.Bytes()) != "application/gzip" {
		t.Fatal("gzip not recognised")
	}
	a, err = InspectArchive(ctx, bytes.NewReader(tb.Bytes()), int64(tb.Len()), "application/gzip", 100)
	if err != nil || a.Format != "tar.gz" || a.Count != 1 || a.Entries[0].Name != "x/y.txt" || a.Entries[0].Size != 3 {
		t.Fatalf("tar.gz: %+v, %v", a, err)
	}
	// A gzip that holds no tar is not an archive.
	var pb bytes.Buffer
	gz = gzip.NewWriter(&pb)
	gz.Write(bytes.Repeat([]byte("plain text, not a tar\n"), 100))
	gz.Close()
	if _, err := InspectArchive(ctx, bytes.NewReader(pb.Bytes()), int64(pb.Len()), "application/gzip", 100); err == nil {
		t.Fatal("plain gzip listed as tar")
	}

	rar5 := func(typ byte) []byte {
		return append([]byte("Rar!\x1a\x07\x01\x00\x00\x00\x00\x00\x0b"), typ, 0, 0, 0, 0, 0, 0, 0, 0)
	}
	if a, _ = InspectArchive(ctx, bytes.NewReader(rar5(4)), 22, "application/vnd.rar", 0); a.Encrypted != "headers" {
		t.Fatalf("rar5 encrypted headers: %+v", a)
	}
	if a, _ = InspectArchive(ctx, bytes.NewReader(rar5(1)), 22, "application/vnd.rar", 0); a.Encrypted != "unknown" {
		t.Fatalf("rar5 plain: %+v", a)
	}
	rar4 := []byte("Rar!\x1a\x07\x00\x00\x00\x73\x80\x00\x0d\x00\x00\x00\x00\x00\x00\x00")
	if a, _ = InspectArchive(ctx, bytes.NewReader(rar4), int64(len(rar4)), "application/vnd.rar", 0); a.Encrypted != "headers" {
		t.Fatalf("rar4 encrypted headers: %+v", a)
	}

	sevenZ := func(hdr []byte) []byte {
		b := make([]byte, 32, 32+len(hdr))
		copy(b, "7z\xbc\xaf\x27\x1c")
		b[20] = byte(len(hdr))
		return append(b, hdr...)
	}
	for hdr, want := range map[string]string{
		"\x17\x06\x00\x01\x09\x10\x00\x07\x0b\x01\x00\x24\x06\xf1\x07\x01\x00": "headers",
		"\x17\x06\x00\x01\x09\x10\x00\x07\x0b\x01\x00\x23\x03\x01\x01\x05\x00": "unknown",
		"\x01\x04\x06\x00\x01\x09\x10\x00\x07\x0b\x24\x06\xf1\x07\x01\x00\x00": "entries",
		"\x01\x04\x06\x00\x01\x09\x10\x00\x07\x0b\x23\x03\x01\x01\x05\x00\x00": "no",
	} {
		b := sevenZ([]byte(hdr))
		if a, _ = InspectArchive(ctx, bytes.NewReader(b), int64(len(b)), "application/x-7z-compressed", 0); a.Encrypted != want {
			t.Errorf("7z %q: encrypted %q, want %q", hdr, a.Encrypted, want)
		}
	}
}
