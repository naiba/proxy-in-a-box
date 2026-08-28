# Proxy-in-a-Box

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](https://go.dev)
[![Go Report Card](https://goreportcard.com/badge/github.com/naiba/proxyinabox)](https://goreportcard.com/report/github.com/naiba/proxyinabox)

自动化代理池，专为网页爬虫设计。通过 YAML 配置定义代理源，自动抓取和验证代理，提供 HTTP/HTTPS 代理服务，支持自动轮换和 TLS 指纹伪装。

[English](README.md)

## 功能特性

- **YAML 驱动的数据源** — 所有代理源通过 YAML 配置定义，支持 Lua 脚本处理复杂逻辑
- **无头浏览器抓取** — 集成 [Obscura](https://github.com/h4ckf0r0day/obscura)，默认开启防检测能力，处理复杂的 JS 渲染页面
- **自动验证** — 并发代理验证，可配置工作线程数
- **智能轮换** — 基于域名和 IP 限制自动分配代理
- **TLS 指纹伪装** — 使用 uTLS 模拟 Chrome 浏览器指纹
- **MITM 支持** — 内置中间人代理处理 HTTPS 流量
- **SQLite 存储** — 轻量级嵌入式数据库，无外部依赖

## 快速开始

### Docker（推荐）

```yaml
# docker-compose.yml
services:
  proxy-in-a-box:
    image: ghcr.io/naiba/proxy-in-a-box
    restart: unless-stopped
    volumes:
      - ./data:/app/data
    ports:
      - "8080:8080"   # HTTP 代理
      - "8081:8081"   # HTTPS 代理
      - "8083:8083"   # Dashboard + API
```

### 从源码安装

```bash
go install github.com/naiba/proxyinabox/cmd/proxy-in-a-box@latest
mkdir -p data/sources
# 创建 data/pb.yaml 和 data/sources/*.yaml（参见下方说明）
proxy-in-a-box
```

## 使用方法

```
用法:
  proxy-in-a-box [flags]
  proxy-in-a-box [command]

可用命令:
  test-source    测试单个代理源 YAML 文件（抓取 + 验证可用性）

参数:
  -c, --conf string   配置文件路径 (默认 "./data/pb.yaml")
  -p, --ha string     HTTP 代理服务地址 (默认 "0.0.0.0:8080")
  -s, --sa string     HTTPS 代理服务地址 (默认 "0.0.0.0:8081")
  -m, --ma string     管理面板/API 地址 (默认 "0.0.0.0:8083")
  -h, --help          帮助信息
```

### 测试数据源

```bash
proxy-in-a-box test-source data/sources/my-source.yaml [-w 20]
```

从指定的源 YAML 文件抓取代理并验证其可用性。使用 `-w` 设置并发验证工作线程数（默认 20）。

在你的应用中配置代理：

```
HTTP 代理:  http://127.0.0.1:8080
HTTPS 代理: https://127.0.0.1:8081
```

管理面板和 API：

```
GET /             — Web 管理面板（代理池概览、代理列表、数据源状态）
GET /stat         — 代理池统计（纯文本）
GET /get          — 获取一个可用代理
GET /api/stats    — 代理池统计（JSON：可用/隔离数量、按协议/来源分类、封禁 IP、请求统计）
GET /api/proxies  — 全量代理列表（JSON）
GET /api/sources  — 各数据源抓取状态（JSON）
```

## 配置说明

`data/pb.yaml`：

```yaml
debug: true

sys:
  name: MyProxy
  proxy_verify_worker: 20    # 并发验证工作线程数

# HTTPS 中间人解密（默认关闭）
# 开启后代理会用自签 CA 解密 HTTPS 流量，客户端需关闭 TLS 验证或信任该 CA
# 关闭时（默认），HTTPS CONNECT 请求直接隧道透传，客户端使用标准 TLS 验证
enable_mitm: false

# 上游代理容错（均可省略，以下为默认值）
upstream:
  max_attempts: 3              # GET/HEAD/OPTIONS/TRACE 及 HTTPS CONNECT 最多尝试的不同出口数
  connect_timeout: 5s          # 连接上游代理的超时
  handshake_timeout: 7s        # CONNECT/SOCKS/TLS 握手超时
  response_header_timeout: 12s # 等待响应头超时
  request_timeout: 20s         # 单次 HTTP 上游请求总超时
  target_failure_ttl: 10m      # 代理与特定目标组合失败后的熔断时间

# 代理健康检查（均可省略，以下为默认值）
verification:
  interval: 2h                 # 可用代理的普通复检间隔
  deep_check_interval: 24h     # 额外 TLS 劫持探测间隔
  retries: 2                   # 单轮验证最多尝试次数
  response_body_limit: 16384   # 验证响应体上限（字节）

# 无头浏览器配置（可选）
# 需要带 stealth 构建特性的 Obscura v0.2.1+ — Docker 镜像已内置
# Proxy-in-a-Box 默认使用 --stealth 启动 Obscura。
obscura:
  bin: obscura                # 二进制路径（留空则使用 PATH 默认命令）
```

失败端点会按 30 分钟、2 小时、6 小时、24 小时逐级退避；代理源重复返回同一不可用端点时也会遵守该退避，避免反复消耗服务器流量。

## 代理来源

代理源是 `data/sources/` 目录下的 YAML 文件，支持三种类型：

### `text` — 纯文本 IP:Port 列表

```yaml
name: example-text
type: text
url: "https://proxy-source.example/http.txt"
protocol: http
interval: 5m
```

### `json` — JSON API + 字段路径提取

```yaml
name: example-json
type: json
url: "https://proxy-source.example/api/proxies"
ip_field: "proxies.*.ip"
port_field: "proxies.*.port"
protocol_field: "proxies.*.protocol"
interval: 5m
```

### `script` — Lua 脚本处理复杂逻辑

Lua 内置函数：`fetch(url, headers?)`、`sleep(ms)`、`json_decode(str)`、`json_encode(table)`、`browser_fetch(url)`、`browser_eval(expression)`

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

### 浏览器抓取（JS 渲染页面）

使用 `obscura` 配置。`browser_fetch(url)` 导航无头浏览器并返回渲染后的 HTML。`browser_eval(expression)` 在已加载的页面上执行 JavaScript。Proxy-in-a-Box 默认使用带 stealth 构建特性的 Obscura 并传入 `--stealth`，同时将 Obscura 的导航、脚本、网络和 CDP 超时与客户端的 60 秒超时对齐。

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

## 架构

```
                    ┌─────────────────────────────────────────┐
                    │           Proxy-in-a-Box                │
                    ├─────────────────────────────────────────┤
 你的应用 ────────► │  HTTP 代理 :8080 / HTTPS 代理 :8081    │
                    ├─────────────────────────────────────────┤
                    │                代理池                   │
                    │   ┌─────┐ ┌─────┐ ┌─────┐ ┌─────┐      │
                    │   │ IP1 │ │ IP2 │ │ IP3 │ │ ... │      │
                    │   └─────┘ └─────┘ └─────┘ └─────┘      │
                    ├─────────────────────────────────────────┤
                    │  YAML 数据源    │ 验证器                │
                    │  text/json/lua  │ (并发工作线程)        │
                    ├─────────────────────────────────────────┤
                    │       Obscura（无头浏览器）              │
                    └─────────────────────────────────────────┘
                                     │
                                     ▼
                              ┌─────────────┐
                              │   SQLite    │
                              └─────────────┘
```

## 性能测试

```bash
ab -v4 -n100 -c10 -X 127.0.0.1:8080 https://target.example/resource
```

## 技术栈

- **语言**：Go 1.25
- **数据库**：SQLite（`glebarez/sqlite` + GORM）
- **脚本引擎**：gopher-lua（Lua 5.1 VM）
- **浏览器**：[Obscura](https://github.com/h4ckf0r0day/obscura)
- **TLS**：uTLS 指纹伪装
- **HTTP**：标准库 + 自定义 MITM 代理

## 许可证

MIT
