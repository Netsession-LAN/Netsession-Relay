package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/auth"
	"github.com/Netsession-LAN/Netsession-Relay/internal/control"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"

	"gopkg.in/yaml.v3"
)

const defaultDB = "/var/lib/nsrelay-web/nsrelay.db"

// common flags of admin/backup subcommands
type adminFlags struct {
	db, socket, user string
}

func (a *adminFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&a.db, "db", envOr("NSRELAY_DB", defaultDB), "SQLite database")
	fs.StringVar(&a.socket, "socket", envOr("NSRELAY_SOCKET", engineapi.DefaultSocket), "engine API socket")
	fs.StringVar(&a.user, "user", defaultUser(), "user name for audit log and revisions")
}

func envOr(k, def string) string {
	return cmp.Or(os.Getenv(k), def)
}

func defaultUser() string {
	if u, err := user.Current(); err == nil {
		return "cli:" + u.Username
	}
	return "cli"
}

const adminUsage = `usage: nsrelay admin <command> [flags] [arguments]

  init [-preset lan|netsession|lab] [-parent trunk0] [-force]  create configuration (seed games + preset, mode observe);
                                                           refuses an existing configuration without -force
  status                                                   engine status (via socket)
  games [-enabled]                                         list games
  enable <game-id>...  /  disable <game-id>...             switch games in the active event
  vlans                                                    list VLANs
  mode active|observe|paused                               engine mode for the next apply
  apply [-note text] [-dry-run]                            compile and send to the engine (new revision)
  revisions                                                list revisions
  rollback <revision-id>                                   restore and apply an earlier revision
  audit                                                    audit log
  export [file|-]                                          configuration of the active event as YAML (stdout or file)
  import <file|->                                          replace VLANs, profiles and event games from a YAML export
                                                           (- = standard input); then apply
  set-password [-role admin|operator|viewer] <name>        local account (emergency login); prints a new password once
  logout <name>                                            end all sessions of an account (local or single sign-on)
  seed                                                     sync games.yaml into the database
  finder-start [-vlans 101,102] [-src IP] [-seconds 300]   port finder session (via socket)
  finder                                                   port finder results
  finder-stop                                              end the session early
  finder-pcap <file|->                                     capture of the last session (Wireshark; - = standard output)
  probe                                                    server probe now, then list the servers that answered

common flags: -db (env NSRELAY_DB), -socket (env NSRELAY_SOCKET), -user; flags go before the arguments

In the container only /backups is writable; write files there or use "-" with a redirect on the host:
  docker compose exec -T web nsrelay admin export - > relay.yaml
  docker compose exec -T web nsrelay admin import - < relay.yaml
  docker compose exec -T web nsrelay admin finder-pcap - > capture.pcap`

// adminCall is one invocation of an admin subcommand: its own flag set (with the common flags registered), the
// output streams and a context that ends after a minute.
type adminCall struct {
	ctx            context.Context
	name           string // subcommand
	args           []string
	fs             *flag.FlagSet
	common         adminFlags
	stdout, stderr io.Writer
	stdin          io.Reader
}

// parse parses the flags of the subcommand; false means the flag package already printed the error.
func (c *adminCall) parse() bool { return c.fs.Parse(c.args) == nil }

// fail prints err and returns the exit code for a failed command.
func (c *adminCall) fail(err error) int {
	fmt.Fprintln(c.stderr, err)
	return 1
}

// usage prints how the subcommand is called and returns the exit code for a usage error.
func (c *adminCall) usage(text string) int {
	fmt.Fprintln(c.stderr, "usage: nsrelay admin "+text)
	return 2
}

// store opens the database; the caller closes it. On failure the error is printed and ok is false.
func (c *adminCall) store() (st *store.Store, ok bool) {
	st, err := store.Open(c.common.db)
	if err != nil {
		fmt.Fprintln(c.stderr, "database:", err)
		return nil, false
	}
	return st, true
}

func (c *adminCall) engine() *engineapi.Client { return engineapi.NewClient(c.common.socket) }

// adminCommands maps the subcommands of `nsrelay admin` to their functions. Each one registers its own flags
// on c.fs, calls c.parse and returns the exit code.
var adminCommands = map[string]func(c *adminCall) int{
	"init":         adminInit,
	"status":       adminStatus,
	"games":        adminGames,
	"enable":       adminEnable,
	"disable":      adminEnable,
	"vlans":        adminVLANs,
	"mode":         adminMode,
	"apply":        adminApply,
	"revisions":    adminRevisions,
	"rollback":     adminRollback,
	"audit":        adminAudit,
	"export":       adminExport,
	"import":       adminImport,
	"set-password": adminSetPassword,
	"logout":       adminLogout,
	"seed":         adminSeed,
	"finder-start": adminFinderStart,
	"finder":       adminFinderShow,
	"finder-stop":  adminFinderStop,
	"finder-pcap":  adminFinderPcap,
	"probe":        adminProbe,
}

// adminStdin is what `admin import -` reads; the tests replace it.
var adminStdin io.Reader = os.Stdin

func runAdmin(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, adminUsage)
		return 2
	}
	run, ok := adminCommands[args[0]]
	if !ok {
		fmt.Fprintln(stderr, adminUsage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c := &adminCall{ctx: ctx, name: args[0], args: args[1:], fs: newFlags("admin "+args[0], stderr), stdout: stdout, stderr: stderr, stdin: adminStdin}
	c.common.register(c.fs)
	return run(c)
}

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

func adminLogout(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	if c.fs.NArg() != 1 {
		return c.usage("logout <name>")
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	name := c.fs.Arg(0)
	n, err := st.Logout(c.ctx, c.common.user, name)
	if err != nil {
		return c.fail(fmt.Errorf("logout %s: %w", name, err))
	}
	fmt.Fprintf(c.stdout, "%s: %d session(s) ended\n", name, n)
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

func adminGames(c *adminCall) int {
	onlyEnabled := c.fs.Bool("enabled", false, "only enabled games")
	if !c.parse() {
		return 2
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	games, err := st.Games(c.ctx)
	if err != nil {
		return c.fail(err)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ON\tID\tNAME\tRELAY\tCONF\tPORTS")
	for _, g := range games {
		if *onlyEnabled && !g.Enabled {
			continue
		}
		var ports []string
		for _, d := range g.Discovery {
			p := d.Ports
			if d.Group != "" {
				p = d.Group + ":" + p
			}
			ports = append(ports, p)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", mark(g.Enabled), g.ID, g.Name, g.RelayNeeded, g.Confidence, strings.Join(ports, " "))
	}
	return flush(c, tw)
}

// mark is the "ON" column of the tables.
func mark(on bool) string {
	if on {
		return "x"
	}
	return ""
}

func flush(c *adminCall, tw *tabwriter.Writer) int {
	if err := tw.Flush(); err != nil {
		return c.fail(err)
	}
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
	for _, id := range c.fs.Args() {
		if err := st.SetGameEnabled(c.ctx, c.common.user, id, c.name == "enable"); err != nil {
			return c.fail(err)
		}
	}
	fmt.Fprintf(c.stdout, "%sd: %s (takes effect with `nsrelay admin apply`)\n", c.name, strings.Join(c.fs.Args(), ", "))
	return 0
}

func adminVLANs(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	cfg, err := st.ExportConfig(c.ctx)
	if err != nil {
		return c.fail(err)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ON\tVID\tNAME\tGROUP\tNETWORK\tRELAY IP\tINTERFACE")
	for _, v := range cfg.VLANs {
		ifn := v.Device
		if v.Attach == "trunk" {
			ifn = fmt.Sprintf("%s.%d", v.Parent, v.VID)
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\n", mark(v.Enabled), v.VID, v.Name, v.Group, v.CIDR, v.RelayIP, ifn)
	}
	return flush(c, tw)
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
	for _, w := range res.Warnings {
		if w.Code != "shared_port" {
			fmt.Fprintln(c.stdout, "warning:", w)
		}
	}
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

func adminRevisions(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	revs, err := st.Revisions(c.ctx, 30)
	if err != nil {
		return c.fail(err)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTIME\tUSER\tOK\tHASH\tNOTE")
	for _, r := range revs {
		applied := "yes"
		if !r.AppliedOK {
			applied = "NO: " + r.EngineMessage
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.TS[:19], r.User, applied, r.Hash[:12], r.Note)
	}
	return flush(c, tw)
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

func adminSetPassword(c *adminCall) int {
	role := c.fs.String("role", "admin", "role of the account: admin|operator|viewer")
	if !c.parse() {
		return 2
	}
	if c.fs.NArg() != 1 {
		return c.fail(fmt.Errorf("admin set-password [-role admin|operator|viewer] <name>"))
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	name := c.fs.Arg(0)
	pw := auth.Password()
	hash, err := auth.Hash(pw)
	if err != nil {
		return c.fail(err)
	}
	if err := st.SetLocalUser(c.ctx, c.common.user, name, hash, *role); err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "account %q (%s) - password (shown only now): %s\n", name, *role, pw)
	return 0
}

func adminAudit(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	log, err := st.AuditLog(c.ctx, 50)
	if err != nil {
		return c.fail(err)
	}
	for _, e := range log {
		fmt.Fprintf(c.stdout, "%s  %-18s %-16s %s\n", e.TS[:19], e.User, e.Action, e.Object)
	}
	return 0
}

func adminExport(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	cfg, err := st.ExportConfig(c.ctx)
	if err != nil {
		return c.fail(err)
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return c.fail(err)
	}
	// no file or "-": standard output, and nothing but the YAML on it
	if c.fs.NArg() == 0 || c.fs.Arg(0) == "-" {
		if _, err := c.stdout.Write(b); err != nil {
			return c.fail(err)
		}
		return 0
	}
	if err := os.WriteFile(c.fs.Arg(0), b, 0o600); err != nil {
		return c.fail(writeHint(err))
	}
	fmt.Fprintln(c.stdout, "written:", c.fs.Arg(0))
	return 0
}

// writeHint adds where a file can be written when the target is on the read-only file system of the container.
func writeHint(err error) error {
	if errors.Is(err, syscall.EROFS) {
		return fmt.Errorf("%w (in the container only /backups is writable; \"-\" writes to standard output)", err)
	}
	return err
}

func adminImport(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	if c.fs.NArg() != 1 {
		return c.fail(fmt.Errorf("admin import: file missing (- = standard input)"))
	}
	file := c.fs.Arg(0)
	var b []byte
	var err error
	if file == "-" {
		file = "standard input"
		b, err = io.ReadAll(io.LimitReader(c.stdin, 16<<20))
	} else {
		b, err = os.ReadFile(file)
	}
	if err != nil {
		return c.fail(err)
	}
	var cfg store.Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			err = errors.New("empty")
		}
		return c.fail(fmt.Errorf("admin import: %s: %w", file, err))
	}
	if cfg.Event == "" || len(cfg.VLANs) == 0 {
		return c.fail(fmt.Errorf("admin import: no event or no VLANs in %s", file))
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	if err := st.ImportConfig(c.ctx, c.common.user, cfg); err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "imported event %q (%d VLANs, %d profiles, %d games); run `nsrelay admin apply`\n",
		cfg.Event, len(cfg.VLANs), len(cfg.Profiles), len(cfg.Games))
	return 0
}

func adminStatus(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	st, err := c.engine().Status(c.ctx)
	if err != nil {
		return c.fail(err)
	}
	out := c.stdout
	fmt.Fprintf(out, "engine %s, started %s\n", st.Version, st.Started.Format(time.RFC3339))
	fmt.Fprintf(out, "mode %s, sending %v, standby %v, snapshot %s, %d VLANs, %d rules\n",
		cmp.Or(st.Mode, "(none)"), st.Sending, st.Standby, shortHash(st.SnapshotHash), st.VLANs, st.Rules)
	fmt.Fprintf(out, "rx %d, sent %d, reasons %v\n", st.RX, st.Sent, st.Reasons)
	// Per network: does its interface deliver frames at all? (A silent one points at the trunk, the VLAN tag or
	// the interface, not at the rules.) And per rule: did it see frames, did it relay them?
	for _, v := range st.VLANStats {
		last := "never"
		if !v.LastIn.IsZero() {
			last = v.LastIn.Format(time.RFC3339)
		}
		fmt.Fprintf(out, "vlan %d %s (%s): in %d, out %d, last in %s\n", v.VID, v.Name, v.Interface, v.In, v.Out, last)
	}
	for _, r := range st.RuleStats {
		if r.RX > 0 {
			fmt.Fprintf(out, "rule %s: rx %d, relayed %d\n", r.ID, r.RX, r.Relayed)
		}
	}
	for _, a := range st.Alarms {
		fmt.Fprintf(out, "ALARM %s %s x%d: %s\n", a.Kind, a.Key, a.Count, a.Detail)
	}
	for _, p := range st.TopUnknown {
		fmt.Fprintf(out, "unknown broadcast port %d: %d\n", p.Port, p.Count)
	}
	// what the engine could not prepare on the host (no routing on relay interfaces, firewall, management routes)
	for _, is := range st.HostIssues {
		fmt.Fprintf(out, "HOST %s: %s\n", is.Code, is.Detail)
	}
	if len(st.HostIssues) == 0 && st.Mode != "" {
		fmt.Fprintln(out, "host prepared")
	}
	return 0
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return cmp.Or(h, "-")
}

func runBackup(args []string, stdout, stderr io.Writer) int {
	restore := len(args) > 0 && args[0] == "restore"
	if restore {
		args = args[1:]
	}
	fs := newFlags("backup", stderr)
	var af adminFlags
	af.register(fs)
	dir := fs.String("dir", envOr("NSRELAY_BACKUP_DIR", "/backups"), "backup directory")
	keep := fs.Int("keep", 14, "number of backups to keep")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if restore {
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: nsrelay backup restore [-db path] <backup.db>   (stop the web part first)")
			return 2
		}
		old, err := control.Restore(af.db, fs.Arg(0))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "restored %s into %s (previous database kept as %s); run `nsrelay admin apply`\n", fs.Arg(0), af.db, cmp.Or(old, "-"))
		return 0
	}
	st, err := store.Open(af.db)
	if err != nil {
		fmt.Fprintln(stderr, "database:", err)
		return 1
	}
	defer st.Close()
	path, err := control.Backup(context.Background(), st, *dir, *keep)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "backup written:", path)
	return 0
}
