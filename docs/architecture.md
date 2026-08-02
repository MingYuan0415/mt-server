# 架构

## 模块化单体

```text
cmd/mt-server
    -> internal/app                    组合根与动态运行时
       -> internal/platform/*         状态、鉴权、HTTP、健康与请求位置
       -> internal/modules/admin      管理 API 与嵌入式网页
       -> internal/modules/weather    稳定天气模型、限速与缓存
          -> internal/providers/qweather
```

所有模块在编译期显式注册。服务不加载动态插件、不提供任意上游转发，也不依赖数据库或外部缓存。供应商原始响应只存在于适配层。

## 初始化与状态

进程启动只读取监听地址、日志级别、状态目录和管理传输策略。若 `state.json` 不存在，服务进入 `setup_required`，管理页面仍可使用，设备天气接口返回 `service_unconfigured`。

网页提交完整配置时必须通过同源、CSRF 和全局限速校验。QWeather 实时验证必须带一次性浏览器测试位置；验证成功后才以 `0600` 权限原子创建状态。临时位置不持久化，也不进入天气缓存。

schema v2 状态包含 Argon2id 管理员验证器、QWeather 私钥、缓存设置和设备令牌哈希，不包含定位配置或设备位置。未知 schema 或损坏状态会使启动失败，不会静默回到初始化状态。

## 天气请求流程

```text
Bearer 认证
  -> 解析 X-MT-Location-* 请求头
  -> 校验并归一化到 0.1 度网格
  -> 按 DeviceID 限制位置网格跳变
  -> 查询或命中 QWeather 缓存
  -> 返回当前请求的显示元数据，不返回坐标或 IP
```

纬度、经度和位置来源标识必填；城市、地区、国家和时区可选。服务器不读取连接来源 IP、`X-Forwarded-For`、代理位置头或查询参数，也不发起位置解析网络请求。

Bearer token 持有者可以声明任意合法位置。每个 DeviceID 的位置变更令牌桶容量为 4，每 5 分钟恢复 1 次；同一网格不计数，超限返回 `429`。内存 LRU 最多保存 64 个位置，天气数据按网格、种类、语言和单位隔离；显示元数据始终来自当前请求，不跨请求缓存。

## 动态运行时

```text
QWeather 管理写操作
  -> 本地字段与密钥校验
  -> 使用浏览器临时位置调用 QWeather current
  -> 原子持久化候选状态
  -> 替换 QWeather、缓存与鉴权运行时
  -> 关闭旧缓存后台任务
```

QWeather 更新会创建新运行时并清空旧缓存；失败时保留原状态和运行时。设备令牌变更只替换鉴权器，并清除已撤销 DeviceID 的位置限速状态。

## 管理安全

管理员密码使用 Argon2id。管理 session 和 CSRF token 仅存在内存，重启后失效。所有写操作要求同源 Origin 和 CSRF；初始化与登录使用不记录 IP 的全局限速。Cookie 使用 `HttpOnly`、`SameSite=Strict`，直接 TLS 或显式配置 `MT_ADMIN_BEHIND_HTTPS_PROXY=true` 时增加 `Secure`。

服务不信任 `X-Forwarded-Proto`。LAN 模板显式允许管理 HTTP；HTTPS 代理模板通过部署配置声明外部连接为 HTTPS，源站仍不得暴露到不受信网络。

## 扩展规则

- 新业务使用独立版本化路径，不复用天气 DTO。
- 新天气供应商实现 `weather.Provider`，原始供应商结构不得越过适配层。
- 需要持久化的新模块扩展内部 state schema，并提供明确的升级策略。
- 管理 API 不得复用设备 Bearer token。
