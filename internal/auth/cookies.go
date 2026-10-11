package auth

import (
	"net/http"
	"time"
)

// Cookie describes a cookie set by SetCookie: always HttpOnly, for the whole
// origin, and Secure outside dev mode.
type Cookie struct {
	Name, Value string
	Expires     time.Time
	MaxAge      int
	SameSite    http.SameSite
}

func SetCookie(w http.ResponseWriter, c Cookie, secure bool) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 nosemgrep -- Secure is off only with dev = true (http://localhost)
		Name: c.Name, Value: c.Value, Expires: c.Expires, MaxAge: c.MaxAge, SameSite: c.SameSite,
		Path: "/", HttpOnly: true, Secure: secure,
	})
}

// ClearCookie deletes a cookie set with SetCookie.
func ClearCookie(w http.ResponseWriter, name string, secure bool, sameSite http.SameSite) {
	SetCookie(w, Cookie{Name: name, MaxAge: -1, SameSite: sameSite}, secure)
}
