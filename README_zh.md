# Proxy-in-a-Box

[![Go](https://img.shields.io/badge/Go-1.23-00ADD8?logo=go)](https://go.dev)
[![Go Report Card](https://goreportcard.com/badge/github.com/naiba/proxyinabox)](https://goreportcard.com/report/github.com/naiba/proxyinabox)

自动化代理池，专为网页爬虫设计。通过 YAML 配置定义代理源，自动抓取和验证代理，提供 HTTP/HTTPS 代理服务，支持自动轮换、速率限制和 TLS 指纹伪装。

[English](README.md)

## 功能特性

- **YAML 驱动的数据源** — 所有代理源通过 YAML 配置定义，支持 Lua 脚本处理复杂逻辑
- **无头浏览器抓取** — 集成 [pinchtab](https://github.com/pinchtab/pinchtab)，处理 JS 渲染的页面（如 IPRoyal）
- **自动验证** — 并发代理验证，可配置工作线程数
- **智能轮换** — 基于域名和 IP 限制自动分配代理
- **速率限制** — 可配置每 IP 请求数和每 IP 域名数
- **TLS 指纹伪装** — 使用 uTLS 模拟 Chrome 浏览器指纹
- **MITM 支持** — 内置中间人代理处理 HTTPS 流量
- **SQLite 存储** — 轻量级嵌入式数据库，无外部依赖

## 快速开始

### Docker（推荐）

```bash
mkdir -p data/sources

# 下载默认配置和源定义
# pb.yaml — 主配置
# data/sources/*.yaml — 代理源定义
# 参见下方「配置说明」和「代理来源」章节

docker run -d --name proxy-in-a-box \
  -v ./data:/app/data \
  -p 8080:8080 -p 8081:8081 -p 8083:8083 \
  ghcr.io/naiba/proxy-in-a-box
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

参数:
  -c, --conf string   配置文件路径 (默认 "./data/pb.yaml")
  -p, --ha string     HTTP 代理服务地址 (默认 "127.0.0.1:8080")
  -s, --sa string     HTTPS 代理服务地址 (默认 "127.0.0.1:8081")
  -m, --ma string     管理 API 地址 (默认 "0.0.0.0:8083")
  -h, --help          帮助信息
```

在你的应用中配置代理：

```
HTTP 代理:  http://127.0.0.1:8080
HTTPS 代理: https://127.0.0.1:8081
```

管理 API：

```
GET /stat  — 代理池统计
GET /get   — 获取一个可用代理
```

## 配置说明

`data/pb.yaml`：

```yaml
debug: true

sys:
  name: MyProxy
  proxy_verify_worker: 20    # 并发验证工作线程数
  domains_per_ip: 30         # 30 分钟内每个 IP 可访问的最大域名数
  request_limit_per_ip: 10   # 每秒每个 IP 的最大请求数
  verify_duration: 30        # 代理重新验证间隔（分钟）

# 无头浏览器配置（可选）
# 需要 pinchtab 二进制 — Docker 镜像已内置
pinchtab:
  bin: pinchtab              # 二进制路径（留空则禁用）
  port: "9867"               # 监听端口
```

## 代理来源

代理源是 `data/sources/` 目录下的 YAML 文件，支持三种类型：

### `text` — 纯文本 IP:Port 列表

```yaml
name: thespeedx-http
type: text
url: "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/http.txt"
protocol: http
interval: 5m
```

### `json` — JSON API + 字段路径提取

```yaml
name: proxyscrape
type: json
url: "https://api.proxyscrape.com/v3/free-proxy-list/get?request=displayproxies&format=json"
ip_field: "proxies.*.ip"
port_field: "proxies.*.port"
protocol_field: "proxies.*.protocol"
interval: 5m
```

### `script` — Lua 脚本处理复杂逻辑

Lua 内置函数：`fetch(url, headers?)`、`sleep(ms)`、`json_decode(str)`、`json_encode(table)`、`browser_fetch(url)`、`browser_eval(expression)`

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

### 浏览器抓取（JS 渲染页面）

需要配置 `pinchtab`。`browser_fetch(url)` 导航无头浏览器并返回渲染后的 HTML。`browser_eval(expression)` 在已加载的页面上执行 JavaScript。

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

### 内置数据源

| 来源 | 类型 | 方式 |
|------|------|------|
| TheSpeedX (http/socks4/socks5) | text | GitHub 原始文件 |
| ProxyScrape | json | 公开 API |
| GeoNode | json | 公开 API |
| 快代理 | script | 网页抓取 + JSON 提取 |
| ProxyRack | script | API |
| Monosans | text | GitHub 原始文件 |
| IPRoyal | script | 无头浏览器 (pinchtab) |

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
                    │  pinchtab ←──── Chrome (无头模式)       │
                    └─────────────────────────────────────────┘
                                     │
                                     ▼
                              ┌─────────────┐
                              │   SQLite    │
                              └─────────────┘
```

## 性能测试

```bash
ab -v4 -n100 -c10 -X 127.0.0.1:8080 http://api.ip.la/cn
```

## 技术栈

- **语言**：Go 1.23
- **数据库**：SQLite（`glebarez/sqlite` + GORM）
- **脚本引擎**：gopher-lua（Lua 5.1 VM）
- **浏览器**：[pinchtab](https://github.com/pinchtab/pinchtab) + Chromium
- **TLS**：uTLS 指纹伪装
- **HTTP**：标准库 + 自定义 MITM 代理

## 许可证

MIT
