# Ascender Galaxy Proxy

[![CI](https://github.com/ctrliq/ascender-galaxy-proxy/actions/workflows/test.yml/badge.svg)](https://github.com/ctrliq/ascender-galaxy-proxy/actions/workflows/test.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](./LICENSE)
[![Go](https://img.shields.io/badge/go-%3E%3D1.25-blue.svg)](https://go.dev)

A caching proxy for Ansible Galaxy, written in Go. It sits between your automation and `galaxy.ansible.com`, serving collection metadata and artifacts from disk so that job runs stay fast, survive an upstream outage, and work inside networks with restricted internet access.

## Requirements

- Go 1.25 or newer, to build from source
- Docker with Compose, to run the container
- A DNS name for the proxy, since clients are handed URLs that point back to it

## Installation

The proxy is normally deployed to Kubernetes by the [Ascender installer](https://github.com/ctrliq/ascender-install). To run it standalone, use the example manifest, or Compose:

```bash
docker compose up --build -d
```

The container listens on port 80 and caches into `/app/.cache`.

## Using the proxy

Point `ansible-galaxy` at it directly:

```bash
ansible-galaxy collection install -s http://galaxy.local/api/ community.vmware
```

To use it from Ascender, create a Galaxy credential whose server URL is the proxy address ending in `/api/`. Then edit your Organization, add the credential under Galaxy Credentials, and order it ahead of the default Ansible Galaxy entry. Remove the default entry to force every request through the proxy.

## Authentication

Authentication is optional and off by default:

- Set `GALAXY_API_TOKEN` to require `Authorization: Token <token>` on every request
- Leave it unset to accept all requests without authentication
- The header is validated by the proxy and never forwarded upstream

## Configuration

All configuration is by environment variable, read from `.env` in Compose deployments. See [`.env.example`](./.env.example) for a template.

| Variable | Default | Purpose |
| -------- | ------- | ------- |
| `URL` | required | External URL of the proxy, used to rewrite upstream links |
| `UPSTREAM_BASEURL` | `https://galaxy.ansible.com` | Galaxy server to proxy |
| `QUERY_CACHE_EXPIRE` | `1` | Days to keep cached API responses |
| `ARTIFACT_CACHE_EXPIRE` | `30` | Days to keep cached artifact downloads |
| `MEM_CACHE_SIZE` | `2000` | API responses held in the in-memory LRU cache |
| `CLEAR_CACHE_ON_START` | unset | Clear the cache and reset metrics on startup |
| `TRUSTED_PROXIES` | empty | Comma-separated reverse proxy IPs, for correct client IPs |
| `HTTP_PROXY` | empty | Corporate HTTP proxy for upstream requests |
| `GALAXY_API_TOKEN` | empty | Token required from clients, when set |
| `EXTERNAL_PORT` | `80` | Host port mapped to the container |
| `DEBUG` | `false` | Verbose logging and debug mode |

## Included content

- **Full Galaxy API coverage** across the v1, v2, and v3 endpoint families
- **Two-tier caching**: an in-memory LRU in front of an on-disk artifact cache
- **Prometheus metrics** at `/metrics`, covering cache hits and upstream health

## Testing

All commands run from `src/`, where `go.mod` lives.

- **Tests**: `go test -v -count=1 -race ./...`
- **Lint**: `golangci-lint run`, requires golangci-lint v2.11.4
- **Vet**: `go vet ./...`

Always pass `-count=1` to defeat test caching and `-race` to enable the detector, which is what CI runs on every pull request.

## The Ascender ecosystem

| Repository | Description |
| ---------- | ----------- |
| [ascender](https://github.com/ctrliq/ascender) | The platform itself: web UI, REST API, and task engine |
| [ascender-install](https://github.com/ctrliq/ascender-install) | Installer for Ascender and Ledger, with Galaxy Proxy support |
| [ascender-k8s-install](https://github.com/ctrliq/ascender-k8s-install) | Kubernetes installer for Ascender, Ledger, and React |
| [ascender-pro-install](https://github.com/ctrliq/ascender-pro-install) | Enhanced installer adding Reaqt, Registry, and Galaxy Proxy |
| [ascender-operator](https://github.com/ctrliq/ascender-operator) | Kubernetes operator that deploys and manages Ascender |
| [ascender-ee](https://github.com/ctrliq/ascender-ee) | Default execution environment image for Ascender jobs |
| [ascender-kit](https://github.com/ctrliq/ascender-kit) | The `ascender` command line client and Python API library |
| [ascender-collection](https://github.com/ctrliq/ascender-collection) | The `ctrliq.ascender` Ansible collection for a controller |
| [ascender-ledger](https://github.com/ctrliq/ascender-ledger) | Reporting tool for host facts and playbook changes |
| [ascender-galaxy-proxy](https://github.com/ctrliq/ascender-galaxy-proxy) | Caching proxy for Ansible Galaxy collection downloads |
| [ascender-playbooks](https://github.com/ctrliq/ascender-playbooks) | Example playbooks for use with Ascender |
## Contributing

- See [CONTRIBUTING.md](./CONTRIBUTING.md) for development setup, testing, and pull requests.
- Report bugs and feature ideas via [GitHub Issues](https://github.com/ctrliq/ascender-galaxy-proxy/issues).
- For security vulnerabilities, follow [SECURITY.md](./SECURITY.md) rather than opening an issue.
- Join the [Ascender forum](https://forum.ascender-automation.org) to discuss development topics.

## License

Licensed under the **Apache License 2.0**. See [LICENSE](./LICENSE) for the full text.
