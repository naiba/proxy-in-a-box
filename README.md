# Proxy-in-a-Box

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](https://go.dev)
[![Go Report Card](https://goreportcard.com/badge/github.com/naiba/proxyinabox)](https://goreportcard.com/report/github.com/naiba/proxyinabox)

Automatic proxy pool for web scraping. Crawls proxies from YAML-defined sources, validates them, and provides HTTP/HTTPS proxy servers with automatic rotation and TLS fingerprint spoofing.

[中文说明](README_zh.md)

## Features

- **YAML-driven sources** — All proxy sources defined as YAML configs with Lua scripting for complex logic
- **Headless browser scraping** — Integrated [Lightpanda](https://github.com/lightpanda-io/browser) for JS-rendered pages (e.g. IPRoyal)
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
GET /api/stats    — Pool statistics (JSON: totals, by protocol/source, blocked IPs, request stats)
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

# Headless browser for JS-rendered pages (optional)
# Requires lightpanda binary — included in Docker image
lightpanda:
  bin: lightpanda             # binary path (leave empty to disable)
```

## Proxy Sources

Sources are YAML files in `data/sources/`. Three types supported:

### `text` — Plain text IP:Port lists

```yaml
name: thespeedx-http
type: text
url: "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/http.txt"
protocol: http
interval: 5m
```

### `json` — JSON API with field paths

```yaml
name: proxyscrape
type: json
url: "https://api.proxyscrape.com/v3/free-proxy-list/get?request=displayproxies&format=json"
ip_field: "proxies.*.ip"
port_field: "proxies.*.port"
protocol_field: "proxies.*.protocol"
interval: 5m
```

### `script` — Lua scripts for complex logic

Lua globals: `fetch(url, headers?)`, `sleep(ms)`, `json_decode(str)`, `json_encode(table)`, `browser_fetch(url)`, `browser_eval(expression)`

```yaml
name: kuaidaili
type: script
interval: 10m
script: |
  local proxies = {}
  for page = 1, 5 do
    sleep(3000)
    local body = fetch("https://www.kuaidaili.com/free/inha/" .. page)
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

Requires `lightpanda` config. `browser_fetch(url)` navigates the headless browser and returns rendered HTML. `browser_eval(expression)` executes JavaScript on the loaded page.

```yaml
name: iproyal
type: script
interval: 30m
script: |
  local proxies = {}
  local html = browser_fetch("https://iproyal.com/free-proxy-list/")
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
                    │       Lightpanda (headless browser)      │
                    └─────────────────────────────────────────┘
                                     │
                                     ▼
                              ┌─────────────┐
                              │   SQLite    │
                              └─────────────┘
```

## Benchmark

```bash
ab -v4 -n100 -c10 -X 127.0.0.1:8080 http://api.ip.la/cn
```

## Tech Stack

- **Language**: Go 1.25
- **Database**: SQLite (via `glebarez/sqlite` + GORM)
- **Scripting**: gopher-lua (Lua 5.1 VM)
- **Browser**: [Lightpanda](https://github.com/lightpanda-io/browser)
- **TLS**: uTLS for fingerprint spoofing
- **HTTP**: Standard library + custom MITM proxy

## License

MIT
