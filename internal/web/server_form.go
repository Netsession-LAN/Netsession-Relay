package web

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// Reading the body of a request: forms with a size and time limit.

// readForm reads the request body into the form values. Bodies are small forms: bounded in size and in the
// time a client may take to send them. The one upload (the event logo) gets more of both, and only for an
// admin: nobody else makes the server read that much. It reports false when it has answered the request itself
// (a body too large or malformed).
func (s *Server) readForm(w http.ResponseWriter, r *http.Request, rc *http.ResponseController) bool {
	limit, wait := int64(MaxBody), bodyTimeout
	upload := r.Method == http.MethodPost && r.URL.Path == logoUploadPath && s.adminSession(r)
	if upload {
		limit, wait = MaxLogoBody, uploadTimeout
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	_ = rc.SetReadDeadline(time.Now().Add(wait))
	if err := parseBody(r, limit); err != nil {
		refuseBody(w, r, err, upload)
		return false
	}
	_ = rc.SetReadDeadline(time.Time{})
	return true
}

// parseBody reads the whole body now (forms from pages are urlencoded, the ones from app.js multipart); files
// stay in memory, nothing is written to a temporary directory.
func parseBody(r *http.Request, limit int64) error {
	err := r.ParseForm()
	if err == nil && strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		err = r.ParseMultipartForm(limit)
	}
	return err
}

// refuseBody answers a request whose body could not be read: 413 when it was too large (for the logo upload a
// page the admin can read instead of a bare status), else 400.
func refuseBody(w http.ResponseWriter, r *http.Request, err error, upload bool) {
	var tooBig *http.MaxBytesError
	switch {
	case !errors.As(err, &tooBig):
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
	case upload:
		back(w, r, logoPanel, "", msgLogoTooBig)
	default:
		http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
	}
}
