package main

import (
	"cmp"
	"context"
	"fmt"
	"io"

	"github.com/Netsession-LAN/Netsession-Relay/internal/control"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// runBackup writes a backup of the database (`nsrelay backup`) or puts one back (`nsrelay backup restore`).
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
		return backupRestore(af.db, fs.Args(), stdout, stderr)
	}
	return backupWrite(af.db, *dir, *keep, stdout, stderr)
}

// backupRestore replaces the database db with the backup named in args (exactly one file).
func backupRestore(db string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: nsrelay backup restore [-db path] <backup.db>   (stop the web part first)")
		return 2
	}
	old, err := control.Restore(db, args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "restored %s into %s (previous database kept as %s); run `nsrelay admin apply`\n", args[0], db, cmp.Or(old, "-"))
	return 0
}

// backupWrite writes a backup of the database db into dir and keeps the newest keep backups there.
func backupWrite(db, dir string, keep int, stdout, stderr io.Writer) int {
	st, err := store.Open(db)
	if err != nil {
		fmt.Fprintln(stderr, "database:", err)
		return 1
	}
	defer st.Close()
	path, err := control.Backup(context.Background(), st, dir, keep)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "backup written:", path)
	return 0
}
