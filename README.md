# Proxy-in-a-Box

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](https://go.dev)
[![Go Report Card](https://goreportcard.com/badge/github.com/naiba/proxyinabox)](https://goreportcard.com/report/github.com/naiba/proxyinabox)

Automatic proxy pool for web scraping. Crawls proxies from YAML-defined sources, validates them, and provides HTTP/HTTPS proxy servers with automatic rotation and TLS fingerprint spoofing.

[中文说明](README_zh.md)

## Features

- **YAML-driven sources** — All proxy sources defined as YAML configs with Lua scripting for complex logic
- **Headless browser scraping** — Integrated [Obscura](https://github.com/h4ckf0r0day/obscura) with anti-detection enabled by default for complex JS-rendered pages
- **Auto-validation** — Concurrent proxy verification with configurable worker pool
- **Smart rotation** — Automatic proxy assignment based on domain and IP limits
- **TLS fingerprint spoofing** — Uses uTLS to mimic Chrome browser fingerprints
- **MITM support** — Built-in man-in-the-middle proxy for HTTPS traffic
- **SQLite storage** — Lightweight embedded database, no external dependencies

## Quick Start

### Docker (Recommended)

```yaml
# docker-compose.yml
services:
  proxy-in-a-box:
    image: ghcr.io/naiba/proxy-in-a-box
    restart: unless-stopped
    volumes:
      - ./data:/app/data
    ports:
      - "8080:8080"   # HTTP proxy
      - "8081:8081"   # HTTPS proxy
      - "8083:8083"   # Dashboard + API
```

### From Source

```bash
go install github.com/naiba/proxyinabox/cmd/proxy-in-a-box@latest
mkdir -p data/sources
# Create data/pb.yaml and data/sources/*.yaml (see below)
proxy-in-a-box
```

## Usage

```
Usage:
  proxy-in-a-box [flags]
  proxy-in-a-box [command]

Available Commands:
  test-source    Test a single proxy source YAML file (fetch + verify availability)

Flags:
  -c, --conf string   config file (default "./data/pb.yaml")
  -p, --ha string     http proxy server addr (default "0.0.0.0:8080")
  -s, --sa string     https proxy server addr (default "0.0.0.0:8081")
  -m, --ma string     management/dashboard addr (default "0.0.0.0:8083")
  -h, --help          help for proxy-in-a-box
```

### Test a Source

```bash
proxy-in-a-box test-source data/sources/my-source.yaml [-w 20]
```

Fetches proxies from the specified source YAML file and verifies their availability. Use `-w` to set concurrent verification workers (default: 20).

Configure your application to use the proxy:

```
HTTP Proxy:  http://127.0.0.1:8080
HTTPS Proxy: https://127.0.0.1:8081
```

Management Dashboard & API:

```
GET /             — Web dashboard (pool overview, proxy list, source status)
GET /stat         — Pool statistics (plain text)
GET /get          — Get one available proxy
GET /api/stats    — Pool statistics (JSON: available/quarantined totals, by protocol/source, blocked IPs, request stats)
GET /api/proxies  — Full proxy list (JSON)
GET /api/sources  — Source fetch statuses (JSON)
```

## Configuration

`data/pb.yaml`:

```yaml
debug: true

sys:
  name: MyProxy
  proxy_verify_worker: 20    # concurrent verification workers

# HTTPS MITM decryption (default: false)
# When enabled, the proxy decrypts HTTPS traffic using a self-signed CA — clients must disable TLS verification or trust the CA.
# When disabled (default), HTTPS CONNECT requests are tunneled as-is — clients use standard TLS verification.
enable_mitm: false

# Upstream proxy resilience (all optional; defaults shown)
upstream:
  max_attempts: 3              # distinct exits tried for GET/HEAD/OPTIONS/TRACE and HTTPS CONNECT
  connect_timeout: 5s          # TCP connection timeout to an upstream proxy
  handshake_timeout: 7s        # CONNECT/SOCKS/TLS handshake timeout
  response_header_timeout: 12s # response-header timeout
  request_timeout: 20s         # total timeout for one upstream HTTP attempt
  target_failure_ttl: 10m      # circuit-breaker TTL for one proxy/target pair

# Proxy health checks (all optional; defaults shown)
verification:
  interval: 2h                 # routine checks for available proxies
  deep_check_interval: 24h     # additional TLS interception probe
  retries: 2                   # attempts per health-check round
  response_body_limit: 16384   # maximum health-check response bytes

# Headless browser for JS-rendered pages (optional)
# Requires Obscura v0.2.1+ with the stealth build feature — included in the Docker image
# Proxy-in-a-Box starts Obscura with --stealth by default.
obscura:
  bin: obscura                # binary path (leave empty to use PATH default)
```

Failed endpoints back off for 30 minutes, 2 hours, 6 hours, and then 24 hours. Repeated candidates returned by a source respect the same delay instead of consuming bandwidth on every source refresh.

## Proxy Sources

Sources are YAML files in `data/sources/`. Three types supported:

### `text` — Plain text IP:Port lists

```yaml
name: example-text
type: text
url: "https://proxy-source.example/http.txt"
protocol: http
interval: 5m
```

### `json` — JSON API with field paths

```yaml
name: example-json
type: json
url: "https://proxy-source.example/api/proxies"
ip_field: "proxies.*.ip"
port_field: "proxies.*.port"
protocol_field: "proxies.*.protocol"
interval: 5m
```

### `script` — Lua scripts for complex logic

Lua globals: `fetch(url, headers?)`, `sleep(ms)`, `json_decode(str)`, `json_encode(table)`, `browser_fetch(url)`, `browser_eval(expression)`

```yaml
name: example-script
type: script
interval: 10m
script: |
  local proxies = {}
  for page = 1, 5 do
    sleep(3000)
    local body = fetch("https://proxy-source.example/pages/" .. page)
    if body then
      local match = string.match(body, "fpsList = (.-);%s*\n")
      if match then
        local list = json_decode(match)
        if list then
          for _, item in ipairs(list) do
            proxies[#proxies+1] = {ip = item.ip, port = item.port, protocol = "http"}
          end
        end
      end
    end
  end
  return proxies
```

### Browser-powered scraping (for JS-rendered pages)

Uses `obscura` config. `browser_fetch(url)` navigates the headless browser and returns rendered HTML. `browser_eval(expression)` executes JavaScript on the loaded page. Proxy-in-a-Box starts the stealth-enabled Obscura build with `--stealth` and aligns Obscura's navigation, script, fetch, and CDP deadlines with the 60-second client timeout.

```yaml
name: example-browser
type: script
interval: 30m
script: |
  local proxies = {}
  local html = browser_fetch("https://proxy-source.example/rendered-list")
  if not html then return proxies end
  local raw = browser_eval([[(function(){
    var rows = document.querySelectorAll('div.grid.min-w-\\[600px\\]');
    var r = [];
    for (var i = 0; i < rows.length; i++) {
      var ch = rows[i].children;
      if (ch.length >= 3) {
        var ip = ch[0].textContent.trim();
        if (/^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(ip))
          r.push({ip: ip, port: ch[1].textContent.trim(), protocol: ch[2].textContent.trim().toLowerCase()});
      }
    }
    return JSON.stringify(r);
  })()]])
  if raw then
    local data = json_decode(raw)
    if data then
      for _, item in ipairs(data) do
        proxies[#proxies+1] = {ip = item.ip, port = item.port, protocol = item.protocol}
      end
    end
  end
  return proxies
```

## Architecture

```
                    ┌─────────────────────────────────────────┐
                    │           Proxy-in-a-Box                │
                    ├─────────────────────────────────────────┤
 Your App ────────► │  HTTP Proxy :8080 / HTTPS Proxy :8081  │
                    ├─────────────────────────────────────────┤
                    │              Proxy Pool                 │
                    │   ┌─────┐ ┌─────┐ ┌─────┐ ┌─────┐      │
                    │   │ IP1 │ │ IP2 │ │ IP3 │ │ ... │      │
                    │   └─────┘ └─────┘ └─────┘ └─────┘      │
                    ├─────────────────────────────────────────┤
                    │  YAML Sources   │ Validators            │
                    │  text/json/lua  │ (concurrent workers)  │
                    ├─────────────────────────────────────────┤
                    │       Obscura (headless browser)         │
                    └─────────────────────────────────────────┘
                                     │
                                     ▼
                              ┌─────────────┐
                              │   SQLite    │
                              └─────────────┘
```

## Benchmark

```bash
ab -v4 -n100 -c10 -X 127.0.0.1:8080 https://target.example/resource
```

## Tech Stack

- **Language**: Go 1.25
- **Database**: SQLite (via `glebarez/sqlite` + GORM)
- **Scripting**: gopher-lua (Lua 5.1 VM)
- **Browser**: [Obscura](https://github.com/h4ckf0r0day/obscura)
- **TLS**: uTLS for fingerprint spoofing
- **HTTP**: Standard library + custom MITM proxy

## License

MIT
