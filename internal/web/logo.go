package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/eventlogo"
	"github.com/Netsession2024/Netsession-Relay/internal/store"
)

// The event logo: an installation may show the logo of its own LAN party next to the product mark (header,
// overview, login, setup). An admin uploads a PNG or JPEG on the system page; the server decodes it, scales it
// down and stores a PNG it encoded itself (internal/eventlogo). Everybody sees it, also without a session.
const (
	logoPath       = "/event-logo.png"
	logoUploadPath = "/system/logo"
	logoPanel      = "/system" // where the answer shows: the message is at the top of the page
	// MaxLogoBody is the largest request body of the logo upload: the file plus the multipart framing.
	MaxLogoBody   = eventlogo.MaxUpload + 512<<10
	uploadTimeout = time.Minute
)

// logoView is what the templates need to show the logo.
type logoView struct {
	URL           string // with the hash, so a new logo shows at once
	Alt           string
	Width, Height int
}

// loadLogo reads the logo from the database into memory (pages and the picture itself are served from there).
func (s *Server) loadLogo(ctx context.Context) {
	l, err := s.Store.EventLogo(ctx)
	switch {
	case err == nil:
		s.logo.Store(&l)
	case errors.Is(err, store.ErrNotFound):
		s.logo.Store(nil)
	default:
		s.Log.Error("event logo", "err", err)
	}
}

// SetEventLogo checks and stores an uploaded picture as the event logo (also used by nsrelay web -demo-logo).
func (s *Server) SetEventLogo(ctx context.Context, user string, data []byte) error {
	l, err := eventlogo.Process(data)
	if err != nil {
		return err
	}
	err = s.Store.SetEventLogo(ctx, user, store.EventLogo{PNG: l.PNG, Hash: l.Hash, Width: l.Width, Height: l.Height})
	s.loadLogo(ctx)
	return err
}

func (s *Server) logoView(ctx context.Context, T translator) *logoView {
	l := s.logo.Load()
	if l == nil {
		return nil
	}
	alt, err := s.Store.ActiveEvent(ctx)
	if err != nil || alt == "" {
		alt = T("Logo der LAN")
	}
	return &logoView{URL: logoPath + "?v=" + l.Hash[:min(12, len(l.Hash))], Alt: alt, Width: l.Width, Height: l.Height}
}

// adminSession reports whether the request carries the session of an admin. ServeHTTP asks before it reads the
// body of the upload; the handler behind need() checks role and form token as for every other write.
func (s *Server) adminSession(r *http.Request) bool {
	se, err := s.Store.SessionByToken(r.Context(), s.sessionToken(r))
	return err == nil && store.RoleRank(se.User.Role) >= store.RoleRank(store.RoleAdmin)
}

// eventLogo serves the logo. The bytes are a PNG this server encoded, never the upload itself.
func (s *Server) eventLogo(w http.ResponseWriter, r *http.Request) {
	l := s.logo.Load()
	if l == nil {
		w.Header().Set("Cache-Control", "no-cache")
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("ETag", `"`+l.Hash+`"`)
	h.Set("Cache-Control", "public, max-age=300, must-revalidate")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(l.PNG)) // answers If-None-Match with 304
}

func (s *Server) logoSet(w http.ResponseWriter, r *http.Request) {
	var data []byte
	if r.MultipartForm != nil && len(r.MultipartForm.File["logo"]) > 0 {
		fh := r.MultipartForm.File["logo"][0]
		if fh.Size > eventlogo.MaxUpload {
			back(w, r, logoPanel, "", eventlogo.ErrTooBig)
			return
		}
		f, err := fh.Open()
		if err == nil {
			data, err = io.ReadAll(io.LimitReader(f, eventlogo.MaxUpload+1))
			f.Close()
		}
		if err != nil {
			back(w, r, logoPanel, "", eventlogo.ErrFormat)
			return
		}
	}
	err := s.SetEventLogo(r.Context(), s.actor(r), data)
	switch {
	case err == nil:
	case errors.Is(err, eventlogo.ErrEmpty), errors.Is(err, eventlogo.ErrTooBig), errors.Is(err, eventlogo.ErrFormat), errors.Is(err, eventlogo.ErrTooWide):
		back(w, r, logoPanel, "", err)
		return
	default:
		s.Log.Error("event logo: store", "err", err)
		back(w, r, logoPanel, "", errors.New("Das Logo ließ sich nicht speichern."))
		return
	}
	back(w, r, logoPanel, "Logo gespeichert. Es steht jetzt oben in der Leiste, auf der Übersicht und auf der Anmeldeseite.", nil)
}

func (s *Server) logoRemove(w http.ResponseWriter, r *http.Request) {
	err := s.Store.DeleteEventLogo(r.Context(), s.actor(r))
	s.loadLogo(r.Context())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.Log.Error("event logo: remove", "err", err)
		back(w, r, logoPanel, "", errors.New("Das Logo ließ sich nicht entfernen."))
		return
	}
	back(w, r, logoPanel, "Logo entfernt.", nil)
}
