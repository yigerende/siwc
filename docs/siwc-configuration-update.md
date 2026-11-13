# SIWC 配置更新兼容

账号管理顶部新增全局开关，默认关闭。设置保存在独立键
`siwc_configuration_update_compat_enabled`，重启后保留；GET/PUT
`/api/v1/admin/siwc/configuration-update` 受现有管理员认证、审计和限流保护。

开启时，在分组映射之后、交给原 SIWC 传输插件之前，过滤 Responses
`input` 数组中 `type=configuration_update` 的记录。参考 BPS SIWC 的处理，
提取最后一个包含非空 `reasoning.effort` 的配置记录，仅在请求顶层没有该字段时补入。
已有顶层参数（包括分组映射结果）优先，`none` 不改写。
配置记录的其他字段不转换；其他输入记录、工具定义及调用结果保留。

关闭时不转换请求，普通 OAuth/API Key 不受影响。覆盖常规和透传 HTTP
请求构造路径；现有 SIWC 插件和远程压缩/搜索路径不变。
没有配置更新记录的请求不读取此设置；有记录时使用进程缓存（60 秒），本进程
保存立即更新，跨实例最多 60 秒同步。数据库读取失败保留上次有效值，5 秒后重试；
没有有效值则保持关闭。读取与保存串行，避免旧读取覆盖新开关。

本修复只解决此输入类型导致的 `subscription_sharing_unsupported_capability`。
其他不支持的类型仍按原逻辑处理，不承诺消除所有相同错误码。
