package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/control"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// Admin subcommands that change the configuration of the active event and apply it to the engine.

func adminInit(c *adminCall) int {
	preset := c.fs.String("preset", control.PresetLAN, "preset: "+control.Presets+" (lan: small example, netsession: a 14-table event, lab: the network lab)")
	parent := c.fs.String("parent", "trunk0", "trunk interface")
	force := c.fs.Bool("force", false, "replace an existing configuration")
	if !c.parse() {
		return 2
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	res, err := control.Init(c.ctx, st, c.common.user, *preset, *parent, *force)
	if err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "initialised with preset %s in mode observe (counts, sends nothing): %d games added; run `nsrelay admin apply`, "+
		"then `nsrelay admin mode active` and another apply to start relaying\n", *preset, res.Added)
	return 0
}

func adminSeed(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	seed, err := gamedb.Seed()
	if err != nil {
		return c.fail(err)
	}
	res, err := st.SyncSeed(c.ctx, seed)
	if err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "seed: %d added, %d updated, %d locally modified (seed differs)\n", res.Added, res.Updated, res.Pending)
	return 0
}

// adminEnable serves both `enable` and `disable`.
func adminEnable(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	if c.fs.NArg() == 0 {
		return c.fail(fmt.Errorf("admin %s: game id missing", c.name))
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	if err := setGamesEnabled(c, st, c.fs.Args(), c.name == "enable"); err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "%sd: %s (takes effect with `nsrelay admin apply`)\n", c.name, strings.Join(c.fs.Args(), ", "))
	return 0
}

// setGamesEnabled switches the games on or off, one after the other; it stops at the first error.
func setGamesEnabled(c *adminCall, st *store.Store, ids []string, on bool) error {
	for _, id := range ids {
		if err := st.SetGameEnabled(c.ctx, c.common.user, id, on); err != nil {
			return err
		}
	}
	return nil
}

func adminMode(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	if c.fs.NArg() != 1 {
		return c.fail(fmt.Errorf("admin mode: active|observe|paused"))
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	if err := st.SetMode(c.ctx, c.common.user, c.fs.Arg(0)); err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "mode %s (takes effect with `nsrelay admin apply`)\n", c.fs.Arg(0))
	return 0
}

func adminApply(c *adminCall) int {
	note := c.fs.String("note", "", "note for the revision")
	dry := c.fs.Bool("dry-run", false, "only compile and show warnings")
	if !c.parse() {
		return 2
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	res, err := control.Apply(c.ctx, st, c.engine(), c.common.user, *note, *dry)
	printApplyWarnings(c.stdout, res.Warnings)
	if err != nil {
		return c.fail(err)
	}
	s := res.Snapshot
	if *dry {
		fmt.Fprintf(c.stdout, "dry run: %d VLANs, %d rules, hash %s\n", len(s.VLANs), len(s.Rules), s.Hash[:12])
	} else {
		fmt.Fprintf(c.stdout, "applied revision %d: %d VLANs, %d rules, hash %s\n", res.Revision, len(s.VLANs), len(s.Rules), s.Hash[:12])
	}
	return 0
}

// printApplyWarnings prints the compiler's warnings except shared_port, which the command line leaves out.
func printApplyWarnings(w io.Writer, warnings []compile.Warning) {
	for _, wa := range warnings {
		if wa.Code != "shared_port" {
			fmt.Fprintln(w, "warning:", wa)
		}
	}
}

func adminRollback(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	id, err := strconv.ParseInt(c.fs.Arg(0), 10, 64)
	if err != nil {
		return c.fail(fmt.Errorf("admin rollback <revision-id>"))
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	res, err := control.Rollback(c.ctx, st, c.engine(), c.common.user, id)
	if err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "rolled back to revision %d as new revision %d (hash %s)\n", id, res.Revision, res.Snapshot.Hash[:12])
	return 0
}
