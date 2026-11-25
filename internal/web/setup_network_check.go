package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/control"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The checks of step 2 of the setup: the fields above the network list, then the engine checks the finished
// configuration.

// setupBasics checks the fields above the list of networks: trunk interface, mode, name of the event and the
// host part of the relay address. msg is "" or what is wrong, in the language of T.
func (s *Server) setupBasics(ctx context.Context, f setupForm, T translator) (trunk string, host int, msg string) {
	if trunk, msg = s.setupTrunk(ctx, f, T); msg != "" {
		return "", 0, msg
	}
	if msg = setupModeEventProblem(f, T); msg != "" {
		return "", 0, msg
	}
	if host, msg = setupHostPart(f.Host, T); msg != "" {
		return "", 0, msg
	}
	return trunk, host, ""
}

// setupTrunk is the trunk interface of the form (the typed name wins over the list), or what is wrong with it.
func (s *Server) setupTrunk(ctx context.Context, f setupForm, T translator) (trunk, msg string) {
	trunk = cmp.Or(f.TrunkOther, f.Trunk)
	if !ifName.MatchString(trunk) {
		return "", T("Bitte die Schnittstelle wählen, an der die Netze als Trunk anliegen.")
	}
	return trunk, s.setupTrunkProblem(ctx, trunk, T)
}

// setupModeEventProblem checks the mode and the name of the event; "" or what is wrong.
func setupModeEventProblem(f setupForm, T translator) string {
	if f.Mode != compile.ModeObserve && f.Mode != compile.ModeActive {
		return T("Bitte eine Betriebsart wählen.")
	}
	if f.Event == "" || len(f.Event) > 60 {
		return T("Bitte einen Namen für die LAN eingeben (höchstens 60 Zeichen).")
	}
	return ""
}

// setupCheck compiles the configuration of the setup and lets the engine check it, before anything is stored.
// msg is "" or why it cannot be used.
func (s *Server) setupCheck(r *http.Request, f setupForm, vlans []store.VLANConfig) (cfg store.Config, msg string) {
	ctx := r.Context()
	T := s.T(r)
	db, err := s.Store.GameDB(ctx)
	if err != nil {
		return cfg, string(s.internal(r, "setup: read the games", err))
	}
	cfg = control.SetupConfig(f.Event, f.Mode, vlans, db)
	in, err := store.ConfigInput(cfg, db, true)
	if err != nil {
		return cfg, T("Das Netz passt nicht zur Konfiguration: %s", err.Error())
	}
	snap, _, err := compile.Compile(in)
	switch {
	case errors.Is(err, compile.ErrOverlap):
		return cfg, T("Zwei Netze überschneiden sich: %s", err.Error())
	case err != nil:
		return cfg, T("Das Netz passt nicht zur Konfiguration: %s", err.Error())
	}
	return cfg, s.setupEngineProblem(ctx, snap, T)
}

// setupEngineProblem lets the engine check the snapshot of the setup; "" or why it cannot be used.
func (s *Server) setupEngineProblem(ctx context.Context, snap *compile.Snapshot, T translator) string {
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := s.Engine.Validate(vctx, snap)
	switch {
	case err != nil:
		s.Log.Error("setup: engine not reachable", "err", err)
		return T("Die Engine antwortet nicht, deshalb wurde nichts gespeichert. Läuft der Container der Engine?")
	case !res.OK:
		return T("Die Engine lehnt diese Netze ab: %s", res.Error)
	}
	return ""
}
