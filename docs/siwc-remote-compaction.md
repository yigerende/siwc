# SIWC 远程压缩兼容

账号管理顶部的独立开关“SIWC 远程压缩兼容”默认关闭。配置键
siwc_remote_compaction_compat_enabled，管理员 GET/PUT
/api/v1/admin/siwc/remote-compaction。与 configuration_update 开关独立。

## 范围

仅开启时的原生流式 /responses + input compaction_trigger 请求允许 SIWC
账号进入压缩候选。普通 OAuth 的可用性、优先级、请求、响应、模型映射及重试
逻辑保持原样。旧 /responses/compact、无 stream:true、正常聊天和已有
context_management 请求不进入适配器。账号自身 force_off 仍生效。

开关在请求开始时读取并固定到该请求上下文。关闭后的新请求恢复旧行为，
不取消正在执行的请求。配置缺失默认 false；保存成功立即更新本进程缓存，
保存失败不改变已生效状态。重启从数据库读取，DB 读失败保留上次已知值，
没有缓存时关闭。多实例缓存最长60秒；当前正式仅一个应用实例。

## 请求和响应适配

选中 SIWC 后，在原 HTTP/插件出站点移除 compaction_trigger，保留其他输入
（包括工具调用及结果）、模型、会话标识和工具定义，写入
context_management=[{type:compaction,compact_threshold:1000}]。
其他 context_management 项保留。store=false；有工具定义时 tool_choice=none，
避免维护回合继续执行上游工具。不新增普通 OAuth 回退，不修改插件。

由上游完成加密压缩。1000 是真实接口验证的最低阈值，低于此值返回400；
短历史未产出 compaction 时明确失败，绝不填充历史或把普通回答作为压缩成功。
一次上游请求可以产生多个 compaction，中间夹有普通回答。适配器保留最后一个
有效加密状态，验证最终 response.completed，再仅向客户端发送一个 compaction
output_item.done 及 response.completed，保留该请求实际 usage。
流内上游错误保留错误码；断流、损坏数据、空密文和无压缩结果均失败。

响应解析按需读取，没有后台 goroutine，每行上限16MiB、整流64MiB。客户端
结束会关闭上游。上游加密状态仅在当前请求内存中，不新增落盘/明文日志。

## 验证

- 默认关闭、开启、再关闭、重新读取、缓存、并发读写、保存失败。
- 普通 OAuth auto/force_on/force_off 的压缩优先级及出站路径不变。
- 请求原件与重放体、工具配对、会话、模型、已有自动压缩和旧压缩路由。
- 多次压缩取最终状态、短历史、流式失败/限流、EOF、损坏/超长流、Close。
- 真实上游两次触发压缩后移除旧历史，继续工具读文件/提交修复，20个独立
  Python用例通过；保留原需求、禁止修改文件、撤回的500重试条件及0边界。
  另一个模型读取加密状态继续测试通过。关闭后旧503路径保持。
- 可选 TestSIWCRemoteCompactionLiveBridge 用于隔离客户端验收，默认跳过，
  不启动数据库、worker，不带入真实客户提示词。真实密钥仅在服务端内存。
