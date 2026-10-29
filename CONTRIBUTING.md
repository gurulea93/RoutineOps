# Contributing to RoutineOps

Thanks for your interest in contributing! RoutineOps is a self-hosted MDM/RMM;
this repository is the open-core (Free) codebase.

## Dev setup
- **Go** — version pinned in [`go.mod`](./go.mod).
- **Web UI** — Node.js (see [`web/`](./web/)); `cd web && npm ci`.
- **Postgres** — integration tests need a reachable PostgreSQL (the CI spins one up;
  see [`.github/workflows/ci.yml`](./.github/workflows/ci.yml) for the exact config).

## Build & test (what CI enforces)
Server + agent (Go):
```sh
go build ./...
go vet ./...
go test -race ./...        # integration tests require Postgres
```
Web:
```sh
cd web && npm ci && npm run build && npm test && npm run lint
```
The agent must also cross-compile for every target OS:
```sh
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -o /dev/null ./cmd/agent
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./cmd/agent
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./cmd/agent
```

## Protocol (`proto/agent.proto`)
The proto is a shared contract between agents and server, and agents self-update —
old and new versions run simultaneously. Changes must be **backward-compatible**:
add fields only at the end, never change or reuse a field number, never delete a
field (mark it `reserved`). CI runs `buf breaking` against `main` on every PR.

## Pull requests
- Keep changes focused; one logical change per PR.
- Make sure `go build ./...`, `go vet ./...`, `go test ./...` and the web checks pass.
- Describe what changed and why.

## License
By contributing, you agree that your contributions are licensed under the
[Apache License 2.0](./LICENSE).
