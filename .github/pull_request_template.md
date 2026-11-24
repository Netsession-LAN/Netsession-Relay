## What and why

## How it was tested

- [ ] `gofmt -l cmd internal games` prints nothing, `go mod tidy -diff` is clean
- [ ] `go vet ./...`, golangci-lint and govulncheck as in [CONTRIBUTING.md](../CONTRIBUTING.md)
- [ ] `go test ./...` and `CGO_ENABLED=1 go test -race ./...`
- [ ] Engine change: lab case added or updated, `bash lab/run-docker.sh` green
- [ ] Game database change: sources named in `sources`, honest `confidence`
- [ ] UI change: works without JavaScript, design tokens only, German source text plus its English entry in
      `internal/web/i18n/en-*.json`
