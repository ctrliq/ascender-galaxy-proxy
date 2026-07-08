Galaxy Proxy
------------

A proxy / cache for downloading Ansible collections from Ansible Galaxy

Features
--------

- **On-disk caching** with configurable expiration for both API responses and artifact downloads
- **Prometheus metrics** endpoint (`/metrics`) to monitor cache performance and upstream server health
- **API token authentication** with optional `GALAXY_API_TOKEN` validation
- **Configurable upstream server** via `UPSTREAM_BASEURL` environment variable
- **Cache clearing on startup** option for fresh deployments
- **Full Galaxy API support** for v1, v2, and v3 endpoints
- **Artifact download handling** with 40MB size limits and caching
- **Trusted proxy support** for reverse proxy deployments


Installation
------------

The Galaxy Proxy is currently set to be installed in Kubernetes via the [Ascender Installer](https://github.com/ctrliq/ascender-install).  You can deploy the container itself via the example manifest, but will need to ensure any additional Ingresses, Environmental Vars, etc... are updated.  If you want to deploy in docker, then the typical run commands should work, just ensure your .env file it properly set up.


Run for Development
-------------------

1. docker compose build
2. docker compose up
or
1. docker compose up --build -d

The container will listen on port 80 for incoming requests and will either forward the request off to https://galaxy.ansible.com or return the previously cached response from disk.

Each cached response will live in the /app/.cache directory on the container.

Run Tests and Linter
--------------------

All commands must be run from the `src/` directory where `go.mod` lives:

```bash
cd src/

# Run the full test suite (matches CI exactly)
go test -v -count=1 -race ./...

# Run the linter (requires golangci-lint v2.11.4)
golangci-lint run

# Run go vet
go vet ./...
```

Always use `-count=1` to disable test caching and `-race` to enable the race detector — this matches what CI runs on every pull request.

Environment Variables
---------------------

The following environment variables can be configured in the `.env` file:

- `DEBUG` - Enable debug mode (default: `false`)
  - Set to `true` or `1` for verbose logging and debug mode, useful for development
  - Set to `false` or `0` (or leave unset) for production release mode with minimal logging

- `EXTERNAL_PORT` - External port for Docker container (default: `80`)
  - The internal application always listens on port 80
  - This controls the port mapping from host to container
  - Example: `EXTERNAL_PORT=8080` would map `8080:80` in Docker

- `QUERY_CACHE_EXPIRE` - Number of days to keep cached API query responses before expiring them (default: `1`)
  - API responses (for v1, v2, v3 endpoints) older than this value will be refreshed from upstream
  - Allows quick refreshing of metadata and collection information

- `ARTIFACT_CACHE_EXPIRE` - Number of days to keep cached artifact downloads before expiring them (default: `30`)
  - Artifact downloads (`.tar.gz` files) older than this value will be re-downloaded from upstream
  - Longer cache period since artifacts rarely change once published

- `TRUSTED_PROXIES` - Comma-separated list of IP addresses of trusted proxies (default: empty)
  - Example: `10.4.50.1,192.168.1.1`
  - Required if running behind a reverse proxy to get correct client IPs

- `GALAXY_API_TOKEN` - API token required for authentication (default: empty, no authentication)
  - If set, all requests must include `Authorization: Token <token>` header
  - If not set, authentication is not enforced and all requests are allowed
  - The Authorization header is validated but not forwarded to upstream servers
  - Example: `GALAXY_API_TOKEN=my_secret_token_123`

- `UPSTREAM_BASEURL` - Base URL of the upstream Galaxy server (default: `https://galaxy.ansible.com`)
  - Allows proxying to alternative Galaxy instances
  - Example: `UPSTREAM_BASEURL=https://galaxy.company.com`

- `URL` - The external URL of this proxy as seen by clients (required)
  - Used to rewrite upstream URLs in API responses so they point back to the proxy
  - Must be a valid HTTP or HTTPS URL including scheme and host
  - Example: `URL=https://galaxy-proxy.example.com`

- `CLEAR_CACHE_ON_START` - If set to `1` or `True`, clears the cache directory on startup (default: empty, no clearing)
  - Useful for fresh deployments or when you need to force cache invalidation
  - Metrics are also reset when cache is cleared
  - Example: `CLEAR_CACHE_ON_START=1`

- `MEM_CACHE_SIZE` - Maximum number of API responses to keep in the in-memory LRU cache (default: `2000`)
  - Higher values reduce disk I/O by keeping more responses in memory
  - Each entry holds a full API response, so memory usage scales with response sizes
  - Example: `MEM_CACHE_SIZE=5000`

- `HTTP_PROXY` - HTTP proxy URL for routing upstream requests through a corporate proxy (default: empty, no proxy)
  - Use this in enterprise environments where internet access requires going through an HTTP proxy
  - Must be a valid HTTP or HTTPS URL including scheme and optional port
  - Example: `HTTP_PROXY=http://corporate-proxy.company.com:8080`

Example `.env` file:
```
DEBUG=false
QUERY_CACHE_EXPIRE=1
ARTIFACT_CACHE_EXPIRE=30
TRUSTED_PROXIES=10.4.50.1
GALAXY_API_TOKEN=my_secret_token_123
UPSTREAM_BASEURL=https://galaxy.ansible.com
URL=https://galaxy-proxy.example.com
CLEAR_CACHE_ON_START=0
MEM_CACHE_SIZE=2000
HTTP_PROXY=
```



Clients
-------
### Ascender

To utilize the Galaxy Proxy in [Ascender](https://github.com/ctrliq/ascender).  First create a new Galaxy Credential.  In the Credential's Galaxy Server URL field, add the full url to the Galaxy Proxy instance, it should end in /api/ and look something like this.
`http://galaxy.local/api/`.  You can add a token to the API Token field, but if so, it must match what you set in GALAXY_API_TOKEN.  This will not be passed along to the upstream server and is instead used to authenticate to the proxy itself.

Once the credential is created, edit your Organization.  In the "Galaxy Credentials" field, click the magnifying glass.  Check the box next to your new credential, and then up top, change the order so that the Proxy credential is before the default Ansible Galaxy credential.  This is the order that it will check for collections in.  If the proxy is down, it will attempt to go straight to Ansible Galaxy.  If you want all requests to always go through the proxy (and not ever go straight to Galaxy) then uncheck the box next to the default Ansible Galaxy credential.

### Ansible Command Line
ansible-galaxy supports two methods for specifying the Galaxy server: providing the server URL with the -s option or configuring it in ansible.cfg.

```
$ ansible-galaxy collection install -s http://172.16.32.80/api/ community.vmware
```
