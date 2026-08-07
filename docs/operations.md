# 运维手册

以下示例只使用 `example.com` 和占位值。真实状态卷、域名、坐标、QWeather 标识和凭据不得写回仓库。

## LAN 模板

LAN 模板直接发布 HTTP 8080，并显式允许浏览器提交管理凭据。只应在受信局域网和主机防火墙之后使用，不得通过路由器端口转发或公网安全组开放。

```sh
docker compose -f deploy/compose.lan.yaml pull
docker compose -f deploy/compose.lan.yaml up -d
```

打开 `http://<NAS地址>:8080/admin/`。初始化页面要求管理员密码、QWeather 账户专属 API Host、Project ID、Credential ID、Ed25519 PKCS#8 私钥、一次性测试坐标和首个设备名称。

临时测试坐标只用于本次 QWeather 实时天气和预警验证，不写入状态或天气缓存。测试成功后页面显示归一化的当前天气；生成的设备 Bearer token 只显示一次。

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
- 需要 IP 推断时，按“IP 位置推断”一节配置可信客户端 IP 头与网段；否则不需要转发 `X-Forwarded-For` 或任何代理地理位置头。

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

五个端点均使用 GET 和 Bearer 鉴权：

```text
GET /api/v1/weather/current
GET /api/v1/weather/hourly
GET /api/v1/weather/daily
GET /api/v1/weather/alerts
GET /api/v1/location
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

纬度、经度和 Provider 需要同时提供或同时省略。提供时，服务校验坐标范围、有限值、Provider 格式和可选元数据，并将坐标归一化到 `0.1` 度网格；只提供其中一部分返回 `400 invalid_location`。省略时启用 IP 推断（见下节），推断不可用返回 `503 location_unavailable`；未配置任何 IP 推断时，天气接口对无位置头请求返回 `400 location_required`，`GET /api/v1/location` 返回 `503 location_unavailable`。以上情况都不会调用 QWeather。

服务不读取任意转发头（如 `X-Forwarded-For`）、查询参数或其他位置头；配置了 Cloudflare 访客位置头时，只读取网段内直连代理提供的坐标对。未配置可信客户端 IP 头时，IP 推断使用直连对端地址，该地址从不记录或返回。响应只返回当前请求的可选显示元数据、来源、Provider、精度和 `location_key`，不返回坐标或 IP。`location_key` 由服务端按归一化网格确定性派生的 16 位小写十六进制字符串派生，不直接包含坐标或 IP，同一网格恒定、网格变化时变化，不随 GeoIP 数据库热重载变化；它不是密码学匿名化——网格空间可枚举，仅作为位置作用域身份比较依据，显示字段仅供展示。相同网格共享天气数据缓存，但显示元数据不进入缓存。`GET /api/v1/location` 返回 `schema_version`、位置元数据、可选 `accuracy_radius_km` 和 `location_key`，同样不包含 IP 或坐标。

预警响应是同一位置的完整快照，设备应替换旧列表而不是按 ID 增量合并。空列表表示当前无预警；`truncated=true` 表示上游条目超过公开上限。预警默认新鲜 10 分钟，发生上游故障时最多返回 1 小时内且带 `stale=true` 的缓存。

## IP 位置推断

IP 推断使用本地 GeoLite2 City MMDB 文件，只做内存查询，不调用任何在线定位服务。结果代表“公网出口附近的粗略天气区域”，不等于设备真实位置；移动网络、CGNAT、VPN、代理或企业出口会定位到运营商或代理出口。

```yaml
environment:
  MT_GEOIP_DB: /var/lib/geoip/GeoLite2-City.mmdb
  MT_TRUSTED_CLIENT_IP_HEADER: "CF-Connecting-IP"
  MT_TRUSTED_CLIENT_IP_NETS: "172.30.0.0/16"
volumes:
  - geoip-data:/var/lib/geoip:ro
```

- `MT_TRUSTED_CLIENT_IP_NETS` 必须是直连代理的实际网段（如 Cloudflare Tunnel 容器所在 Docker 子网），且源站端口只能由该代理访问；只有来自这些网段的请求才会读取 `MT_TRUSTED_CLIENT_IP_HEADER`，其余请求使用连接对端地址。
- `MT_TRUSTED_CLIENT_IP_HEADER` 取单个 IP，多值或非法值直接返回 `location_unavailable`。Cloudflare Tunnel 场景使用 `CF-Connecting-IP`；Caddy 等反代请改用能正确传递原始客户端 IP 的配置并给出精确网段。
- 设置头但未设置网段会拒绝启动。私有、环回、链路本地、CGNAT 和文档网段不可定位。
- MMDB 由外部官方 `geoipupdate` 维护：账户凭据放入只对更新任务可见的配置，数据库写入独立私有卷（应用只读挂载）。应用每 5 分钟检测文件替换并热重载，无需重启。
- GeoLite2 数据受 MaxMind 许可约束，包含署名义务；不要把 MMDB 打包进公开镜像。

## Cloudflare 访客位置头

Cloudflare 的“托管转换 → 添加访问者位置标头（Add visitor location headers）”会向源站请求添加 `CF-IPLatitude`、`CF-IPLongitude`、`CF-IPCity`、`CF-Region`、`CF-IPCountry` 和 `CF-Timezone`。启用该转换后，服务可按以下方式解析，无需本地 MMDB：

```yaml
environment:
  MT_CLOUDFLARE_LOCATION_HEADERS: "true"
  MT_TRUSTED_CLIENT_IP_NETS: "127.0.0.1/32"
```

- 启用时必须设置 `MT_TRUSTED_CLIENT_IP_NETS`，否则拒绝启动。这些头只在直连来源属于配置网段时读取；其余请求一律忽略，防止绕过代理伪造位置。请同时把源站端口限制为仅代理可访问。
- 经纬度必须同时出现且各为单值。坐标对完全缺失（例如只出现 `CF-IPCountry`、或根本没有位置头）不会激活该来源，服务继续尝试其他推断（如 MMDB）；一旦坐标对出现但只提供其一、为空白、重复或非法，直接返回 `location_unavailable`，不会降级到 MMDB 或其他来源。
- 结果固定为 `source: "ip"`、`provider: "cloudflare"`、`precision: "coarse"`，不含 `accuracy_radius_km`。显式设备位置头始终优先。
- 仅配置 `CF-IPCountry`（IP Geolocation 设置）不满足本服务的位置需求，必须启用完整“Add visitor location headers”托管转换。

示例 sidecar（仅示意，实际应使用官方镜像并妥善保管凭据）：

```yaml
  geoipupdate:
    image: maxmindinc/geoipupdate:latest
    restart: unless-stopped
    environment:
      GEOIPUPDATE_ACCOUNT_ID: "${MAXMIND_ACCOUNT_ID}"
      GEOIPUPDATE_LICENSE_KEY: "${MAXMIND_LICENSE_KEY}"
      GEOIPUPDATE_EDITION_IDS: "GeoLite2-City"
      GEOIPUPDATE_FREQUENCY: "24"
    volumes:
      - geoip-data:/usr/share/GeoIP
```

## 设备令牌轮换

管理页面允许最多 32 个命名令牌。安全轮换顺序为：创建新令牌、确认新令牌可用、撤销旧令牌。服务拒绝撤销最后一个令牌。令牌原文无法恢复。

## 健康与故障

```sh
curl --fail http://127.0.0.1:8080/health/live
curl http://127.0.0.1:8080/health/ready
```

- 未初始化：live 为 200，ready 为 503 且模块状态为 `setup_required`。
- 正常配置：live 和 ready 均为 200。
- 部分或非法请求位置：天气接口返回 400，不调用 QWeather。
- 省略位置头且未配置推断：天气接口返回 400 `location_required`，`/api/v1/location` 返回 503 `location_unavailable`。
- 省略位置头且已配置推断但推断不可用：天气接口与 `/api/v1/location` 均返回 503 `location_unavailable`。启用 Cloudflare 访客位置头时，可信请求携带非法坐标对直接属于此情况；坐标对完全缺失时，仅在其余推断（如 MMDB）也不可用时才返回 503。
- 位置切换过快：天气接口返回 429，并附带 `Retry-After`。
- QWeather 认证熔断：live 为 200，ready 为 503；已有缓存允许时仍可返回 `stale=true`。
- 状态目录同步未确认：写操作仍成功并返回 `X-MT-State-Warning: durability_unconfirmed`，管理概览持续显示告警；检查状态卷后执行下一次管理写入以重新确认。

管理页面更新 QWeather 时会先真实请求实时天气和预警接口。任一验证或写盘失败都不会替换原配置。管理概览的运行诊断只显示供应商状态和按数据种类聚合的内存缓存计数；日志只记录事件类别、请求 ID、路径和安全错误，不记录请求体、IP、坐标、位置元数据或凭据。

## 备份、升级与回滚

- `mt-server-state` 卷包含明文 QWeather 私钥和管理员验证器，必须使用加密备份并限制读取权限。
- 仅部署语义化版本标签或镜像 digest，禁止使用 `latest`。
- v0.3.0 使用 state schema v4。升级前仍必须加密备份 `mt-server-state`；首次读取 v0.2.x 的 schema v3 时，服务会完整校验并自动迁移，同时以 `0600` 权限保留 `state.v3.backup.json`。
- schema v2 不自动迁移。v0.1.x 部署必须继续按 v0.2.0 文档先备份并重新初始化，不能跳过缺失的管理 Origin 信任信息。
- 升级后观察 `/health/ready`、容器重启次数、管理概览诊断和最近日志。迁移前备份含完整 QWeather 私钥，不得下载到不受保护的位置或通过网页公开。
- 回滚 v0.2.0 前，先停止 v0.3.0，使用 v0.3.0 镜像和同一状态卷执行 `mt-server state restore-v3-backup`，再把镜像固定为 v0.2.0 后启动。恢复会丢弃迁移后产生的管理配置变更，应优先保留完整加密卷备份。

HTTPS Compose 回滚示例：

```sh
MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml stop mt-server
MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml run --rm --no-deps mt-server \
  state restore-v3-backup
MT_SERVER_IMAGE=ghcr.io/mingyuan0415/mt-server:v0.2.0 \
  MT_PUBLIC_HOST=api.example.com docker compose -f deploy/compose.https.yaml up -d
```

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
