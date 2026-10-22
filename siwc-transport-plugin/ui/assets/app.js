// SIWC Transport 插件配置页 — UI Bridge v1 极简实现。
// 信封：{source, bridge_token, type, request_id[, config|height|level|message]}
// 校验：event.source === parent、来源标识、bridge token、等待中的 request_id。
(function () {
  "use strict";

  var bridgeToken = "";
  var pending = Object.create(null);
  var seq = 0;
  var TIMEOUT_MS = 15000;

  function send(type, payload) {
    return new Promise(function (resolve, reject) {
      var id = "req-" + (++seq);
      pending[id] = { resolve: resolve, reject: reject };
      window.parent.postMessage(
        Object.assign({
          source: "sub2api-plugin-ui",
          bridge_token: bridgeToken,
          type: type,
          request_id: id
        }, payload || {}),
        "*"
      );
      setTimeout(function () {
        if (pending[id]) {
          pending[id].reject(new Error("请求超时: " + type));
          delete pending[id];
        }
      }, TIMEOUT_MS);
    });
  }

  window.addEventListener("message", function (event) {
    if (event.source !== parent) return;
    var data = event.data;
    if (!data || data.source !== "sub2api-plugin-host") return;
    if (data.bridge_token !== bridgeToken) return;
    var waiter = pending[data.request_id];
    if (!waiter) return;
    delete pending[data.request_id];
    if (data.ok) waiter.resolve(data);
    else waiter.reject(new Error(data.message || "宿主返回失败"));
  });

  function readToken() {
    var match = /(?:^#|&)bridge_token=([^&]+)/.exec(window.location.hash || "");
    bridgeToken = match ? decodeURIComponent(match[1]) : "";
    if (!bridgeToken) setStatus("未找到 Bridge Token，无法与宿主通信", "err");
  }

  function setStatus(text, level) {
    var el = document.getElementById("status");
    el.textContent = text;
    el.className = level || "dim";
  }

  function resize() {
    var height = Math.max(
      document.body.scrollHeight,
      document.documentElement.scrollHeight
    );
    window.parent.postMessage({
      source: "sub2api-plugin-ui",
      bridge_token: bridgeToken,
      type: "ui.resize",
      request_id: "resize-" + (++seq),
      height: height
    }, "*");
  }

  function configFromForm() {
    return {
      direct_base_url: document.getElementById("direct_base_url").value.trim(),
      user_agent: document.getElementById("user_agent").value.trim(),
      force_store_false: document.getElementById("force_store_false").checked,
      force_stream: document.getElementById("force_stream").checked,
      rewrite_chatgpt_codex_urls: document.getElementById("rewrite_chatgpt_codex_urls").checked
    };
  }

  function formFromConfig(config) {
    document.getElementById("direct_base_url").value = config.direct_base_url || "https://api.openai.com";
    document.getElementById("user_agent").value = config.user_agent || "sub2api-siwc-transport/0.1.0";
    document.getElementById("force_store_false").checked = config.force_store_false !== false;
    document.getElementById("force_stream").checked = config.force_stream !== false;
    document.getElementById("rewrite_chatgpt_codex_urls").checked = config.rewrite_chatgpt_codex_urls !== false;
  }

  function renderStatus(result) {
    var lines = [];
    var statusJson = result && result.result && result.result.status_json;
    var health = statusJson ? JSON.parse(statusJson) : null;
    if (health) {
      lines.push("宿主服务: " + (health.host_service_ready ? "已连接 (v" + health.host_service_api_version + ")" : "未就绪"));
      lines.push("缓存账号: " + health.cached_accounts);
      lines.push("累计请求: " + health.requests_total + "（SIWC " + health.siwc_requests + " / 透传 " + health.passthrough_requests + "）");
      lines.push("错误: " + health.errors_total + "　进行中: " + health.in_flight);
      if (health.directory_last_error) lines.push("目录错误: " + health.directory_last_error);
      if (health.config) lines.push("direct_base_url: " + health.config.direct_base_url);
    }
    if (result && result.result && result.result.message) lines.push("测试: " + result.result.message);
    setStatus(lines.join("\n") || JSON.stringify(result), "");
  }

  document.getElementById("save").addEventListener("click", function () {
    setStatus("保存中…", "dim");
    send("config.save", { config: configFromForm() })
      .then(function (data) { formFromConfig(data.config || {}); setStatus("已保存", "ok"); resize(); })
      .catch(function (err) { setStatus("保存失败: " + err.message, "err"); });
  });

  document.getElementById("test").addEventListener("click", function () {
    setStatus("测试中…", "dim");
    send("config.test")
      .then(function (data) { renderStatus(data); setStatus((data.result && data.result.message ? data.result.message : "测试完成"), data.result && data.result.success ? "ok" : "err"); })
      .catch(function (err) { setStatus("测试失败: " + err.message, "err"); });
  });

  document.getElementById("refresh").addEventListener("click", function () {
    send("plugin.status").then(renderStatus).catch(function (err) { setStatus("状态读取失败: " + err.message, "err"); });
  });

  window.addEventListener("beforeunload", function () { /* bridge 随 iframe 销毁 */ });

  readToken();
  send("sub2api.plugin.ready", {}).catch(function () { /* ready 无响应属正常 */ });
  send("config.load")
    .then(function (data) { formFromConfig(data.config || {}); setStatus("配置已加载", "ok"); resize(); })
    .catch(function (err) { setStatus("配置加载失败: " + err.message, "err"); });
  send("plugin.status").then(renderStatus).catch(function () { /* 状态仅增强展示 */ });
  resize();
})();
