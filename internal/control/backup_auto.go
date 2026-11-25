package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// Automatic backups: a new one whenever the newest is older than BackupEvery; only the newest few are kept.

// LastBackup returns the time of the newest backup in dir (from its file name), zero if there is none.
func LastBackup(dir string) time.Time {
	m, _ := backupFiles(dir)
	for i := len(m) - 1; i >= 0; i-- {
		stamp := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m[i]), "nsrelay-"), ".db")
		if len(stamp) > len(backupStamp) {
			stamp = stamp[:len(backupStamp)]
		}
		if t, err := time.Parse(backupStamp, stamp); err == nil {
			return t
		}
	}
	return time.Time{}
}

// BackupEvery is the rhythm of the automatic backup.
const BackupEvery = 24 * time.Hour

// AutoBackup writes a backup into dir whenever the newest one is older than BackupEvery (checked a minute after
// the start, then every hour) and keeps the newest keep. It replaces a timer on the host; backups written by
// `nsrelay backup` in between count, too. report gets every attempt. Returns when ctx ends.
func AutoBackup(ctx context.Context, st *store.Store, dir string, keep int, report func(path string, err error)) {
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = time.Hour
		if !backupDue(dir, st.Now()) {
			continue
		}
		path, err := Backup(ctx, st, dir, keep)
		report(path, err)
	}
}

// backupDue: no backup yet or the newest one is a day old (five minutes of slack for the hourly check).
func backupDue(dir string, now time.Time) bool {
	last := LastBackup(dir)
	return last.IsZero() || now.Sub(last) >= BackupEvery-5*time.Minute
}

func prune(dir string, keep int) error {
	if keep <= 0 {
		return nil
	}
	m, err := backupFiles(dir)
	if err != nil {
		return err
	}
	for len(m) > keep {
		old := m[0]
		m = m[1:]
		os.Remove(old)
		os.Remove(strings.TrimSuffix(old, ".db") + ".yaml")
	}
	return nil
}
