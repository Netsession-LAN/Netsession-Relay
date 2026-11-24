// Package games embeds the seed game database games.yaml (see the header of that file for the format).
package games

import _ "embed"

// SeedYAML is the content of games.yaml.
//
//go:embed games.yaml
var SeedYAML []byte
