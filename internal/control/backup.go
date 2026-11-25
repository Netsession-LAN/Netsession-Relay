package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// Backup writes a consistent SQLite copy (VACUUM INTO) and a readable YAML export of the active
// configuration into dir, keeping the newest keep backups. Returns the database copy path. Both files are
// readable by the owner only: the database holds password hashes and the secrets of the monitoring targets.
// The name carries the time to the second; a second backup within the same second gets a counter ("-2").
func Backup(ctx context.Context, st *store.Store, dir string, keep int) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	stamp := st.Now().UTC().Format(backupStamp)
	base, err := copyDatabase(ctx, st, dir, stamp)
	if err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	dbPath := base + ".db"
	if cfg, err := st.ExportConfig(ctx); err == nil {
		b, _ := yaml.Marshal(cfg)
		header := "# Netsession Relay configuration export " + stamp + " (readable copy; restore uses the .db file)\n"
		if err := os.WriteFile(base+".yaml", append([]byte(header), b...), 0o600); err != nil {
			return dbPath, err
		}
	}
	return dbPath, prune(dir, keep)
}

// backupStamp is the time in a backup's file name: nsrelay-<stamp>.db, nsrelay-<stamp>-<n>.db for a further
// backup within the same second.
const backupStamp = "20060102-150405"

// copyDatabase writes the database copy of a new backup and returns its path without the extension. SQLite
// would create the copy with the permissions the umask leaves (usually readable by everybody), so the file is
// created here, empty and private; VACUUM INTO accepts an empty file as its target.
func copyDatabase(ctx context.Context, st *store.Store, dir, stamp string) (string, error) {
	base, err := createBackupFile(dir, stamp)
	if err != nil {
		return "", err
	}
	if err := st.VacuumInto(ctx, base+".db"); err != nil {
		os.Remove(base + ".db")
		return "", err
	}
	return base, nil
}

// createBackupFile creates the empty, private database file of a new backup and returns its path without the
// extension. The file is created exclusively, so two backups at the same moment never share a name.
func createBackupFile(dir, stamp string) (string, error) {
	for n := 1; n <= 1000; n++ {
		base := filepath.Join(dir, "nsrelay-"+stamp)
		if n > 1 {
			base += fmt.Sprintf("-%d", n)
		}
		f, err := os.OpenFile(base+".db", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		return base, f.Close()
	}
	return "", fmt.Errorf("no free file name for nsrelay-%s in %s", stamp, dir)
}

// backupFiles lists the database copies in dir, oldest first. Sorted without the extension, by length first: a
// counter suffix makes the name longer, so "-2" follows the plain name and "-10" follows "-9".
func backupFiles(dir string) ([]string, error) {
	m, err := filepath.Glob(filepath.Join(dir, "nsrelay-*.db"))
	if err != nil {
		return nil, err
	}
	key := func(p string) (string, string) {
		name := strings.TrimSuffix(filepath.Base(p), ".db")
		stamp := name
		if len(stamp) > len("nsrelay-")+len(backupStamp) {
			stamp = stamp[:len("nsrelay-")+len(backupStamp)]
		}
		return stamp, name
	}
	sort.Slice(m, func(i, j int) bool {
		si, ni := key(m[i])
		sj, nj := key(m[j])
		if si != sj {
			return si < sj
		}
		if len(ni) != len(nj) {
			return len(ni) < len(nj)
		}
		return ni < nj
	})
	return m, nil
}

// BackupTemp writes a backup for a download into a fresh private directory and returns the database copy and a
// function that removes the directory again. The directory is created in the first of dirs that is writable,
// last in the temporary directory of the system: in the container the root file system is read-only, so /tmp
// may not exist as a writable place, while the backup directory and the data directory are volumes.
// Directories a crashed download left behind are removed when they are older than an hour.
func BackupTemp(ctx context.Context, st *store.Store, dirs ...string) (path string, cleanup func(), err error) {
	tmp, err := downloadDir(append(slices.Clone(dirs), os.TempDir()))
	if err != nil {
		return "", nil, fmt.Errorf("backup: no writable directory for the temporary copy: %w", err)
	}
	cleanup = func() { os.RemoveAll(tmp) }
	path, err = Backup(ctx, st, tmp, 0)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// downloadPrefix starts the name of a temporary download directory.
const downloadPrefix = ".nsrelay-download-"

// downloadDir creates a fresh private directory in the first writable one of dirs (empty entries are skipped),
// after removing the stale download directories there. It returns the error of the last attempt when none
// is writable.
func downloadDir(dirs []string) (tmp string, err error) {
	for _, d := range dirs {
		if d == "" {
			continue
		}
		removeStaleDownloads(d)
		if tmp, err = os.MkdirTemp(d, downloadPrefix); err == nil {
			return tmp, nil
		}
	}
	return "", err
}

// removeStaleDownloads removes download directories in d that a crashed download left behind (older than an
// hour).
func removeStaleDownloads(d string) {
	old, _ := filepath.Glob(filepath.Join(d, downloadPrefix+"*"))
	for _, o := range old {
		if fi, err := os.Stat(o); err == nil && fi.IsDir() && time.Since(fi.ModTime()) > time.Hour {
			os.RemoveAll(o)
		}
	}
}

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
