# Contributing

Thanks for helping LAN parties find their games. Contributions are welcome — most of all **verified game ports**.

## The most useful contributions

1. **Game ports from a real capture.** Use the issue template *"New game / port correction"* or edit
   `games/games.yaml` directly (format in [docs/game-database.md](docs/game-database.md)). Say how you verified it:
   capture, source code, official docs. Honest `confidence` is more useful than a guess marked `high`.
2. **Reports from events:** which games worked, which did not, what the UI's diagnosis said.
3. Bug fixes and features with tests.

## Development

```sh
go test ./...
gofmt -l cmd internal games      # must be empty
bash lab/run-docker.sh           # network lab, for anything touching the engine
```

See [docs/development.md](docs/development.md). Rules of thumb:

- Every engine feature gets a lab case in `lab/test.sh`.
- UI: Netsession brand tokens only, no CDN, no inline scripts, forms must work without JavaScript, German UI texts.
- Code, identifiers and comments in English; UI texts and most project docs in German.
- Keep functions small and packages focused; no new dependencies without a good reason.

## Pull requests

- One topic per pull request, with tests.
- CI must be green (`gofmt`, `go vet` for Linux and Windows, `go test`).
- This repository is a published copy of Netsession's internal repository. Accepted pull requests are applied there
  and come back with the next publication, so your commit may appear with a different hash — you stay credited in
  the changelog and release notes.

## Reporting security issues

Please do not open public issues for security problems, see [SECURITY.md](SECURITY.md).

By contributing you agree that your contribution is licensed under the MIT license of this project.
