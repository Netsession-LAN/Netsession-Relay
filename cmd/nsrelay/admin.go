package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
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
