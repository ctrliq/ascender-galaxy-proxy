# Copilot Instructions — Ascender Galaxy Proxy

## Project Summary

Ascender Galaxy Proxy is a Go reverse-proxy/cache for Ansible Galaxy (`galaxy.ansible.com`). It caches API responses and artifact downloads to disk and an in-memory LRU cache, rewrites upstream URLs in JSON responses, and exposes Prometheus-style metrics. The web framework is **Gin**. There is a single Go source file and seven test files, all in `package main`.

## Repository Layout

```
.                              # repo root — NO go.mod here
├── .github/workflows/
│   ├── lint.yml               # golangci-lint v2.11.4 (runs on PRs)
│   ├── test.yml               # go test -v -count=1 -race ./... (runs on PRs)
│   ├── build.yml              # docker compose build + push (on push to main/devel/feature_*)
│   └── release.yml            # manual workflow: tag, draft release, push image
├── docker-compose.yml         # builds from src/Dockerfile, uses .env for config
├── .env.example               # template for .env (which is gitignored)
├── .env                       # gitignored — local env config
├── src/                       # ← Go module root (go.mod lives here)
│   ├── go.mod                 # module github.com/ctrliq/ascender-galaxy-proxy  (Go 1.25)
│   ├── go.sum
│   ├── ascender_galaxy_proxy.go   # ALL production code (~1400 lines, single file)
│   ├── helpers_test.go        # newTestProxy() helper, roundTripFunc, setUpstream()
│   ├── cache_test.go          # clearCacheDirectory, fetchAndCache, getUpstreamURL tests
│   ├── config_test.go         # getUpstreamBaseURL, getCacheExpire, getBaseURL, env helpers
│   ├── handlers_test.go       # Api, GalaxyHandler, ArtifactHandler, HealthzHandler tests
│   ├── lru_test.go            # LRU cache unit tests
│   ├── metrics_test.go        # metrics increment, flush, snapshot, Prometheus output tests
│   ├── util_test.go           # RawBytes, formatHashKey, rewriteBodyURLs, HTTP client tests
│   ├── Dockerfile             # multi-stage: golang:1.25 builder → alpine:3.23 runtime
│   └── .dockerignore
```

**Key architectural facts:**
- Everything is in one Go file (`src/ascender_galaxy_proxy.go`) in `package main`. All types, handlers, helpers, LRU cache, and `main()` are there.
- Test files are also `package main` (white-box tests with access to unexported symbols).
- `helpers_test.go` defines `newTestProxy(t)` — the standard way to create a `GalaxyProxy` for tests. Always use it.
- `helpers_test.go` also provides `roundTripFunc` and `setUpstream()` for mocking HTTP upstreams in tests.
- There is no `golangci-lint` config file; CI uses default settings with golangci-lint v2.11.4.
- Dependencies: `gin-gonic/gin`, `cespare/xxhash/v2`, `golang.org/x/sync` (singleflight). No ORM, no database.

## Build & Test Commands

**Always `cd src/` first.** The `go.mod` is in `src/`, not the repo root.

| Action | Command | Working dir | Notes |
|--------|---------|-------------|-------|
| Build | `go build -o ascender_galaxy_proxy ascender_galaxy_proxy.go` | `src/` | Fast (<5 s). Produces a single binary. |
| Test (CI-equivalent) | `go test -v -count=1 -race ./...` | `src/` | ~1 s. This is the exact command CI runs. Always use `-count=1` to avoid cached results and `-race` to match CI. |
| Vet | `go vet ./...` | `src/` | Runs as part of golangci-lint in CI. |
| Module tidy | `go mod tidy` | `src/` | Run after adding/removing imports. Verify `go.mod` and `go.sum` are unchanged if no deps were added. |
| Docker build | `docker compose build` | repo root | Produces the runtime image. |

### Validated command sequence for a typical change

```bash
cd src/
# 1. Build
go build -o /dev/null ascender_galaxy_proxy.go
# 2. Run tests (matches CI exactly)
go test -v -count=1 -race ./...
# 3. Vet
go vet ./...
```

All three commands pass cleanly on the current codebase.

## CI Checks (Run on Every PR)

1. **Lint** (`.github/workflows/lint.yml`): `golangci-lint` v2.11.4 with default config, working directory `src/`.
2. **Unit Tests** (`.github/workflows/test.yml`): `go test -v -count=1 -race ./...`, working directory `src/`, Go version from `src/go.mod`.

Both have a 10-minute timeout. Go version is read from `src/go.mod` (currently Go 1.25).

## Writing Tests

- All test files use `package main` — tests can access unexported types and functions directly.
- Use `newTestProxy(t)` from `helpers_test.go` to create a `GalaxyProxy` instance. It sets up a temp cache dir, default config, and an in-memory LRU cache.
- Use `setUpstream(g, serverURL, client)` to point the proxy at an `httptest.Server` for integration tests.
- Use `roundTripFunc` to create inline `http.RoundTripper` mocks.
- For handler tests, use `httptest.NewRecorder()` + `gin.CreateTestContext(w)`.
- Environment-dependent tests use `t.Setenv()` for safe env var overrides that auto-restore.
- Place new test functions in the existing `*_test.go` file that matches the area under test (cache, config, handlers, lru, metrics, util).

## Important Caveats

- **Single-file architecture**: All production code is in `src/ascender_galaxy_proxy.go`. When adding new functionality, add it to this file unless there is a strong reason to split.
- **No `.env` in repo**: `.env` is gitignored. Do not commit real credentials.
- **`URL` env var is required** at runtime. `getBaseURL()` returns an error if it is unset. Tests set `baseURL` directly on the struct.

## Trust These Instructions

These instructions have been validated against the current codebase. Trust the file paths, commands, and architectural descriptions above. Only perform additional exploration if something in these instructions is found to be incorrect or if you need information about a specific implementation detail not covered here.
