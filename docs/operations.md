# 运维手册

以下示例只使用 `example.com` 和占位值。真实状态卷、域名、坐标、QWeather 标识和凭据不得写回仓库。

## LAN 模板

LAN 模板直接发布 HTTP 8080，并显式允许浏览器提交管理凭据。只应在受信局域网和主机防火墙之后使用，不得通过路由器端口转发或公网安全组开放。

```sh
docker compose -f deploy/compose.lan.yaml pull
docker compose -f deploy/compose.lan.yaml up -d
```

打开 `http://<NAS地址>:8080/admin/`。初始化页面要求管理员密码、QWeather 账户专属 API Host、Project ID、Credential ID、Ed25519 PKCS#8 私钥、一次性测试坐标和首个设备名称。

浏览器通常只在 HTTPS 或 localhost 开放定位权限。LAN HTTP 无法授权时，可手工填写临时测试经纬度；测试成功后页面显示 QWeather 当前天气，坐标不会保存。生成的设备 Bearer token 只显示一次。

## Caddy HTTPS 模板

公开主机名必须已指向部署主机，TCP 80/443 和 UDP 443 可达：

```sh
MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml pull
MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml up -d
```

Caddy 与源站只通过内部 Docker 网络通信，源站不发布宿主端口。模板设置 `MT_ADMIN_BEHIND_HTTPS_PROXY=true`，使管理 Cookie 带 `Secure`；服务不读取代理协议或位置头。首次初始化页面公开前，应使用防火墙或上游访问策略限制管理员访问。

## 现有反向代理

接入已有 Caddy、Nginx、Traefik 或 Cloudflare Tunnel 时：

- 源站保持普通 HTTP，仅允许代理网络访问。
- 设置 `MT_ADMIN_ALLOW_INSECURE_HTTP=false` 和 `MT_ADMIN_BEHIND_HTTPS_PROXY=true`。
- 保留原始 `Host`，使同源校验使用浏览器看到的主机名。
- 正常转发设备提交的 `Authorization` 和 `X-MT-Location-*` 请求头。
- 不需要转发 `X-Forwarded-For` 或任何代理地理位置头；服务不会读取它们。

`deploy/examples/` 提供 Nginx、Traefik 和 Cloudflare Tunnel 的普通 HTTPS 接入片段。入口实现不影响天气定位。

## 设备位置与天气请求

设备在网络连接建立后访问自身选择的 IP 定位服务。以 IPinfo 匿名接口为例，响应中的 `loc` 是“纬度,经度”，另有 `city`、`region`、`country` 和 `timezone`。

设备应在当前网络会话内缓存解析后的结果，并在三个天气 GET 请求中携带：

```text
X-MT-Location-Latitude
X-MT-Location-Longitude
X-MT-Location-Provider
X-MT-Location-City       (可选)
X-MT-Location-Region     (可选)
X-MT-Location-Country    (可选)
X-MT-Location-Timezone   (可选)
```

不要在每次天气请求前重复访问定位服务。设备重新联网或网络出口改变时重新获取；定位失败时不要发送缺失或旧网络的位置，天气 API 将返回 `400 location_required`。不得上传定位响应中的 IP、组织、邮编、完整 JSON 或凭据。

## 设备令牌轮换

管理页面允许最多 32 个命名令牌。安全轮换顺序为：创建新令牌、更新并验证设备、撤销旧令牌。服务拒绝撤销最后一个令牌。令牌原文无法恢复。

## 健康与故障

```sh
curl --fail http://127.0.0.1:8080/health/live
curl http://127.0.0.1:8080/health/ready
```

- 未初始化：live 为 200，ready 为 503 且模块状态为 `setup_required`。
- 正常配置：live 和 ready 均为 200。
- 缺少或非法设备位置：天气接口返回 400，不调用 QWeather。
- 位置切换过快：天气接口返回 429，并附带 `Retry-After`。
- QWeather 认证熔断：live 为 200，ready 为 503；已有缓存允许时仍可返回 `stale=true`。

管理页面更新 QWeather 时会先真实请求实时天气。验证或写盘失败不会替换原配置。日志只记录事件类别、请求 ID、路径和安全错误，不记录请求体、IP、坐标、位置元数据或凭据。

## 备份、升级与回滚

- `mt-server-state` 卷包含明文 QWeather 私钥和管理员验证器，必须使用加密备份并限制读取权限。
- 仅部署语义化版本标签或镜像 digest，禁止使用 `latest`。
- 当前预览版从 schema v1 升级到 v2 时不自动迁移；删除旧 `state.json` 后重新初始化。
- 以后升级前备份状态卷，更新固定镜像版本后观察 `/health/ready`、容器重启次数和最近日志。
- 回滚镜像时必须确认其支持当前 `state.schema_version`。
