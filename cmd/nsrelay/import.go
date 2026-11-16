package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Netsession2024/Netsession-Relay/internal/gamedb"
	"github.com/Netsession2024/Netsession-Relay/internal/opnsense"
)

// runImport: nsrelay import opnsense <config.xml>... - report only, nothing is changed or stored.
func runImport(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "opnsense" {
		fmt.Fprintln(stderr, "usage: nsrelay import opnsense <config.xml> [<config.xml> ...]   (report only; files are not stored)")
		return 2
	}
	db, err := gamedb.Seed()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var boxes []opnsense.Box
	for _, path := range args[1:] {
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		b, err := opnsense.Parse(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), f)
		f.Close()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		boxes = append(boxes, b)
	}
	opnsense.BuildReport(boxes, db).WriteText(stdout)
	return 0
}
