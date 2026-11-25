package web

import (
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/Netsession-LAN/Netsession-Relay/internal/otlp"
	"github.com/Netsession-LAN/Netsession-Relay/internal/snmp"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/syslog"
)

// Creating and editing a push target (System → Monitoring): the form is in monitoring_target_form.go, the
// options per kind in monitoring_target_config.go.

func (s *Server) targetNew(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("art")
	if kind != store.TargetOTLP && kind != store.TargetSNMPTrap {
		kind = store.TargetSyslog
	}
	t := store.Target{Kind: kind, Enabled: true, Events: []string{"alarm", "audit"}, Config: map[string]any{}}
	if kind == store.TargetSNMPTrap {
		t.Events = []string{"alarm"} // audit as trap is possible but noise
	}
	f := formFromTarget(t)
	if kind == store.TargetOTLP {
		f.Name, f.Endpoint = "", ""
	}
	s.render(w, r, "monitoring-target", "Ziel hinzufügen", "system", f)
}

func (s *Server) targetEdit(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	t, err := s.Store.Target(r.Context(), id)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "Dieses Ziel gibt es nicht.")
		return
	}
	s.render(w, r, "monitoring-target", s.T(r)("%s bearbeiten", t.Name), "system", formFromTarget(t))
}

func (s *Server) targetSave(w http.ResponseWriter, r *http.Request) {
	T := s.T(r)
	f := readTargetForm(r)
	stored := s.adoptStoredTarget(r, &f)
	title := targetEditTitle(f, T)
	reject := func(msg string) {
		f.Err = msg
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "monitoring-target", title, "system", f)
	}
	if f.Name == "" {
		reject(T("Bitte einen Namen eingeben."))
		return
	}
	t := store.Target{ID: f.ID, Kind: f.Kind, Name: f.Name, Enabled: f.Enabled, Endpoint: f.Endpoint, Events: f.events()}
	var msg string
	t.Config, t.Secret, msg = targetOptions(r, &f, stored, T)
	if msg != "" {
		reject(msg)
		return
	}
	if f.ClearHeaders {
		dropStoredHeaders(t.Secret, stored)
	}
	if _, err := s.Store.SaveTarget(r.Context(), s.actor(r), t); err != nil {
		reject(s.targetStoreError(r, err))
		return
	}
	s.Outputs.Reload(r.Context())
	back(w, r, "/system/monitoring", T("%s gespeichert. Schick eine Testmeldung.", t.Name), nil)
}

// targetEditTitle is the title of the target form: adding a target, or editing the stored one.
func targetEditTitle(f targetForm, T translator) string {
	if f.ID == 0 {
		return "Ziel hinzufügen"
	}
	return T("%s bearbeiten", f.Name)
}

// adoptStoredTarget takes over what an existing target keeps: its kind, and the names of its stored secrets
// (shown as "set"; they are used where the form left a secret empty). It returns the stored secrets, nil for a
// new target.
func (s *Server) adoptStoredTarget(r *http.Request, f *targetForm) map[string]string {
	if f.ID == 0 {
		return nil
	}
	cur, err := s.Store.Target(r.Context(), f.ID)
	if err != nil {
		return nil
	}
	cf := formFromTarget(cur)
	f.HeaderNames, f.SecretsSet = cf.HeaderNames, cf.SecretsSet
	f.Kind = cur.Kind
	return cur.Secret
}

// targetError words a validation error of a target in the language of T. The problems without an identity of
// their own (an unknown transport, a bad header name) are described by their text.
func targetError(err error, T translator) string {
	for _, e := range targetErrorTexts {
		if slices.ContainsFunc(e.errs, func(t error) bool { return errors.Is(err, t) }) {
			return T(e.text)
		}
	}
	return T("Das passt nicht: %s", err.Error())
}

// targetErrorTexts are the sentences for the validation errors of the outputs, first match wins.
var targetErrorTexts = []struct {
	errs []error
	text string
}{
	{[]error{syslog.ErrEndpoint, otlp.ErrEndpoint, snmp.ErrEndpoint}, "Adresse bitte als Adresse:Port, z. B. 10.0.0.5:514."},
	{[]error{otlp.ErrEndpointURL}, "Adresse bitte als URL, z. B. http://10.0.0.5:4318."},
	{[]error{otlp.ErrNoSignal}, "Bitte Metriken, Meldungen oder beides wählen."},
	{[]error{snmp.ErrCommunityMissing}, "Bitte die Community eintragen."},
	{[]error{snmp.ErrUserMissing}, "Bitte den SNMPv3-Benutzer eintragen."},
	{[]error{snmp.ErrPasswordShort}, "Die SNMPv3-Passwörter brauchen mindestens 8 Zeichen."},
	{[]error{syslog.ErrTLSCA, otlp.ErrTLSCA}, "Im CA-Feld steht kein Zertifikat (PEM)."},
}

// targetStoreError words why a target could not be stored; anything unexpected is an internal error.
func (s *Server) targetStoreError(r *http.Request, err error) string {
	T := s.T(r)
	switch {
	case errors.Is(err, store.ErrExists):
		return T("Ein Ziel mit diesem Namen gibt es schon.")
	case errors.Is(err, store.ErrTooManyTargets):
		return T("Höchstens %d Ziele.", store.MaxTargets)
	}
	return string(s.userError(r, "monitoring target: store", err))
}
