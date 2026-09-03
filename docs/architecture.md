# 架构

## 模块化单体

```text
cmd/mt-server
    -> internal/app                    组合根与动态运行时
       -> internal/platform/*         状态、鉴权、HTTP、健康与请求位置
       -> internal/modules/admin      管理 API 与嵌入式网页
       -> internal/modules/weather    稳定天气模型、限速与缓存
       -> internal/modules/location   设备位置 API
          -> internal/providers/qweather
          -> internal/providers/geoip GeoLite2 City 本地读取与热重载
```

所有模块在编译期显式注册。服务不加载动态插件、不提供任意上游转发，也不依赖数据库或外部缓存。供应商原始响应只存在于适配层。

## 初始化与状态

进程启动只读取监听地址、日志级别、状态目录、管理传输策略，以及可选的 GeoLite2 数据库路径与可信客户端 IP 配置（均为环境变量，不写入状态）。若 `state.json` 不存在，服务进入 `setup_required`，管理页面仍可使用，设备天气接口返回 `service_unconfigured`。

网页提交完整配置时必须通过同源、CSRF 和全局限速校验。QWeather 实时验证必须带一次性浏览器测试位置；验证成功后才以 `0600` 权限原子创建状态。临时位置不持久化，也不进入天气缓存。

状态替换分为明确的提交边界：临时文件写入、文件同步或 rename 前失败均不改变活动状态；rename 后目录同步失败视为逻辑提交成功，但管理响应带 `X-MT-State-Warning: durability_unconfirmed`，状态接口同时报告 `state_durability: unconfirmed`。下一次完整同步成功后告警清除。

schema v4 状态包含 Argon2id 管理员验证器、管理 HTTPS Origin、QWeather 私钥、四类天气缓存设置和设备令牌哈希，不包含定位配置或设备位置。启动时会严格读取 schema v3，在完整校验候选状态后先原子保存 `state.v3.backup.json`，再迁移为 v4 并激活运行时。schema v2、未知 schema 或损坏状态会使启动失败，不会静默回到初始化状态。

## 天气请求流程

```text
Bearer 认证
  -> 解析 X-MT-Location-* 请求头（全有或全无）
  -> 无位置头时读取可信直连代理的 Cloudflare 访客位置头
  -> 仍无位置时按可信客户端 IP 推断位置
  -> 校验并把坐标归一化到两位小数精度，派生 location_key
  -> 按 DeviceID 限制位置跳变
  -> 并行：查询或命中 QWeather 天气缓存 + 尽力反查中文显示名称
  -> 返回当前请求的显示元数据与 location_key，不返回坐标或 IP
```

纬度、经度和位置来源标识需要同时提供或同时省略；只提供一部分按客户端错误处理。城市、地区、国家和时区可选。服务器不读取任意转发头（如 `X-Forwarded-For`）或查询参数，也不发起位置解析网络请求；配置了 `MT_CLOUDFLARE_LOCATION_HEADERS` 时只读取网段内直连代理提供的 Cloudflare 访客位置头，其余请求一律忽略这些头。未配置可信客户端 IP 头时，IP 推断使用直连对端地址，该地址从不记录或返回。

省略位置头时，若配置了 `MT_CLOUDFLARE_LOCATION_HEADERS=true`，`internal/platform/location` 的 Cloudflare 解析器只从 `MT_TRUSTED_CLIENT_IP_NETS` 内直连代理读取 `CF-IPLatitude`/`CF-IPLongitude` 坐标对（城市等为可选显示元数据），返回 `source: "ip"`、`provider: "cloudflare"`、`precision: "coarse"` 的归一化点；坐标对出现但缺失、重复或非法时直接 `location_unavailable`，不降级。若进程配置了 `MT_GEOIP_DB`，未出现 Cloudflare 坐标对时 `internal/providers/geoip` 会查询本地 GeoLite2 City 数据库，返回 `source: "ip"`、`precision: "coarse"` 的归一化点；该结果只存在内存，不落盘。可信客户端 IP 头是可选配置：配置 `MT_TRUSTED_CLIENT_IP_HEADER` 与 `MT_TRUSTED_CLIENT_IP_NETS` 时，客户端 IP 只从网段内直连代理提供的该头读取，其余请求使用连接对端地址；头值严格解析为单个 IP，多值或非法值直接拒绝。未配置可信头时使用直连对端地址，该地址从不记录或返回。私有、环回、链路本地、CGNAT 和特殊用途网段不可定位。MMDB 文件由外部 `geoipupdate` 原子替换，Store 每 5 分钟检测文件变化并热重载。

归一化完成后，`location.Point.Key` 由规范坐标字符串（`"lat,lon"` 两位小数）的 FNV-1a 64 哈希派生成 16 位小写十六进制 `location_key`。它不直接包含坐标或 IP，同一位置恒定、位置变化时变化，不随 GeoIP 热重载变化；该值无密钥，是稳定的不透明作用域标识而非密码学匿名化。天气缓存键与限速键同样使用规范坐标字符串，`location_key` 只是同一位置的额外派生，不改变缓存和限速行为。

Bearer token 持有者可以声明任意合法位置。每个 DeviceID 的位置变更令牌桶容量为 4，每 5 分钟恢复 1 次；同一位置不计数，超限返回 `429`。内存 LRU 最多保存 64 个位置，天气数据按归一化位置、种类、语言和单位隔离；显示元数据始终来自当前请求，不跨请求缓存。

天气与位置接口会尽力把显示名称本地化为中文：活动 QWeather Provider 通过 `GET /geo/v2/city/lookup`（`number=1`、`lang=zh`，仅使用归一化后的两位小数坐标）反查地点，把 `adm2` 映射到 `city`（缺少时回退到 `name`）、`name` 映射到新增的 `district` 区县字段、`adm1` 映射到 `region`、`tz` 映射到 `timezone`；`country` 保持 ISO 代码不变。查询成功才覆盖显示字段，`source`、`provider`、`precision`、`location_key` 与坐标获取语义不变。GeoAPI 数据按 QWeather 许可不做缓存或持久化：每次请求都实时查询，受每设备令牌桶（容量 20、每 5 分钟恢复 1）和全局 4 个在途上限约束；预算耗尽、上游失败或超过 3 秒预算时回退请求自身的元数据。天气查询与本地化查询并行执行，天气失败时先取消并等待本地化协程结束，避免运行时替换期间使用已关闭的 Provider。`GET /api/v1/location` 仅在本地化成功且确有显示字段被覆盖时返回可选 `localization` 归属对象（QWeather 及官网链接），客户端展示位置名称时必须可见署名；天气响应的 QWeather 归属已在 `source` 中。海外地点按 QWeather 多语言回退规则可能返回当地官方语言或英文名称。

`GET /api/v1/location` 使用同一位置解析逻辑，返回城市、可选区县、地区、国家、时区、可选 `accuracy_radius_km` 和 `location_key`，不返回 IP 或坐标。天气接口在推断不可用时返回 `503 location_unavailable`。

## 动态运行时

```text
QWeather 管理写操作
  -> 本地字段与密钥校验
  -> 使用浏览器临时位置调用 QWeather current 与 alerts
  -> 原子持久化候选状态
  -> 替换 QWeather、缓存与鉴权运行时
  -> 关闭旧缓存后台任务
```

QWeather 更新会创建新运行时并清空旧缓存；失败时保留原状态和运行时。设备令牌变更只替换鉴权器，并清除已撤销 DeviceID 的位置限速状态。

活动 QWeather Provider 最多执行 8 个并发上游请求，等待并发槽时响应请求取消。管理连接测试全局单并发且每分钟最多 6 次。网络错误和 5xx 立即重试一次；随后缓存按 5 秒起步、带 20% 抖动的指数退避冷却，最长 5 分钟。天气请求的 401/403 打开 15 分钟凭据熔断，429 按 `Retry-After` 建立最长 15 分钟的账户阻断，400 只冷却当前缓存键。熔断期间健康检查不可用，但未超过上限的陈旧缓存仍可返回。GeoAPI 显示名称查询是尽力而为的例外：其 401/403/429 不打开上述熔断，只回退请求自身元数据，天气可用性不受影响。

实时、逐小时、逐日和预警分别缓存。预警新鲜期为 10 分钟，陈旧上限为 1 小时；响应是完整快照，空列表代表当前无预警。供应商字段在适配层规范化为稳定等级、状态和 CAP 紧急度/确定性，原始结构不会越过适配层。

已认证管理接口可以读取进程内诊断快照，包括供应商阻断状态和按天气种类聚合的缓存计数。诊断读取不调用上游，且不按设备、令牌、位置或 IP 建立维度；进程重启或 QWeather 运行时替换会重置这些数据。

## 管理安全

管理员密码使用 Argon2id，要求至少 12 个 Unicode code point 且不超过 128 个 UTF-8 字节。管理 session 和 CSRF token 仅存在内存，重启后失效。所有写操作要求同源 Origin 和 CSRF；初始化与登录使用不记录 IP 的全局限速。Cookie 使用 `HttpOnly`、`SameSite=Strict`，直接 TLS 或显式配置 `MT_ADMIN_BEHIND_HTTPS_PROXY=true` 时增加 `Secure`。

代理部署把浏览器可见的 HTTPS Origin 持久化到状态卷，同源校验不依赖代理内部 `Host`。初始化候选列表必须包含当前标准 `Origin`；管理写入在状态提交后原子替换活动白名单。添加入口保留会话，删除入口清除全部会话，且当前入口不可删除。服务不信任 `Forwarded` 或任何 `X-Forwarded-*` 头。LAN 模板显式允许管理 HTTP；HTTPS 源站不得暴露到不受信网络。

服务进程在整个生命周期持有状态目录独占锁。离线管理域名和迁移备份恢复命令使用同一把锁，运行中的服务不会与维护命令并发覆盖 `state.json`。

## 扩展规则

- 新业务使用独立版本化路径，不复用天气 DTO。
- 新天气供应商实现 `weather.Provider`，原始供应商结构不得越过适配层。
- 需要持久化的新模块扩展内部 state schema，并提供明确的升级策略。
- 管理 API 不得复用设备 Bearer token。
