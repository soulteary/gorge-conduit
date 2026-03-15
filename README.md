# gorge-conduit

Go Conduit API 网关，为 Phorge 提供反向代理能力。接收所有 `/api/:method` 请求，完成 Service Token 认证和基于 IP 的 Token Bucket 限流后，转发到上游 Phorge PHP 应用，实现 Conduit API 的统一接入控制。

## 特性

- 轻量级 HTTP 反向代理，专为 Phorge Conduit API 设计
- 可选 Service Token 认证，使用恒定时间比较防止时序攻击
- 可选基于 IP 的 Token Bucket 限流，支持方法级豁免
- 自动过滤 Hop-by-Hop 头，注入 `X-Forwarded-For` / `X-Forwarded-Proto`
- 统一 Phorge Conduit 兼容的 JSON 错误响应格式
- 静态编译，零外部依赖，Docker 镜像极轻量
- 内置健康检查端点，适配容器编排
- 优雅关闭，支持 SIGINT / SIGTERM 信号处理

## 快速开始

### 本地运行

```bash
go build -o gorge-conduit ./cmd/server
./gorge-conduit
```

服务默认监听 `:8150`。

### Docker 运行

```bash
docker build -t gorge-conduit .
docker run -p 8150:8150 gorge-conduit
```

### 带配置运行

```bash
export SERVICE_TOKEN="my-secret-token"
export UPSTREAM_URL="http://phorge:80"
export RATE_LIMIT_RPS=50
./gorge-conduit
```

## 配置

所有配置通过环境变量加载。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LISTEN_ADDR` | `:8150` | 服务监听地址 |
| `SERVICE_TOKEN` | (空) | 服务间认证 Token，为空则不启用认证 |
| `UPSTREAM_URL` | `http://phorge:80` | 上游 Phorge PHP 应用地址 |
| `RATE_LIMIT_RPS` | `0` | 每客户端每秒请求数上限，0 表示关闭限流 |
| `RATE_LIMIT_BURST` | `20` | 令牌桶突发容量 |
| `RATE_LIMIT_EXEMPT` | `conduit.ping,conduit.getcapabilities` | 豁免限流的 Conduit 方法，逗号分隔 |
| `PROXY_TIMEOUT_SEC` | `30` | 上游请求超时秒数 |
| `MAX_BODY_SIZE` | `10M` | 请求体大小限制（Echo BodyLimit 格式） |

## API

所有 `/api/:method` 端点在启用 `SERVICE_TOKEN` 时需要认证。认证方式：

- 请求头：`X-Service-Token: <token>`
- 查询参数：`?token=<token>`

### ANY /api/:method

将请求转发到上游 Phorge Conduit API。支持所有 HTTP 方法（GET、POST、PUT、DELETE 等）。

**请求**：与目标 Conduit 方法要求一致，请求头和请求体原样转发。

**成功响应**：原样返回上游响应的状态码、响应头和响应体。

**错误响应**：

| 状态码 | 错误码 | 说明 |
|---|---|---|
| 400 | `ERR-CONDUIT-CORE` | 未指定 Conduit 方法 |
| 401 | `ERR-CONDUIT-AUTH` | Token 缺失或无效 |
| 429 | `ERR-RATE-LIMIT` | 触发限流 |
| 502 | `ERR-CONDUIT-PROXY` | 上游请求失败 |

错误响应格式与 Phorge Conduit 保持一致：

```json
{
  "result": null,
  "error_code": "ERR-CONDUIT-PROXY",
  "error_info": "Upstream request failed."
}
```

### GET /healthz

健康检查端点，不需要认证。

**响应** (200)：

```json
{"status": "ok"}
```

## 项目结构

```
gorge-conduit/
├── cmd/server/main.go              # 服务入口
├── internal/
│   ├── config/config.go            # 环境变量配置加载
│   ├── gateway/
│   │   ├── proxy.go                # HTTP 反向代理核心实现
│   │   └── ratelimit.go            # Token Bucket 限流器
│   └── httpapi/handlers.go         # HTTP 路由、认证中间件与处理器
├── Dockerfile                      # 多阶段 Docker 构建
├── go.mod
└── go.sum
```

## 开发

```bash
# 运行全部测试
go test ./...

# 运行测试（带详细输出）
go test -v ./...

# 构建二进制
go build -o gorge-conduit ./cmd/server
```

## 技术栈

- **语言**：Go 1.26
- **HTTP 框架**：[Echo](https://echo.labstack.com/) v4.15.1
- **许可证**：Apache License 2.0
