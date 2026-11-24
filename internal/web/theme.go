package web

import (
	"net/http"
	"time"
)

// UI themes. The Netsession brand is the default and the only theme for the login page; "98" is a joke theme in
// the look of a 1998 desktop (static/theme98.css on top of app.css), chosen per browser in the account menu.
const (
	themeDefault = "netsession"
	theme98      = "98"
	themeCookie  = "nsrelay_theme"
)

// themeOf is the theme of a request: the cookie set by the switch, else the brand.
func themeOf(r *http.Request) string {
	if c, err := r.Cookie(themeCookie); err == nil && c.Value == theme98 {
		return theme98
	}
	return themeDefault
}

// w98Mode tells how a page is rendered in the theme "98". The browser says what the request is for
// (Sec-Fetch-Dest): a top-level GET gets the desktop shell, whose window manager (static/w98.js) loads the same
// address into a window, and that request, made by an iframe, gets the page without the desktop around it.
// Without the header (old browsers) the page is rendered as a single window with the taskbar, no script needed.
func w98Mode(r *http.Request, theme string) string {
	if theme != theme98 {
		return ""
	}
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "document":
		if r.Method == http.MethodGet {
			return "shell"
		}
	case "iframe":
		return "frame"
	}
	return ""
}

// setTheme stores the chosen theme in a cookie (a year) and goes back to the page the switch was used on.
func (s *Server) setTheme(w http.ResponseWriter, r *http.Request) {
	th := r.PathValue("theme")
	if th != themeDefault && th != theme98 {
		http.NotFound(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: themeCookie, Value: th, Path: "/", MaxAge: int((365 * 24 * time.Hour).Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secure(r)})
	http.Redirect(w, r, safeBack(r.URL.Query().Get("zurueck")), http.StatusSeeOther)
}

// safeBack keeps a local path for a redirect and falls back to "/" for anything that could leave the site.
func safeBack(back string) string {
	if !localPath(back) {
		return "/"
	}
	return back
}
