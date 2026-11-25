package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
)

// The English catalog: German text -> English text, merged from i18n/en-*.json.

//go:embed i18n/*.json
var i18nFS embed.FS

// catalogEN maps German texts to English; loaded once from all i18n/en-*.json files.
var catalogEN = mustCatalog()

func mustCatalog() map[string]string {
	c, err := loadCatalog(i18nFS)
	if err != nil {
		panic(err)
	}
	return c
}

// loadCatalog merges the English catalog files. The same German text with two different translations is an error.
func loadCatalog(fsys fs.FS) (map[string]string, error) {
	files, err := fs.Glob(fsys, "i18n/en-*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	out := map[string]string{}
	from := map[string]string{} // file of each entry, for the error message
	for _, f := range files {
		m, err := readCatalogFile(fsys, f)
		if err != nil {
			return nil, err
		}
		if err := mergeCatalog(out, from, m, f); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// mergeCatalog adds the entries m of file to out; from records the file of every entry.
func mergeCatalog(out, from, m map[string]string, file string) error {
	for de, en := range m {
		if old, ok := out[de]; ok && old != en {
			return fmt.Errorf("%q translated twice: %q (%s) and %q (%s)", de, old, from[de], en, file)
		}
		out[de], from[de] = en, file
	}
	return nil
}

// readCatalogFile reads one catalog file (German text -> English text).
func readCatalogFile(fsys fs.FS, name string) (map[string]string, error) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return m, nil
}
