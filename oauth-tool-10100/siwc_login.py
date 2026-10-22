#!/usr/bin/env python3
"""SIWC 授权导入工具（跑在用户本机，配合 sub2api 的 /siwc/* 管理接口）。

流程：
  1. 用管理员账号登录 sub2api 拿 JWT；
  2. POST /api/v1/admin/openai/siwc/generate-auth-url 生成授权 URL
     （首次注册 client_id=dynamic_agent_client；ext_agent_host_id 本地持久化复用）；
  3. 打开系统浏览器（建议用已登录 ChatGPT 的浏览器/配置文件）；
  4. 本机 127.0.0.1:<port> 监听 /auth/callback，接住 code/state/签发 client_id；
  5. POST /api/v1/admin/openai/siwc/create-from-oauth 换 token + 建号
     （服务端完成 PKCE 交换、ID token JWKS 验签、scope 检查）。

用法：
  python siwc_login.py                       # 默认全流程
  python siwc_login.py --port 54321          # 换本地回调端口
  python siwc_login.py --print-only          # 只打印授权 URL（手动在带代理的浏览器打开）
  python siwc_login.py --name 我的SIWC号 --group-ids 1,2
"""
import argparse
import http.server
import json
import pathlib
import socketserver
import sys
import threading
import time
import urllib.parse
import urllib.request
import webbrowser

STATE_FILE = pathlib.Path(__file__).resolve().parent / "siwc-state.json"
CALLBACK_PATH = "/auth/callback"


def api(base: str, path: str, token: str | None = None, payload: dict | None = None,
        method: str | None = None, timeout: int = 60) -> dict:
    url = base.rstrip("/") + path
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(url, data=data, method=method or ("POST" if data else "GET"))
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = json.load(resp)
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", "replace")[:500]
        raise SystemExit(f"HTTP {exc.code} {path}: {detail}")
    if body.get("code") not in (0, None):
        raise SystemExit(f"API 错误 {path}: {json.dumps(body, ensure_ascii=False)[:500]}")
    return body.get("data") or body


def load_state() -> dict:
    if STATE_FILE.exists():
        try:
            return json.loads(STATE_FILE.read_text(encoding="utf-8"))
        except Exception:
            return {}
    return {}


def save_state(state: dict) -> None:
    STATE_FILE.write_text(json.dumps(state, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"[state] 已保存 {STATE_FILE.name}（ext_agent_host_id 复用于后续所有 SIWC 注册）")


class CallbackCatcher:
    """单次接住 127.0.0.1 回调；返回 query dict。"""

    def __init__(self, port: int):
        self.port = port
        self.result: dict | None = None
        catcher = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):  # noqa: N802
                parsed = urllib.parse.urlparse(self.path)
                if parsed.path != CALLBACK_PATH:
                    self.send_error(404)
                    return
                catcher.result = dict(urllib.parse.parse_qsl(parsed.query))
                body = (
                    "<html><body style='font-family:system-ui;padding:40px'>"
                    "<h2>✅ 授权码已接收</h2>"
                    "<p>本页面可以关闭，回到终端查看导入结果。</p></body></html>"
                ).encode()
                self.send_response(200)
                self.send_header("Content-Type", "text/html; charset=utf-8")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
                threading.Thread(target=server.shutdown, daemon=True).start()

            def log_message(self, *args):
                pass

        server = socketserver.TCPServer(("127.0.0.1", port), Handler)
        server.daemon_threads = True
        self.server = server

    def wait(self, timeout: float) -> dict:
        thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        thread.start()
        deadline = time.time() + timeout
        while time.time() < deadline and self.result is None:
            time.sleep(0.3)
        self.server.shutdown()
        return self.result or {}


def main() -> None:
    parser = argparse.ArgumentParser(description="SIWC 授权导入 sub2api")
    parser.add_argument("--base", default="http://YOUR-SUB2API-HOST:10100", help="sub2api 地址")
    parser.add_argument("--email", default="admin@sub2api.local", help="sub2api 管理员登录名")
    parser.add_argument("--password", default="CHANGE-ME-ADMIN-PASSWORD", help="sub2api 管理员密码")
    parser.add_argument("--port", type=int, default=1455, help="本地回调监听端口")
    parser.add_argument("--agent-name", default="sub2api", help="agent_name_hint（首次注册展示的应用名）")
    parser.add_argument("--name", default="", help="账号备注名（默认用账号邮箱）")
    parser.add_argument("--group-ids", default="2", help="逗号分隔的分组 ID（默认 2：10100 实例的 openai 分组）")
    parser.add_argument("--proxy-id", type=int, default=None, help="sub2api 代理 ID（可选，服务器访问 OpenAI 用）")
    parser.add_argument("--concurrency", type=int, default=70, help="账号并发上限（默认 70）")
    parser.add_argument("--print-only", action="store_true", help="只生成并打印授权 URL，不启动本地监听")
    parser.add_argument("--timeout", type=int, default=600, help="等待浏览器回调的超时秒数")
    args = parser.parse_args()

    print(f"[1/5] 登录 sub2api {args.base} …")
    token = api(args.base, "/api/v1/auth/login",
                payload={"email": args.email, "password": args.password})["access_token"]

    state = load_state()
    ext_host_id = state.get("ext_agent_host_id", "")
    redirect_uri = f"http://127.0.0.1:{args.port}{CALLBACK_PATH}"

    print("[2/5] 生成 SIWC 授权 URL …")
    gen = api(args.base, "/api/v1/admin/openai/siwc/generate-auth-url", token=token, payload={
        "redirect_uri": redirect_uri,
        "agent_name_hint": args.agent_name,
        "ext_agent_host_id": ext_host_id,
    })
    print(f"      会话: {gen['session_id']}")
    print(f"      宿主标识: {gen['ext_agent_host_id']}  (registration={gen['registration']})")
    if state.get("ext_agent_host_id") != gen["ext_agent_host_id"]:
        save_state({"ext_agent_host_id": gen["ext_agent_host_id"]})

    print("\n[3/5] 在浏览器完成 ChatGPT 授权（建议用已登录 ChatGPT 的浏览器）：\n")
    print("      " + gen["auth_url"] + "\n")
    if args.print_only:
        print("--print-only：不启动本地监听。浏览器授权后地址栏会变成")
        print(f"      {redirect_uri}?code=...&state=...&client_id=...")
        print("      复制该 URL 后手工调用 create-from-oauth（见 SIWC-DESIGN.md §5）。")
        return
    try:
        webbrowser.open(gen["auth_url"])
    except Exception:
        pass

    print(f"[4/5] 本地监听 127.0.0.1:{args.port} 等待回调（最长 {args.timeout}s，可随时 Ctrl+C 退出）…")
    callback = CallbackCatcher(args.port).wait(args.timeout)
    if not callback:
        raise SystemExit("超时未收到回调；重跑本工具重新生成授权 URL。")
    if callback.get("error"):
        raise SystemExit(f"授权被拒绝或失败: {callback.get('error')} / {callback.get('error_description','')}")
    code = callback.get("code", "")
    cb_state = callback.get("state", "")
    issued_client_id = callback.get("client_id", "")
    if not code or not cb_state:
        raise SystemExit(f"回调参数不完整: {callback}")
    print(f"      收到 code（长度 {len(code)}）、签发 client_id={issued_client_id or '(复授权未回传)'}")

    print("[5/5] 换取 token 并创建账号 …")
    create = {
        "session_id": gen["session_id"],
        "code": code,
        "state": cb_state,
        "issued_client_id": issued_client_id,
        "redirect_uri": redirect_uri,
        "name": args.name,
        "concurrency": args.concurrency,
    }
    if args.proxy_id is not None:
        create["proxy_id"] = args.proxy_id
    if args.group_ids:
        create["group_ids"] = [int(x) for x in args.group_ids.split(",") if x.strip()]
    account = api(args.base, "/api/v1/admin/openai/siwc/create-from-oauth", token=token, payload=create)

    print("\n✅ 导入成功：")
    print(f"   账号 ID:  {account.get('id')}")
    print(f"   名称:     {account.get('name')}")
    print(f"   平台/类型: {account.get('platform')}/{account.get('type')}")
    print(f"   状态:     {account.get('status')}")
    print("\n后续：")
    print("   · 该账号的请求会自动走 SIWC 插件直连 api.openai.com/v1/responses（无需任何开关）；")
    print("   · access token 由服务器自动刷新（refresh token 30 天滚动）；")
    print("   · 把账号加入分组后即可在网关用对应模型（注意 SIWC 仅支持 Responses API 的对话模型）。")


if __name__ == "__main__":
    main()
