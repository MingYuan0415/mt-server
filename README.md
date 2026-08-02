# mt-server

`mt-server` 是面向 MicroTech 设备的自托管天气后端。服务提供带鉴权的天气 API、QWeather 供应商适配、内存缓存，以及用于初始化和日常维护的内嵌中文管理界面。

天气数据来源于 [和风天气 / QWeather](https://www.qweather.com/)。所有天气响应都包含稳定的数据来源和官方归属链接。

## 功能

- 未配置实例可正常启动，并通过管理网页完成首次初始化。
- 管理 QWeather Ed25519 凭据、连接验证和运行时热切换。
- 提供实时天气、24 小时逐小时预报和 7 天逐日预报。
- 使用 Bearer token 鉴权，支持最多 32 个命名令牌、重叠轮换和撤销。
- 校验请求位置并归一化到 `0.1` 度网格，不记录或返回坐标和客户端 IP。
- 按位置网格隔离 LRU 内存缓存，支持并发刷新合并和陈旧数据降级。
- 按鉴权主体限制短时间位置跳变，保护上游配额。
- 使用 Argon2id 管理员密码、内存 session、同源校验、CSRF 和全局登录限速。
- 通过初始化和管理网页维护 HTTPS Origin 白名单，新增入口无需重启容器。
- 提供版本化状态、可报告持久性结果的原子写入、健康检查和结构化访问日志。
- 提供 scratch、非 root、只读根文件系统的多架构容器镜像。

## 部署

### LAN

LAN 模板直接发布 HTTP 端口，只能用于受信局域网：

```sh
docker compose -f deploy/compose.lan.yaml up -d
```

宿主机端口默认为 `8080`，可通过 `MT_HTTP_PORT` 修改。打开 `http://<NAS地址>:8080/admin/` 完成初始化。

### HTTPS

Caddy 模板只公开 `80/443`，`mt-server` 不映射源站端口：

```sh
MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml up -d
```

也可接入已有 Caddy、Nginx、Traefik 或 Cloudflare Tunnel。部署边界、现有反向代理接入和升级流程见 [`docs/operations.md`](docs/operations.md)。

## 初始化与管理

首次打开 `/admin/` 时需要配置：

- 至少 12 个 Unicode 字符且 UTF-8 编码不超过 128 字节的管理员密码。
- 当前及备用管理 HTTPS Origin；代理模式会自动加入当前入口。
- QWeather 账户专属 API Host、Project ID、Credential ID 和 Ed25519 PKCS#8 私钥。
- 仅用于本次 QWeather 实时验证的临时坐标。
- 首个设备名称。

验证成功后，服务原子创建 `state.json`、热加载天气运行时，并仅显示一次首个设备令牌。后续可在管理界面增删管理域名、测试或更新 QWeather、修改管理员密码，以及创建和撤销设备令牌。私钥保存后只返回公钥指纹。

未初始化时 `/health/live` 返回 `200`，`/health/ready` 返回 `503 setup_required`，天气接口返回 `503 service_unconfigured`。未初始化实例没有管理员身份边界，应先在受限网络中完成配置。

## 天气 API

```text
GET /api/v1/weather/current
GET /api/v1/weather/hourly
GET /api/v1/weather/daily
```

每个请求需要设备 Bearer token，以及纬度、经度和位置来源标识。城市、地区、国家和时区为可选显示元数据：

```sh
curl https://api.example.com/api/v1/weather/current \
  -H 'Authorization: Bearer <device-token>' \
  -H 'X-MT-Location-Latitude: <latitude>' \
  -H 'X-MT-Location-Longitude: <longitude>' \
  -H 'X-MT-Location-Provider: example' \
  -H 'X-MT-Location-City: Example City' \
  -H 'X-MT-Location-Region: Example Region' \
  -H 'X-MT-Location-Country: EX' \
  -H 'X-MT-Location-Timezone: Etc/UTC'
```

服务先完成鉴权，再解析位置头。坐标必须有限且在合法范围内；来源标识必须匹配 `[a-z0-9][a-z0-9._-]{0,31}`；可选元数据必须为合法 UTF-8、无控制字符且不超过 128 个字符。服务不读取客户端地址、`X-Forwarded-For`、查询参数或代理位置头，也不会发起位置解析请求。

缺少必填位置头返回 `400 location_required`，非法内容返回 `400 invalid_location`，位置变化超限返回 `429 location_rate_limited`。完整请求、响应和错误契约见 [`api/openapi.json`](api/openapi.json)。

## 运行配置

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `MT_LISTEN_ADDR` | `:8080` | 进程监听地址 |
| `MT_LOG_LEVEL` | `info` | `debug`、`info`、`warn` 或 `error` |
| `MT_STATE_DIR` | `/var/lib/mt-server` | 必须为绝对路径的可写状态目录 |
| `MT_ADMIN_ALLOW_INSECURE_HTTP` | `false` | 仅在受信 LAN 中允许 HTTP 管理写操作 |
| `MT_ADMIN_BEHIND_HTTPS_PROXY` | `false` | 明确声明管理入口始终由受信 HTTPS 代理提供 |

两个管理传输开关不能同时启用。HTTPS 代理模式的域名列表保存在私有状态中，由初始化页和“管理域名”页面维护；服务直接匹配浏览器 `Origin`，不信任 `Forwarded`、`X-Forwarded-Host` 或 `X-Forwarded-Proto`。代理必须确保源站只允许代理访问。

QWeather 私钥、管理员密码验证器、管理域名、缓存策略和设备令牌哈希保存在状态卷中。请求位置只用于单次请求、网格限速和内存缓存键，不写入持久状态。

## 开发与验证

要求 Go 1.26.5 和 Docker Engine。管理界面端到端测试及 OpenAPI 校验另需 Node.js 22.12+；Node 依赖仅用于开发和 CI。

```sh
make format
make check
make web-test
make build
make docker-build
```

CI 执行格式检查、vet、普通及 race 测试、覆盖率门槛、Go/npm 漏洞扫描、OpenAPI 校验、管理界面测试、容器扫描和镜像冒烟测试，不调用真实 QWeather。

## 文档

- [`docs/architecture.md`](docs/architecture.md)：模块边界、请求处理、状态和热切换。
- [`docs/operations.md`](docs/operations.md)：部署、代理、健康检查、备份、升级与回滚。
- [`api/openapi.json`](api/openapi.json)：天气 API 契约。
- [`api/admin-openapi.json`](api/admin-openapi.json)：管理 API 契约。
- [`SECURITY.md`](SECURITY.md)：管理面、请求位置和状态卷安全边界。

## 许可证

本项目使用 [MIT License](LICENSE)。QWeather 数据及服务受其许可条款约束。
