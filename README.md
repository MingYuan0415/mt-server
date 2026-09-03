# mt-server

`mt-server` 是面向 MicroTech 设备的自托管天气后端。服务提供带鉴权的天气 API、QWeather 供应商适配、内存缓存，以及用于初始化和日常维护的内嵌中文管理界面。

天气数据来源于 [和风天气 / QWeather](https://www.qweather.com/)。所有天气响应都包含稳定的数据来源和官方归属链接。

## 功能

- 未配置实例可正常启动，并通过管理网页完成首次初始化。
- 管理 QWeather Ed25519 凭据、连接验证和运行时热切换。
- 提供实时天气、24 小时逐小时预报、7 天逐日预报和天气预警。
- 使用 Bearer token 鉴权，支持最多 32 个命名令牌、重叠轮换和撤销。
- 校验请求位置并将坐标归一化到两位小数（`0.01` 度）精度，不记录或返回坐标和客户端 IP。
- 可选 GeoLite2 City 本地 IP 推断：设备省略位置头时按公网出口位置查询天气，并提供 `/api/v1/location` 返回城市元数据。
- 通过 QWeather GeoAPI 尽力把显示名称本地化为中文，支持失败回退与调用预算保护。
- 按归一化位置隔离 LRU 内存缓存，支持并发刷新合并和陈旧数据降级。
- 按鉴权主体限制短时间位置跳变，保护上游配额。
- 使用 Argon2id 管理员密码、内存 session、同源校验、CSRF 和全局登录限速。
- 通过初始化和管理网页维护 HTTPS Origin 白名单，新增入口无需重启容器。
- 提供版本化状态、v3 到 v4 自动迁移、可报告持久性结果的原子写入、健康检查和结构化访问日志。
- 在已认证管理页展示不含设备或位置维度的供应商与缓存诊断。
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

验证会同时检查 QWeather 实时天气和天气预警能力。成功后，服务原子创建 `state.json`、热加载天气运行时，并仅显示一次首个设备令牌。后续可在管理界面增删管理域名、查看运行诊断、测试或更新 QWeather、修改管理员密码，以及创建和撤销设备令牌。私钥保存后只返回公钥指纹。

未初始化时 `/health/live` 返回 `200`，`/health/ready` 返回 `503 setup_required`，天气接口返回 `503 service_unconfigured`。未初始化实例没有管理员身份边界，应先在受限网络中完成配置。

## 天气 API

```text
GET /api/v1/weather/current
GET /api/v1/weather/hourly
GET /api/v1/weather/daily
GET /api/v1/weather/alerts
GET /api/v1/location
```

每个天气请求需要设备 Bearer token。设备可携带完整的固定位置头（城市、地区、国家和时区为可选显示元数据）：

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

服务先完成鉴权，再解析位置头。坐标必须有限且在合法范围内；来源标识必须匹配 `[a-z0-9][a-z0-9._-]{0,31}`；可选元数据必须为合法 UTF-8、无控制字符且不超过 128 个字符。服务不读取任意转发头或查询参数；未配置可信客户端 IP 头时，IP 推断使用直连对端地址，该地址从不记录或返回。

位置头必须同时提供或同时省略：只提供其中一部分会返回 `400 invalid_location`。省略全部位置头时，如果部署配置了可信 Cloudflare 访客位置头（`MT_CLOUDFLARE_LOCATION_HEADERS=true`）或 GeoLite2 数据库，服务会根据设备公网出口 IP 推断粗粒度位置（`source: "ip"`、`precision: "coarse"`），并照常归一化、限速和缓存；可信客户端 IP 头为可选配置，未配置时使用直连对端地址（隧道部署建议配置以获取真实公网 IP）。推断不可用时返回 `503 location_unavailable`；未配置任何推断时，天气接口对无位置头请求返回 `400 location_required`，`/api/v1/location` 返回 `503 location_unavailable`。

`GET /api/v1/location` 返回将用于天气请求的位置（显式头优先，否则为 IP 推断结果），只包含城市、可选区县、地区、国家、时区、来源、提供方、精度、可选 `accuracy_radius_km` 和 `location_key`，从不返回 IP 或坐标。`location_key` 是由服务端从归一化后的两位小数坐标确定性派生的 16 位小写十六进制不透明标识，不直接包含坐标或 IP，同一位置恒定，位置变化时变化；它不是密码学匿名化，仅作为位置作用域身份比较的依据（显示字段仅供展示）。设备可在无 GPS 时用该端点获取自身所在城市，再决定是否需要显式位置头。

配置 QWeather 后，天气与位置响应的显示名称会尽力本地化为中文（通过 GeoAPI 按归一化坐标反查，`city` 为城市名、新增 `district` 区县字段、`region`/`timezone` 覆盖，`country` 保持 ISO 代码）。本地化实时查询、不做缓存：失败、超时或超出每设备调用预算时回退请求自身名称，天气可用性不受影响；`/api/v1/location` 仅在本地化成功且确有显示字段被覆盖时附带 `localization` 归属对象，展示名称的界面需可见署名 QWeather。海外地点可能按当地官方语言或英文回退。

天气接口在缺少全部位置头且未配置推断时返回 `400 location_required`，非法内容返回 `400 invalid_location`，位置变化超限返回 `429 location_rate_limited`；`/api/v1/location` 对无位置头且无推断请求返回 `503 location_unavailable`。完整请求、响应和错误契约见 [`api/openapi.json`](api/openapi.json)。

预警端点返回当前位置的完整预警快照；空数组表示当前没有预警。客户端应以每次响应替换同一位置的旧快照。预警默认缓存 10 分钟，上游故障时最多返回 1 小时内的陈旧快照，并通过 `stale` 明确标记。

## 运行配置

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `MT_LISTEN_ADDR` | `:8080` | 进程监听地址 |
| `MT_LOG_LEVEL` | `info` | `debug`、`info`、`warn` 或 `error` |
| `MT_STATE_DIR` | `/var/lib/mt-server` | 必须为绝对路径的可写状态目录 |
| `MT_ADMIN_ALLOW_INSECURE_HTTP` | `false` | 仅在受信 LAN 中允许 HTTP 管理写操作 |
| `MT_ADMIN_BEHIND_HTTPS_PROXY` | `false` | 明确声明管理入口始终由受信 HTTPS 代理提供 |
| `MT_GEOIP_DB` | 空 | GeoLite2 City MMDB 文件路径；设置后启用 IP 位置推断 |
| `MT_CLOUDFLARE_LOCATION_HEADERS` | `false` | 读取可信直连代理提供的 Cloudflare 访客位置头（需启用托管转换 “Add visitor location headers”）；启用时必填 `MT_TRUSTED_CLIENT_IP_NETS` |
| `MT_TRUSTED_CLIENT_IP_HEADER` | 空 | 从受信代理读取客户端 IP 的请求头（如 `CF-Connecting-IP`） |
| `MT_TRUSTED_CLIENT_IP_NETS` | 空 | 可提供客户端 IP 与位置头的代理网段（逗号分隔的 CIDR） |

两个管理传输开关不能同时启用。HTTPS 代理模式的域名列表保存在私有状态中，由初始化页和“管理域名”页面维护；服务直接匹配浏览器 `Origin`，不信任 `Forwarded`、`X-Forwarded-Host` 或 `X-Forwarded-Proto`。代理必须确保源站只允许代理访问。

IP 推断只信任 `MT_TRUSTED_CLIENT_IP_NETS` 中直连代理提供的 `MT_TRUSTED_CLIENT_IP_HEADER` 值，其余请求一律使用连接对端地址。设置头但未设置网段会拒绝启动。公网 IP 推断是“网络出口附近的粗略天气区域”，不等于设备真实位置；移动网络、CGNAT、VPN 会定位到运营商或代理出口。启用 Cloudflare 访客位置头时，`CF-IPLatitude`/`CF-IPLongitude` 也只从网段内直连代理读取，其余请求忽略；坐标对完全缺失时继续尝试其他推断，只出现其一、为空白、重复或非法时直接返回 `location_unavailable`，不会降级到其他来源。MMDB 由外部 `geoipupdate` 写入私有卷，应用只读并检测替换后热加载，密钥不进入镜像、仓库或应用日志。

QWeather 私钥、管理员密码验证器、管理域名、缓存策略和设备令牌哈希保存在状态卷中。请求位置只用于单次请求、位置变更限速和内存缓存键，不写入持久状态；IP 推断结果同样不落盘，客户端 IP 和坐标从不记录或返回。`location_key` 由归一化坐标确定性派生、无需持久化，也不进入日志或持久状态。

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
