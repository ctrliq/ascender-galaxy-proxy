# Contributing to the Ascender Galaxy Proxy

Thanks for your interest in contributing to the Ascender Galaxy Proxy. This document covers the
development setup, testing, and pull request guidelines.

## Development setup

Fork and clone the repository:

```bash
git clone https://github.com/<your-user>/ascender-galaxy-proxy.git
cd ascender-galaxy-proxy
```

All Go commands run from `src/`, where `go.mod` lives.

```bash
cd src/
go build ./...
```

To run the service locally:

```bash
docker compose up --build
```

## Running tests

```bash
cd src/
go test -v -count=1 -race ./...
golangci-lint run
go vet ./...
```

Always pass `-count=1` to defeat test caching and `-race` to enable the
detector. This matches what CI runs on every pull request. The linter is
pinned to golangci-lint v2.11.4.


## Making changes

### Branching

Create a feature branch from `main`:

```bash
git checkout -b my-feature main
```

### Commit messages

Write clear, concise commit messages:

```
Short summary (under 72 characters)

Longer description of what changed and why, if needed.
```

## Submitting a PR

1. Make sure the checks above pass locally.
2. One logical change per PR. Do not bundle unrelated fixes.
3. Target the `main` branch.
4. Explain what changed and why in the PR description.

## Reporting issues

Open an issue at
[github.com/ctrliq/ascender-galaxy-proxy/issues](https://github.com/ctrliq/ascender-galaxy-proxy/issues).
Include the version you are running and the steps that reproduce the problem.

For security vulnerabilities, follow [SECURITY.md](./SECURITY.md) instead of
opening a public issue.
