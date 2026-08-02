# 运维手册

以下示例只使用 `example.com` 和占位值。真实状态卷、域名、坐标、QWeather 标识和凭据不得写回仓库。

## LAN 模板

LAN 模板直接发布 HTTP 8080，并显式允许浏览器提交管理凭据。只应在受信局域网和主机防火墙之后使用，不得通过路由器端口转发或公网安全组开放。

```sh
docker compose -f deploy/compose.lan.yaml pull
docker compose -f deploy/compose.lan.yaml up -d
```

打开 `http://<NAS地址>:8080/admin/`。初始化页面要求管理员密码、QWeather 账户专属 API Host、Project ID、Credential ID、Ed25519 PKCS#8 私钥、一次性测试坐标和首个设备名称。

临时测试坐标只用于本次 QWeather 实时验证，不写入状态或天气缓存。测试成功后页面显示归一化的当前天气；生成的设备 Bearer token 只显示一次。

## Caddy HTTPS 模板

公开主机名必须已指向部署主机，TCP 80/443 和 UDP 443 可达：

```sh
MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml pull
MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml up -d
```

Caddy 与源站只通过内部 Docker 网络通信，源站不发布宿主端口。模板设置 `MT_ADMIN_BEHIND_HTTPS_PROXY=true`，因此管理 Cookie 带 `Secure`；初始化页面会把当前 HTTPS Origin 保存到私有状态，同源校验不依赖代理传给源站的内部 `Host`。首次初始化页面公开前，应使用防火墙或上游访问策略限制管理员访问。

## 现有反向代理

接入已有 Caddy、Nginx、Traefik 或 Cloudflare Tunnel 时：

- 源站保持普通 HTTP，仅允许代理网络访问。
- 设置 `MT_ADMIN_ALLOW_INSECURE_HTTP=false` 和 `MT_ADMIN_BEHIND_HTTPS_PROXY=true`。
- 通过公开 HTTPS 域名打开初始化页面；当前 Origin 会自动加入候选列表。
- 保留天气请求中的 `Authorization` 和 `X-MT-Location-*` 请求头。
- 不需要转发 `X-Forwarded-For` 或任何代理地理位置头；服务不会读取它们。

Origin 必须是完整 HTTPS Origin，可包含 IPv4、带方括号的 IPv6 和非默认端口；主机名大小写及默认 `:443` 会被规范化。路径、查询、片段、用户信息、HTTP 和重复项会被拒绝。Cloudflare Tunnel 与其他代理不需要覆盖源站 `Host`；服务不会读取 `Forwarded`、`X-Forwarded-Host` 或 `X-Forwarded-Proto`。

`deploy/examples/` 提供 Nginx、Traefik 和 Cloudflare Tunnel 的普通 HTTPS 接入片段。入口实现不改变天气 API 的处理语义。

## 管理域名轮换与恢复

管理页面最多保存 16 个 HTTPS Origin。正常更换顺序为：在旧入口添加新 Origin、确认新域名的 DNS 与代理可用、从新域名重新登录、删除旧 Origin。当前正在访问的 Origin 不能删除；删除其他 Origin 后所有管理会话失效，需要在新入口重新登录。添加和删除均在状态提交后立即生效，不需要重启容器。

如果所有网页入口都已失效，先停止业务容器，再使用同一状态卷执行离线命令：

```sh
MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml stop mt-server
MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml run --rm --no-deps mt-server \
  admin-origin list
MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml run --rm --no-deps mt-server \
  admin-origin add https://admin.example.com
MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml start mt-server
```

也可用 `admin-origin remove https://old.example.com` 删除入口。HTTPS 代理模式不允许删除最后一个 Origin。服务运行时持有状态目录独占锁；若未停止容器，离线命令会拒绝修改，不能强行绕过。

## 天气 API 请求

三个天气端点均使用 GET 和 Bearer 鉴权：

```text
GET /api/v1/weather/current
GET /api/v1/weather/hourly
GET /api/v1/weather/daily
```

鉴权通过后，服务只解析以下固定请求头：

```text
X-MT-Location-Latitude
X-MT-Location-Longitude
X-MT-Location-Provider
X-MT-Location-City       (可选)
X-MT-Location-Region     (可选)
X-MT-Location-Country    (可选)
X-MT-Location-Timezone   (可选)
```

纬度、经度和 Provider 必填。服务校验坐标范围、有限值、Provider 格式和可选元数据，并将坐标归一化到 `0.1` 度网格。缺少必填头返回 `400 location_required`，非法内容返回 `400 invalid_location`；两种情况都不会调用 QWeather。

服务不读取 `RemoteAddr`、`X-Forwarded-For`、查询参数或其他位置头。响应只返回当前请求的可选显示元数据、`source: "device"`、Provider 和 `precision: "city"`，不返回坐标或 IP。相同网格共享天气数据缓存，但显示元数据不进入缓存。

## 设备令牌轮换

管理页面允许最多 32 个命名令牌。安全轮换顺序为：创建新令牌、确认新令牌可用、撤销旧令牌。服务拒绝撤销最后一个令牌。令牌原文无法恢复。

## 健康与故障

```sh
curl --fail http://127.0.0.1:8080/health/live
curl http://127.0.0.1:8080/health/ready
```

- 未初始化：live 为 200，ready 为 503 且模块状态为 `setup_required`。
- 正常配置：live 和 ready 均为 200。
- 缺少或非法请求位置：天气接口返回 400，不调用 QWeather。
- 位置切换过快：天气接口返回 429，并附带 `Retry-After`。
- QWeather 认证熔断：live 为 200，ready 为 503；已有缓存允许时仍可返回 `stale=true`。
- 状态目录同步未确认：写操作仍成功并返回 `X-MT-State-Warning: durability_unconfirmed`，管理概览持续显示告警；检查状态卷后执行下一次管理写入以重新确认。

管理页面更新 QWeather 时会先真实请求实时天气。验证或写盘失败不会替换原配置。日志只记录事件类别、请求 ID、路径和安全错误，不记录请求体、IP、坐标、位置元数据或凭据。

## 备份、升级与回滚

- `mt-server-state` 卷包含明文 QWeather 私钥和管理员验证器，必须使用加密备份并限制读取权限。
- 仅部署语义化版本标签或镜像 digest，禁止使用 `latest`。
- v0.2.0 使用 state schema v3，不迁移 v0.1.x 的 schema v2。升级前必须加密备份 `mt-server-state`，停止服务并仅删除 mt-server 状态卷；不得删除 Caddy 的 `caddy-data` 或 `caddy-config` 卷。启动 v0.2.0 后通过管理网页重新配置管理员、管理域名、QWeather 和设备令牌。
- 以后升级前备份状态卷，更新固定镜像版本后观察 `/health/ready`、容器重启次数和最近日志。
- v0.1.x 无法读取 schema v3；回滚旧镜像前必须恢复升级前的 schema v2 状态卷备份。

HTTPS 模板重置旧状态时，先停止整套服务并查询精确卷名，再只删除带 `com.docker.compose.volume=mt-server-state` 标签的卷：

```sh
MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml down
docker volume ls \
  --filter label=com.docker.compose.volume=mt-server-state \
  --format '{{.Name}}'
docker volume rm <上一步输出的mt-server状态卷名>
MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml up -d
```

不得使用 `docker compose down -v`，它会同时删除 Caddy 证书和配置卷。
