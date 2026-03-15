# gorge-conduit 技术报告

## 1. 概述

gorge-conduit 是 Gorge 平台中的 Conduit API 网关微服务，为 Phorge（Phabricator 社区维护分支）提供 Conduit API 的统一接入层。

该服务作为轻量级 HTTP 反向代理部署在客户端（其他 Go 微服务、gorge-worker 等）与 Phorge PHP 应用之间，提供 Service Token 认证和基于 IP 的 Token Bucket 限流能力。所有对 Phorge Conduit API 的调用都通过此网关统一管控，实现认证、限流和请求审计的集中处理。

## 2. 设计动机

### 2.1 原有方案的问题

Phorge 的 Conduit API 原生暴露在 PHP 应用上，各调用方直接访问 PHP 端点：

1. **缺乏统一认证**：每个调用方需要独立管理与 Phorge 的认证凭据，缺乏服务间认证的统一控制点。
2. **无限流保护**：PHP 应用直接承受所有 Conduit 请求，无法对高频调用方进行限速，存在被单个异常客户端拖垮的风险。
3. **缺少请求审计**：散落在各调用方的请求日志难以集中分析，无法全局观察 API 调用模式。
4. **上游耦合**：多个 Go 微服务各自实现 HTTP 客户端配置（超时、重试等），代码重复且难以统一调整。

### 2.2 gorge-conduit 的解决思路

将 Conduit API 调用收敛到一个专用网关：

- **统一认证**：通过 Service Token 控制哪些服务可以调用 Conduit API，一个配置点管理所有访问权限。
- **流量保护**：基于客户端 IP 的 Token Bucket 限流，保护上游 PHP 应用免受过载。
- **集中日志**：所有 Conduit 调用在网关层统一记录方法名、状态码、耗时和客户端 IP。
- **解耦上游**：调用方只需知道网关地址，上游 PHP 地址变更时只需修改网关配置。

## 3. 系统架构

### 3.1 在 Gorge 平台中的位置

```
┌──────────────────────────────────────────────────┐
│                   Gorge 平台                      │
│                                                   │
│  ┌──────────┐  ┌──────────┐  ┌───────────────┐   │
│  │  gorge-  │  │  gorge-  │  │ 其他 Go 服务   │   │
│  │  worker  │  │  db-api  │  │               │   │
│  └────┬─────┘  └────┬─────┘  └───────┬───────┘   │
│       │              │                │           │
│       └──────────────┼────────────────┘           │
│                      │                            │
│                      ▼                            │
│          ┌───────────────────────┐                │
│          │   gorge-conduit      │                │
│          │   :8150              │                │
│          │                     │                │
│          │   Token Auth        │                │
│          │   Rate Limiter      │                │
│          │   Reverse Proxy     │                │
│          └──────────┬──────────┘                │
│                     │                            │
│                     ▼                            │
│          ┌───────────────────────┐                │
│          │   Phorge (PHP)       │                │
│          │   :80                │                │
│          │   /api/*             │                │
│          └───────────────────────┘                │
└──────────────────────────────────────────────────┘
```

### 3.2 模块划分

项目采用 Go 标准布局，分为三个内部模块：

| 模块 | 路径 | 职责 |
|---|---|---|
| config | `internal/config/` | 环境变量配置加载与解析 |
| gateway | `internal/gateway/` | 反向代理和 Token Bucket 限流器 |
| httpapi | `internal/httpapi/` | HTTP 路由注册、认证中间件与请求处理 |

入口程序 `cmd/server/main.go` 负责串联三个模块：加载配置 -> 创建代理 -> 创建限流器（可选） -> 启动 HTTP 服务。

### 3.3 请求处理流水线

一个 Conduit API 请求经过的完整处理链路：

```
客户端请求 POST /api/conduit.search
       │
       ▼
┌─ Echo 框架层 ─────────────────────────────────┐
│  RequestLogger  记录请求日志                     │
│       │                                        │
│       ▼                                        │
│  Recover        捕获 panic，防止进程崩溃          │
│       │                                        │
│       ▼                                        │
│  BodyLimit      拒绝超过 MaxBodySize 的请求体     │
└───────┼────────────────────────────────────────┘
        │
        ▼
┌─ 路由组 /api/:method ─────────────────────────┐
│  ServiceTokenAuth  恒定时间比较校验 Token         │
│       │                                        │
│       ▼                                        │
│  RateLimiter.Middleware  Token Bucket 限流       │
│       │                                        │
│       ▼                                        │
│  Proxy.Handle  构建上游请求并转发                  │
└───────┼────────────────────────────────────────┘
        │
        ▼
  上游 Phorge PHP /api/conduit.search
        │
        ▼
  响应原样返回客户端（头、状态码、Body）
```

## 4. 核心实现分析

### 4.1 反向代理

反向代理实现位于 `internal/gateway/proxy.go`，是整个服务最核心的模块。

#### 4.1.1 Proxy 结构体

```go
type Proxy struct {
    upstream string
    client   *http.Client
}
```

`Proxy` 持有两个字段：去除尾部斜杠的上游 URL 和一个带超时的 HTTP 客户端。`http.Client` 在整个服务生命周期内复用，利用其内置的连接池管理上游连接。

#### 4.1.2 请求转发流程

`Handle` 方法的执行步骤：

1. **提取方法名**：从 Echo 路由参数 `:method` 获取 Conduit 方法名（如 `conduit.ping`）。方法名为空时返回 400。
2. **构建目标 URL**：拼接 `{upstream}/api/{method}`，并保留原始 query string。
3. **构建代理请求**：使用 `http.NewRequestWithContext` 创建新请求，传入原始请求的 Context（保证上下文取消能传播到上游）、HTTP 方法和请求体。
4. **复制请求头**：通过 `copyHeaders` 函数复制客户端请求头，同时过滤 8 个 Hop-by-Hop 头（`Connection`、`Keep-Alive`、`Proxy-Authenticate`、`Proxy-Authorization`、`Te`、`Trailer`、`Transfer-Encoding`、`Upgrade`）。
5. **注入转发头**：设置 `X-Forwarded-For`（客户端真实 IP）、`X-Forwarded-Proto`（原始协议）和 `X-Conduit-Gateway: go-conduit`（标识经过了 Go 网关）。
6. **发送请求**：通过共享的 `http.Client` 发送到上游。
7. **复制响应**：将上游的响应头逐一复制到客户端响应，写入状态码，通过 `io.Copy` 流式传输响应体。

#### 4.1.3 重定向策略

```go
CheckRedirect: func(req *http.Request, via []*http.Request) error {
    return http.ErrUseLastResponse
}
```

`http.Client` 的 `CheckRedirect` 被设置为返回 `http.ErrUseLastResponse`，即不自动跟随重定向。这是反向代理的标准做法——重定向响应应原样返回给客户端，由客户端决定是否跟随，而非代理私自跟随后返回最终响应。

#### 4.1.4 Hop-by-Hop 头过滤

```go
var hopByHopHeaders = map[string]bool{
    "Connection":          true,
    "Keep-Alive":          true,
    "Proxy-Authenticate":  true,
    "Proxy-Authorization": true,
    "Te":                  true,
    "Trailer":             true,
    "Transfer-Encoding":   true,
    "Upgrade":             true,
}
```

根据 HTTP/1.1 规范（RFC 2616 Section 13.5.1），Hop-by-Hop 头仅在单跳连接之间有效，代理不应转发这些头。`copyHeaders` 函数在复制请求头时跳过这 8 个头，确保代理行为符合 HTTP 规范。

#### 4.1.5 错误处理

代理层的错误统一返回 Phorge Conduit 兼容的 JSON 格式：

```json
{
  "result": null,
  "error_code": "ERR-CONDUIT-PROXY",
  "error_info": "Upstream request failed."
}
```

区分两种错误场景：请求构建失败和上游请求失败，都返回 502 Bad Gateway，并在服务端日志中记录详细错误信息和耗时。

### 4.2 Token Bucket 限流器

限流器实现位于 `internal/gateway/ratelimit.go`，采用经典的 Token Bucket 算法，按客户端 IP 独立限流。

#### 4.2.1 数据结构

```go
type visitor struct {
    tokens     float64    // 当前可用令牌数
    lastSeen   time.Time  // 上次访问时间
    maxTokens  float64    // 桶容量（= burst）
    refillRate float64    // 每秒填充速率（= rps）
}

type RateLimiter struct {
    mu       sync.Mutex
    visitors map[string]*visitor  // key: 客户端 IP
    rps      float64
    burst    int
    exempt   map[string]bool      // 豁免的 Conduit 方法
    done     chan struct{}         // 停止信号
}
```

每个客户端 IP 对应一个独立的 `visitor`，互不影响。

#### 4.2.2 Token Bucket 算法实现

`Allow(clientIP, method)` 方法的核心逻辑：

1. **快速路径**：`rps <= 0` 或方法在豁免列表中，直接放行。
2. **首次访问**：为新 IP 创建 `visitor`，初始令牌数等于 `burst`（满桶）。
3. **令牌补充**：根据距上次访问的时间差计算应补充的令牌数 `elapsed * refillRate`，不超过桶容量 `maxTokens`。
4. **令牌消耗**：令牌 >= 1 则消耗一个并放行，否则拒绝。

这种"惰性补充"的实现方式避免了后台定时器为每个 visitor 补充令牌的开销——只在实际请求到来时按时间差一次性补充，计算复杂度为 O(1)。

#### 4.2.3 并发安全

整个 `visitors` map 通过 `sync.Mutex` 保护。选择 `Mutex` 而非 `RWMutex` 是因为 `Allow` 方法既有读操作（查找 visitor）也有写操作（更新 tokens 和 lastSeen），几乎每次调用都需要写锁，`RWMutex` 的读写分离优势不大。

#### 4.2.4 内存保护

```go
const maxVisitors = 100_000
```

`visitors` map 设置了 100,000 的容量上限。当 map 满时，新 IP 的请求会被直接拒绝。这防止了攻击者通过大量不同 IP 的请求消耗服务器内存（HashDoS 变体）。

#### 4.2.5 过期清理

```go
func (rl *RateLimiter) cleanupLoop() {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()
    for {
        select {
        case <-rl.done:
            return
        case <-ticker.C:
            rl.mu.Lock()
            cutoff := time.Now().Add(-10 * time.Minute)
            for ip, v := range rl.visitors {
                if v.lastSeen.Before(cutoff) {
                    delete(rl.visitors, ip)
                }
            }
            rl.mu.Unlock()
        }
    }
}
```

后台 goroutine 每 5 分钟执行一次清理，删除 10 分钟内无活动的 visitor。这保证了即使没有 `maxVisitors` 限制，map 也不会无限增长。选择 5 分钟间隔和 10 分钟过期是一个平衡：足够频繁地回收内存，又不会因过于频繁的清理加锁影响正常请求处理。

通过 `done` channel 和 `Stop()` 方法实现优雅停止，避免主进程退出时 goroutine 泄漏。

#### 4.2.6 方法级豁免

某些 Conduit 方法（如 `conduit.ping`、`conduit.getcapabilities`）是健康检查或能力探测调用，不应受限流影响。通过 `RATE_LIMIT_EXEMPT` 环境变量配置豁免列表，在 `Allow` 方法中通过 `exempt` map 快速判断并直接放行。

### 4.3 Service Token 认证

认证中间件实现位于 `internal/httpapi/handlers.go`。

#### 4.3.1 恒定时间比较

```go
if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(deps.Token)) != 1 {
    return c.JSON(http.StatusUnauthorized, ...)
}
```

使用 `crypto/subtle.ConstantTimeCompare` 进行 Token 比较，而非普通的 `==` 运算符。普通字符串比较会在发现第一个不匹配字符时立即返回，攻击者可以通过测量响应时间差异逐字节猜测 Token（时序攻击）。`ConstantTimeCompare` 无论 Token 是否匹配，比较耗时恒定，从根本上消除了这一攻击向量。

#### 4.3.2 双通道 Token 获取

Token 支持两种传递方式：

1. **请求头**：`X-Service-Token: <token>`——标准方式，适合服务间调用。
2. **查询参数**：`?token=<token>`——备用方式，适合调试或某些无法自定义请求头的场景。

优先检查请求头，未找到时再检查查询参数。

#### 4.3.3 可选认证

当 `SERVICE_TOKEN` 环境变量为空时，中间件直接调用 `next(c)` 放行所有请求。这使得开发和测试环境无需配置 Token 即可使用。

### 4.4 路由设计

```go
func RegisterRoutes(e *echo.Echo, deps *Deps) {
    e.GET("/", healthPing())
    e.GET("/healthz", healthPing())

    g := e.Group("/api/:method")
    g.Use(serviceTokenAuth(deps))
    if deps.RateLimiter != nil {
        g.Use(deps.RateLimiter.Middleware())
    }
    g.Any("", proxyHandler(deps))
}
```

路由设计的几个要点：

- **健康检查独立**：`/` 和 `/healthz` 不经过认证和限流中间件，确保 Docker HEALTHCHECK 和负载均衡器的探测不受影响。
- **路由组中间件**：认证和限流作为路由组中间件挂载，仅对 `/api/:method` 生效。
- **条件注册**：当 `RateLimiter` 为 nil（`RATE_LIMIT_RPS=0`）时，不注册限流中间件，零开销。
- **Any 方法**：使用 `g.Any("")` 接收所有 HTTP 方法的请求，透明转发给上游，不限制客户端使用的 HTTP 方法。

### 4.5 应用生命周期

#### 4.5.1 启动顺序

```
LoadFromEnv() → NewProxy() → NewRateLimiter() → Echo + 中间件 → RegisterRoutes() → e.Start()
```

每个组件在创建时即完成初始化，不依赖延迟加载。`NewRateLimiter` 在创建时启动后台清理协程。

#### 4.5.2 优雅关闭

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

go func() {
    if err := e.Start(cfg.ListenAddr); err != nil {
        log.Printf("[conduit] server stopped: %v", err)
    }
}()

<-ctx.Done()

shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

if rl != nil {
    rl.Stop()
}
if err := e.Shutdown(shutdownCtx); err != nil {
    log.Printf("[conduit] forced shutdown: %v", err)
}
```

关闭流程：

1. 主 goroutine 阻塞等待 SIGINT 或 SIGTERM 信号。
2. 收到信号后，先停止 RateLimiter 的清理协程。
3. 调用 `e.Shutdown()` 优雅关闭 HTTP 服务器：停止接受新连接，等待已有请求处理完毕。
4. 设置 10 秒超时，超时后强制关闭。

### 4.6 配置模块

配置模块位于 `internal/config/config.go`，所有配置通过环境变量加载。

#### 4.6.1 设计选择

不同于 gorge-highlight 同时支持环境变量和 JSON 文件配置，gorge-conduit 仅支持环境变量。这个选择基于两个考虑：

1. **配置项少**：只有 8 个配置项，环境变量完全够用。
2. **容器化适配**：作为 Docker 容器运行时，环境变量是最自然的配置注入方式，与 docker-compose 的 `environment` 段和 Kubernetes 的 ConfigMap/Secret 直接对接。

#### 4.6.2 辅助函数

- `envStr(key, fallback)`：获取字符串环境变量，空值返回默认值。
- `envInt(key, fallback)`：获取整数环境变量，解析失败时打印警告并返回默认值（不静默吞掉错误）。
- `splitCSV(s)`：将逗号分隔字符串解析为切片，自动去除空白和空段。

## 5. 统一错误响应设计

所有错误响应遵循 Phorge Conduit API 的标准格式：

```json
{
  "result": null,
  "error_code": "ERR-XXX-YYY",
  "error_info": "Human-readable error message."
}
```

四个错误码覆盖了请求生命周期的各个阶段：

| 错误码 | HTTP 状态码 | 产生位置 | 含义 |
|---|---|---|---|
| `ERR-CONDUIT-CORE` | 400 | Proxy.Handle | URI 中未指定 Conduit 方法 |
| `ERR-CONDUIT-AUTH` | 401 | serviceTokenAuth | Service Token 缺失或不匹配 |
| `ERR-RATE-LIMIT` | 429 | RateLimiter.Middleware | 客户端请求频率超过限制 |
| `ERR-CONDUIT-PROXY` | 502 | Proxy.Handle | 上游请求构建失败或上游无响应 |

统一格式确保调用方无论遇到什么错误，都可以用相同的 JSON 解析逻辑处理。

## 6. 部署方案

### 6.1 Docker 镜像

采用多阶段构建：

- **构建阶段**：基于 `golang:1.26-alpine3.22`，使用 `CGO_ENABLED=0` 静态编译，`-ldflags="-s -w"` 去除调试信息和符号表以缩小二进制体积。
- **运行阶段**：基于 `alpine:3.20`，仅包含编译后的二进制和 CA 证书（CA 证书确保代理能通过 HTTPS 连接上游）。

内置 Docker `HEALTHCHECK`，每 10 秒通过 `wget` 检查 `/healthz` 端点，启动等待 5 秒，超时 3 秒，最多重试 3 次。

### 6.2 在 docker-compose 中的编排

在 `phorge/docker/services/docker-compose.yml` 中，gorge-conduit 作为 `conduit` 服务部署：

- 上游地址配置为 `http://app:80`（Phorge PHP 容器的服务名）
- 与 `gorge-worker` 等服务在同一 Docker 网络中，通过服务名互相访问
- `gorge-worker` 通过 `GO_CONDUIT_URL` 环境变量指向此网关

### 6.3 资源控制

多层资源保护机制防止滥用：

| 层级 | 机制 | 默认值 | 作用 |
|---|---|---|---|
| Echo 框架 | `BodyLimit` 中间件 | 10 MB | 拒绝超大请求体 |
| 认证层 | Service Token | (可选) | 阻止未授权访问 |
| 限流层 | Token Bucket | (可选) | 限制单 IP 请求频率 |
| 代理层 | `ProxyTimeoutSec` | 30 秒 | 防止慢上游拖垮服务 |
| 限流层 | `maxVisitors` | 100,000 | 防止 visitor map 内存溢出 |

## 7. 依赖分析

| 依赖 | 版本 | 用途 |
|---|---|---|
| `labstack/echo/v4` | v4.15.1 | HTTP 框架，提供路由、中间件和上下文管理 |
| `golang.org/x/crypto` | v0.49.0 | 提供 `subtle.ConstantTimeCompare`（间接） |
| `golang.org/x/net` | v0.52.0 | Echo 的网络基础（间接） |
| `golang.org/x/time` | v0.15.0 | Echo 的时间工具（间接） |

直接依赖仅 Echo 一个，保持了极简原则。限流器和反向代理完全自行实现，未引入第三方代理库（如 `httputil.ReverseProxy`）或限流库，保持了代码的可控性和透明度。

## 8. 测试覆盖

项目包含四组测试文件，覆盖所有核心模块：

| 测试文件 | 覆盖范围 |
|---|---|
| `config_test.go` | 环境变量字符串/整数读取、默认值、无效值警告、CSV 解析（空值/空白/多值）、完整配置默认值、自定义值覆盖 |
| `proxy_test.go` | URL 尾斜杠处理、超时设置、空方法 400、请求转发（路径/头/体）、query string 保留、上游错误 502、Hop-by-Hop 头过滤、多值头复制、上游不同状态码透传 |
| `ratelimit_test.go` | 豁免方法设置、RPS=0 禁用、豁免方法始终放行、突发后拒绝、不同客户端独立、并发安全、中间件放行/拒绝、优雅停止、visitor map 容量上限 |
| `handlers_test.go` | 健康检查（/ 和 /healthz）、无 Token 配置时放行、Header Token 认证、Query Token 认证、缺少 Token 401、无效 Token 401、代理转发集成、限流集成、多 HTTP 方法支持 |

测试设计的几个特点：

- **真实上游模拟**：proxy 和 handler 测试使用 `httptest.NewServer` 启动真实的 HTTP 服务器作为上游，而非 mock 接口，确保端到端行为的准确性。
- **边界条件**：覆盖了 visitor map 容量上限、并发访问、空方法名等边界场景。
- **集成测试**：`handlers_test.go` 中的限流集成测试和多方法测试验证了完整的中间件链路。

## 9. 总结

gorge-conduit 是一个设计精练的 API 网关微服务，核心价值在于：

1. **统一接入**：将分散的 Conduit API 调用收敛到单一网关，实现认证、限流和日志的集中管控。
2. **安全设计**：恒定时间 Token 比较防时序攻击、Hop-by-Hop 头过滤符合 HTTP 规范、visitor map 容量上限防内存耗尽。
3. **渐进式保护**：认证和限流均为可选功能，零配置即可运行，按需开启不同级别的保护。
4. **极简实现**：仅依赖 Echo 框架，反向代理和限流器完全自行实现，代码量约 250 行（不含测试），易于理解和维护。
5. **运维友好**：环境变量配置、健康检查端点、Docker 多阶段构建、优雅关闭，开箱即用于容器化部署。
