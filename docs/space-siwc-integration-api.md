# Space SIWC 对外授权与分组导入

两个接口沿用管理员 Bearer JWT 或管理员 x-api-key，普通推理 Key 无权调用。HTTPS 部署。响应 no-store；审计省略请求体。

## POST /api/v1/admin/openai/siwc/integration/generate-auth-url

JSON：email（必填）、ext_agent_host_id（Space 稳定宿主标识，必填）、proxy_url（当前登录代理，可空，临时保留不导入代理管理）。
成功：`{code:0,data:{auth_url,session_id,ext_agent_host_id,registration}}`。
固定回调 http://127.0.0.1:1456/auth/callback。PKCE/state/nonce 与签名校验复用原 SIWC 实现。会话30分钟、绑定管理员、重启失效。

## POST /api/v1/admin/openai/siwc/integration/callback

必须携带固定的 Idempotency-Key。JSON：session_id、callback_url（完整地址）、email、web_access_token、web_account_id、name、group_ids（非空正整数数组）、proxy_id（可选）、concurrency、priority（1～100）、models（数组，空不限制）。

校验回调地址、重复参数、state、签发clientID、Token签名、账号邮箱和订阅授权范围后，导入配置分组并添加SIWC标记。
成功：`{code:0,data:{account:{id,name,extra},tokens:{access_token,refresh_token,id_token,client_id,auth_mode,email,subject,ext_agent_host_id,expires_at,granted_scopes,plan_use_enabled}}}`。

网页AT只供原网页额度查询，和SIWC凭据独立保存。Space收到结果后完成第三步授权及第四步推送，不再通用建号。代理分配、累计次数与定时解绑仍由Space原规则管理。

同key不同请求返回409。幂等成功记录只存账号ID，重试读取原账号当前凭据，不重复交换或建号，默认保留24小时。结果未知时保留原请求，不把未知当失败重建。会话或进程在交换/建号期间失效需要先核对。普通OAuth、SIWC原页面与续期不改。
