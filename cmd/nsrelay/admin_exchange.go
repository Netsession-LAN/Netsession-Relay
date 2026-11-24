package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"gopkg.in/yaml.v3"
)

// Export and import of the configuration of the active event as YAML.

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
	b, file, err := readImport(c.fs.Arg(0), c.stdin)
	if err != nil {
		return c.fail(err)
	}
	cfg, err := decodeImport(b, file)
	if err != nil {
		return c.fail(err)
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

// readImport reads the export to import from file, or from stdin (at most 16 MiB) for "-". name is how the
// messages call the source.
func readImport(file string, stdin io.Reader) (b []byte, name string, err error) {
	if file == "-" {
		b, err = io.ReadAll(io.LimitReader(stdin, 16<<20))
		return b, "standard input", err
	}
	b, err = os.ReadFile(file)
	return b, file, err
}

// decodeImport decodes a YAML export strictly (unknown fields are errors) and checks that it has an event and
// at least one VLAN; name is the source for the messages.
func decodeImport(b []byte, name string) (store.Config, error) {
	var cfg store.Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			err = errors.New("empty")
		}
		return cfg, fmt.Errorf("admin import: %s: %w", name, err)
	}
	if cfg.Event == "" || len(cfg.VLANs) == 0 {
		return cfg, fmt.Errorf("admin import: no event or no VLANs in %s", name)
	}
	return cfg, nil
}
