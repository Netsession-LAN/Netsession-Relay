package compile

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

// The selected games of the event: each one with its flow profile, overrides and source filter.

// addGame resolves one selected game: its flow profile, overrides and source filter, and what each of its
// discovery specs claims on its ports. Mistakes are collected in c.errs; a game with a mistake claims nothing.
func (c *compiler) addGame(sel Selection) {
	g, ok := c.selectGame(sel.GameID)
	if !ok {
		return
	}
	prof, ok := c.gameProfile(g.ID, sel.FlowProfile)
	if !ok {
		return
	}
	if err := checkOverride(sel.Override); err != nil {
		c.fail("game %q: %w", g.ID, err)
		return
	}
	sources := c.gameSources(g, sel.Sources)
	for _, raw := range g.Discovery {
		c.claim(g.ID, c.effectiveSpec(g, raw, sel.Override), prof.Edges, sources)
	}
}

// selectGame looks up a selected game. It reports false for a game that is unknown or selected twice (both
// mistakes) and for a game without LAN discovery (a warning: there is nothing to relay).
func (c *compiler) selectGame(id string) (gamedb.Game, bool) {
	g, ok := c.in.DB.Get(id)
	if !ok {
		c.fail("game %q not in database", id)
		return g, false
	}
	if c.seenGame[g.ID] {
		c.fail("game %q selected twice", g.ID)
		return g, false
	}
	c.seenGame[g.ID] = true
	if g.RelayNeeded != gamedb.RelayYes {
		c.warn(WarnGameNoRelay, "%s (%s) has no LAN discovery to relay (relay_needed=%s), skipped", g.Name, g.ID, g.RelayNeeded)
		return g, false
	}
	return g, true
}

// gameProfile returns the flow profile of a game ("" = the default profile). An undefined profile is a mistake
// (false); a profile that names a group without VLANs is a mistake, too, but the game still uses it.
func (c *compiler) gameProfile(gameID, name string) (FlowProfile, bool) {
	pname := cmp.Or(name, c.in.DefaultProfile)
	prof, ok := c.profiles[pname]
	if !ok {
		c.fail("game %q: flow profile %q not defined", gameID, pname)
		return prof, false
	}
	for _, e := range prof.Edges {
		for _, grp := range []string{e.Src, e.Dst} {
			if _, known := c.groups[grp]; !known {
				c.fail("flow profile %q: group %q has no VLAN", pname, grp)
			}
		}
	}
	return prof, true
}

// effectiveSpec is a discovery spec with its defaults and the overrides of the event applied. It warns when the
// override rewrites the sender of a game that needs the original one.
func (c *compiler) effectiveSpec(g gamedb.Game, raw gamedb.Spec, o Override) gamedb.Spec {
	s := raw.Effective()
	s.SourceMode = cmp.Or(o.SourceMode, s.SourceMode)
	s.DstMode = cmp.Or(o.DstMode, s.DstMode)
	if s.SourceMode != gamedb.SourceOriginal && g.SourceIP == gamedb.SourceOriginal {
		c.warn(WarnRelayIPRewrite, "%s needs the original sender IP, but source_mode=%s is set", g.ID, s.SourceMode)
	}
	return s
}

func checkOverride(o Override) error {
	if o.SourceMode != "" && !slices.Contains([]string{gamedb.SourceOriginal, gamedb.SourceRelay}, o.SourceMode) {
		return fmt.Errorf("override source_mode %q not supported", o.SourceMode)
	}
	if o.DstMode != "" && !slices.Contains([]string{gamedb.DstKeep, gamedb.DstSubnet, gamedb.DstLimited}, o.DstMode) {
		return fmt.Errorf("override dst_mode %q invalid", o.DstMode)
	}
	return nil
}
