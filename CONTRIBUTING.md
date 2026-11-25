# Contributing

Thanks for helping LAN parties find their games. Contributions are welcome, most of all **verified game ports**.

## The most useful contributions

1. **Game ports from a real capture.** Use the issue template *"New game / port correction"* or add the game
   yourself (next section). Say how you verified it: capture, source code, official docs. An honest `confidence`
   is more useful than a guess marked `high`.
2. **Reports from events:** which games worked, which did not, what the diagnosis page of the game said.
3. Bug fixes and features with tests.

## Adding a game

1. Find the ports. The **port finder** in the web UI records what a game sends while you open its LAN browser and
   turns the result into an entry you can export as YAML (button on the game page).
2. Add the entry to `games/games.yaml`. The fields are described in
   [docs/game-database.md](docs/game-database.md); the file is embedded into the binary and validated when it is
   loaded.
3. Run `go test ./internal/gamedb ./internal/compile`. The tests load the whole file and compile it, so a typo, a
   blocked port or a conflict with another game on the same port shows up here.
4. Open a pull request and name your sources in the `sources` field of the entry.

## Development

Go 1.26 or newer (`go.mod` names toolchain go1.27.4, which the go command fetches when needed), no CGO for the build. Everything CI runs can be run locally:

```sh
gofmt -l cmd internal games      # must print nothing
go mod tidy -diff                # must print nothing
go vet ./... && GOOS=windows go vet ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...
sh tools/complexity.sh               # complexity per file (see "Code shape")
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
go test ./...
CGO_ENABLED=1 go test -race ./...    # the race detector needs a C compiler
bash lab/run-docker.sh               # network lab: the real engine between network namespaces
sh tools/coverage.sh                 # coverage of unit tests and lab together (writes coverage.out)
qlty check --all                     # optional: the checks qlty.sh runs (.qlty/qlty.toml)
```

The lab script needs Docker (privileged containers) and builds the binary with the Go of your machine: Go 1.26 or
newer.

[docs/development.md](docs/development.md) explains the packages, the lab and the demo mode.

## Code shape

The code is kept small on purpose, so that a reader understands a function at one glance. CI enforces the numbers.

- **Functions:** a cyclomatic complexity of at most 7 (golangci-lint, `gocyclo`), 5 or less where it stays natural.
  Split along real steps: one check, one step of a sequence, one lookup, each with a name that says what it does.
  A `switch` or `select` that reads best as one list may stay as it is.
- **Files:** the complexity of all functions in a file sums up to at most 37 (`sh tools/complexity.sh`). A file that
  grows past it is split along its responsibilities into files of the same package (`live_hub.go`,
  `live_rates.go`), never into collections like `util2.go`.
- **Packet path** (`internal/engine`, `internal/pkt`): no allocation per frame and no measurable slowdown; every
  change there comes with the benchmarks before and after (`go test -run=^$ -bench=. -benchmem`). Here a function
  may keep a branch more than elsewhere if a further call costs time.
- **UI scripts** (`internal/web/static`): plain scripts with `defer`, no build step, no modules. Shared helpers
  live in `app-core.js` (`window.NSR`) and `w98-base.js` (`window.W98`); every part is a file of its own. The
  order of the `<script>` tags in `layout.html` is the load order. After a change to the UI, run the browser check
  `UI_CHECK=1 sh tools/screens/run.sh <dir>`: it opens the pages in both themes and fails on any script error.
- Tests are exempt from the limits; they should still read as a list of cases.

What a change should bring along:

- **Engine:** a case in `lab/test.sh` for every feature that touches packets or interfaces, next to the unit
  tests. The lab needs Linux with Docker (`--privileged`).
- **Store:** a new migration file in `internal/store/migrations/` (never edit a shipped one). A migration never
  removes or renames a table or column an older release needs, so the previous release still works on the
  database after a rollback; it may add tables, columns with a default and indexes, and move data into a new
  place. The upgrade test starts with the very first schema, fills it the way the first releases did and runs
  every migration over it. A change is stored together with its audit entry in one transaction (`audited` in
  `internal/store`).
- **Web UI:** texts are written in German in the templates and the Go code and translated through the English
  catalog in `internal/web/i18n/en-*.json` (German text → English text). Every new text needs its English entry;
  the tests render all pages in English and fail on a missing one. Texts used by the scripts (`app-*.js`) are also listed in
  `jsTexts` (`internal/web/i18n.go`). Use the design tokens (`internal/web/static/ns/tokens.css`), no CDN, no
  inline scripts, and keep every form working without JavaScript.
- **Errors:** packages below `internal/web` return errors with an identity (`errors.Is`, never a comparison of
  message texts); only the web part words them for the user (`internal/uimsg`). An error nobody mapped is an
  internal error: the user reads one general sentence and the detail goes to the log, not onto the page or into
  a URL.
- Code, identifiers, comments, commit messages and the documentation are in English. The exceptions are data,
  not code: the German source texts of the UI, the addresses of the pages (`/spiele`, `/netz`) and the four
  built-in group names (`tische`, `gameserver`, `wlan`, `orga`), which are stored in every database.
- No new dependency without a good reason.

## Pull requests

- One topic per pull request, with tests.
- CI must be green: formatting, a tidy `go.mod`, `go vet` for Linux and Windows, golangci-lint (`.golangci.yml`), the file complexity, govulncheck, the
  tests with and without the race detector, and the network lab.
- This repository is a published copy of Netsession's internal repository. Accepted pull requests are applied there
  and come back with the next publication, so your commit may appear with a different hash. You stay credited in
  the changelog and release notes.

## Reporting security issues

Please do not open public issues for security problems, see [SECURITY.md](SECURITY.md).

By contributing you agree that your contribution is licensed under the MIT license of this project.
