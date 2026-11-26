package shares

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"filebrowser/internal/auth"
	"filebrowser/internal/httpx"
)

// Link is the payload of a short-lived signed URL on the share origin, used
// for "Open in VLC" and for PDF previews.
type Link struct {
	UserID int64 `json:"u"`
	// Session is the hash of the session that made the link: logging out (or
	// any other end of that session) revokes the link too.
	Session []byte `json:"s"`
	Path    string `json:"p"`
	// FileID is the identity of the file at Path when the link was made (see
	// files.FS.FileID): the link is for that file, not for whatever is later
	// uploaded or moved to the same path.
	FileID  string `json:"f,omitempty"`
	Variant string `json:"v,omitempty"`
	Inline  bool   `json:"i,omitempty"`
	Expires int64  `json:"e"`
}

func (s *Service) SignLink(l Link) string {
	raw, _ := json.Marshal(l)
	sig := s.Keys.Sign("link", raw)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(raw) + "." + enc.EncodeToString(sig[:20])
}

func (s *Service) VerifyLink(tok string) (*Link, error) {
	enc := base64.RawURLEncoding
	a, b, ok := strings.Cut(tok, ".")
	if !ok {
		return nil, ErrInvalid
	}
	raw, err1 := enc.DecodeString(a)
	sig, err2 := enc.DecodeString(b)
	if err1 != nil || err2 != nil || len(sig) != 20 {
		return nil, ErrInvalid
	}
	full := s.Keys.Sign("link", raw)
	if !auth.ConstantTimeEqual(full[:20], sig) {
		return nil, ErrInvalid
	}
	var l Link
	if err := json.Unmarshal(raw, &l); err != nil || time.Now().Unix() > l.Expires {
		return nil, ErrInvalid
	}
	return &l, nil
}

// LinkURL builds the public URL for a signed link to the file now at l.Path
// (a real path); the file name is appended so external players show a
// sensible title.
func (s *Service) LinkURL(l Link, name string) (string, error) {
	id, err := s.Files.FS.FileID(l.Path)
	if err != nil {
		return "", err
	}
	l.FileID = id
	return s.linkURL(l, name), nil
}

// SameFile reports whether the file at the real path p is still the one l was
// made for.
func (s *Service) SameFile(l *Link, p string) bool {
	id, err := s.Files.FS.FileID(p)
	return err == nil && id == l.FileID
}

func (s *Service) linkURL(l Link, name string) string {
	return s.ShareOrigin + "/x/" + s.SignLink(l) + "/" + httpx.PathEscape(name)
}
