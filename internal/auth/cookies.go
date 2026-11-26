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

// CookieName picks a cookie's name. Served over TLS it carries the __Host-
// prefix, which makes browsers hold it to this host, the path / and the
// Secure attribute; over plain http in development that prefix would be
// refused, so there is a second name.
func CookieName(secure bool, hostName, devName string) string {
	if secure {
		return "__Host-" + hostName
	}
	return devName
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
