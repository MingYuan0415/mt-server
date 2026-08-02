# mt-server

`mt-server` 是面向 MicroTech 设备的自托管后端。当前版本提供经过鉴权、设备定位、归一化与缓存的天气 API，以及用于首次初始化和后续维护的内嵌中文管理界面。

天气数据来源于 [和风天气 / QWeather](https://www.qweather.com/)。设备界面使用天气数据时也必须清晰显示数据来源。

## 快速开始

局域网模板不依赖 Cloudflare、域名或预先创建的 secret 文件，只能部署在受信网络：

```sh
docker compose -f deploy/compose.lan.yaml up -d
```

打开 `http://<NAS地址>:8080/admin/`，配置管理员密码、QWeather 凭据和首个设备令牌。连接测试使用一次性临时坐标；浏览器无法在 LAN HTTP 获取定位时可手工填写。未初始化实例没有管理员认证边界，只能在受信网络中完成首次配置。

公网部署使用 Caddy HTTPS 模板：

```sh
MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml up -d
```

也可接入已有 Caddy、Nginx、Traefik 或 Cloudflare Tunnel。入口只负责 HTTPS，不参与设备定位，详见 [`docs/operations.md`](docs/operations.md)。

## 当前能力

- 未配置实例可启动、通过存活检查并提供管理网页。
- 网页验证并保存 QWeather Ed25519 凭据，直接显示临时位置的实时天气。
- 每个已认证设备请求必须主动提交定位服务、经纬度和可选城市元数据。
- 提供实时天气、24 小时预报和 7 天预报。
- 按 0.1 度位置网格隔离的 LRU 内存缓存、并发刷新合并和陈旧数据降级。
- 按设备限制短时间位置跳变，避免绕过缓存消耗 QWeather 配额。
- 支持多个命名设备令牌、重叠轮换和撤销；服务只保存令牌哈希。
- Argon2id 管理员密码、CSRF 防护、初始化限速和内存会话。
- scratch、非 root、只读容器，只有版本化状态卷可写。

## 设备 API

```text
GET /api/v1/weather/current
GET /api/v1/weather/hourly
GET /api/v1/weather/daily
```

每个天气请求需要设备令牌和位置头：

```sh
curl https://api.example.com/api/v1/weather/current \
  -H 'Authorization: Bearer <device-token>' \
  -H 'X-MT-Location-Latitude: <latitude>' \
  -H 'X-MT-Location-Longitude: <longitude>' \
  -H 'X-MT-Location-Provider: ipinfo' \
  -H 'X-MT-Location-City: Example City' \
  -H 'X-MT-Location-Region: Example Region' \
  -H 'X-MT-Location-Country: EX' \
  -H 'X-MT-Location-Timezone: Etc/UTC'
```

纬度、经度和定位服务标识必填，其他位置头可选。设备可在联网后访问 [`https://ipinfo.io/json`](https://ipinfo.io/json)，将 `loc` 的“纬度,经度”拆分后提交；不得向服务器发送 IP、运营商、邮编、完整原始响应或定位服务凭据。服务不使用请求来源 IP、代理头、查询参数或 QWeather GeoAPI 定位。

完整契约见 [`api/openapi.json`](api/openapi.json)。健康检查和管理页面分别位于 `/health/*` 与 `/admin/`。

## 开发

要求 Go 1.26.5 和 Docker Engine。管理网页端到端测试另需 Node.js 22；Node 依赖仅用于开发与 CI，不进入服务镜像。

```sh
make format
make check
make web-test
make build
make docker-build
```

运行时只需要一个状态目录。QWeather 私钥、管理员密码验证器和设备令牌哈希保存在状态卷中；设备位置只存在于单次请求和内存天气缓存键中，不写入状态。

## 文档

- [`docs/architecture.md`](docs/architecture.md)：模块边界、定位信任边界和热切换流程。
- [`docs/operations.md`](docs/operations.md)：LAN、HTTPS、设备接入、备份与回滚。
- [`SECURITY.md`](SECURITY.md)：管理面、设备位置和状态卷安全边界。

## 许可证

本项目使用 [MIT License](LICENSE)。QWeather 与设备选择的定位服务仍受各自许可和归属要求约束。
