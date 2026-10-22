# SIWC 网页授权

管理员侧栏新增“SIWC 授权”，路径 `/admin/siwc`。

1. 选择“当前 sub2api”，或填写另一台支持 SIWC 的 sub2api 地址、管理员邮箱和密码，连接读取分组。
2. 选择一个或多个 OpenAI 分组，再生成授权链接。
3. 在本机浏览器打开链接，登录 ChatGPT 并完成授权。
4. 浏览器跳转到 `http://127.0.0.1:1456/auth/callback?...` 后，复制地址栏的完整地址，粘贴到菜单的回调框，点击“完成授权并导入”。

回调页面提示无法访问是正常的，本模式不运行本地监听服务。不要只复制 code，完整地址还包含 state 和首次注册的 client_id。1456 与原 Python 工具默认的 1455 分开，避免已有监听程序接收网页生成的回调。

请保留生成链接的页面。会话最长 30 分钟；刷新页面、服务器重启或会话过期后，需要重新生成链接。旧脚本生成的回调不能混用于网页新建的会话。

## 与原脚本的对应关系

网页沿用 `oauth-tool/siwc_login.py` 的 API 流程：

- 登录目标 sub2api；当前站点直接使用已登录的管理员会话。
- 调用 `/api/v1/admin/openai/siwc/generate-auth-url`，传递 loopback redirect_uri、agent_name_hint 和持久化的 ext_agent_host_id。
- 保留返回的 session_id，在本机浏览器授权后提取 code、state、client_id。
- 调用 `/api/v1/admin/openai/siwc/create-from-oauth`，带上同一 session_id、redirect_uri、回调参数、名称和所选 group_ids；并发沿用原默认值 70。

原脚本也提供 `--print-only` 手动回调模式。网页实现的是该模式的交互界面，不需要安装 Python。原 SIWC PKCE、state、nonce、ID token 校验、token 交换和刷新实现保持不变，插件也保持不变。新建 SIWC 账号名称自动附加 `+siwc`，已存在后缀时不重复添加。

## 正式环境

当前站点的接口地址随页面所在域名自动变化，不写死本机 sub2api 地址。loopback 回调仍然指向操作人员的本机浏览器，因此部署到 Linux 或正式域名后，操作步骤相同。

目标站点需要有原 SIWC 接口并安装、启用 SIWC 插件；执行授权交换的服务器需要能访问 OpenAI。浏览器使用的本机代理不会自动成为正式服务器的出站代理。

“其他 sub2api”通过本项目后端转发固定的登录、分组和 SIWC 接口，避免浏览器跨域。远程管理员密码不保存，远程访问令牌仅保留在与当前管理员绑定的短期内存连接中，退出页面会断开连接。连接最长 30 分钟。远程账号若要求验证码或二次验证，请直接登录其站点使用 SIWC 菜单。

若导入请求超时，请先查看目标账号列表，确认是否已创建成功，再决定是否重新授权。网页不会重复提交可能已经使用的回调。
