// 流式对话层：htmx 负责请求-响应界面，这里只补它不擅长的增量渲染。
(function () {
  "use strict";
  var state = document.getElementById("conn-state");
  var sessionState = document.getElementById("session-state");
  var live = document.getElementById("live");
  var ws = null, timer = null, backoff = 500;

  function setConn(online) {
    state.textContent = online ? "已连接" : "未连接";
    state.className = "state " + (online ? "state-online" : "state-offline");
  }

  // 只渲染文本增量与工具步骤，不做 Markdown 全文替换。
  function appendDelta(text) {
    var node = live.querySelector("[data-stream]") || (function () {
      var d = document.createElement("div");
      d.className = "bubble";
      d.setAttribute("data-stream", "");
      live.appendChild(d);
      return d;
    })();
    node.appendChild(document.createTextNode(text));
  }

  function resetStream() {
    live.innerHTML = "";
    live.classList.remove("thinking");
  }

  function handle(msg) {
    if (msg.kind !== "event" || msg.event !== "pi.event") return;
    var ev = msg.data || {};
    switch (ev.type) {
      case "agent_start":
        resetStream();
        live.classList.add("thinking");
        sessionState.textContent = "运行中";
        break;
      case "message_update":
        var e = ev.assistantMessageEvent || {};
        if (e.type === "text_delta" && e.delta) appendDelta(e.delta);
        break;
      case "agent_settled":
        live.classList.remove("thinking");
        sessionState.textContent = "空闲";
        // 等权威 message_end 后再整轮重取，避免把半轮塞进折叠组。
        var turns = document.getElementById("turns");
        if (turns && window.htmx) htmx.trigger(turns, "load");
        break;
    }
  }

  function connect() {
    var proto = location.protocol === "https:" ? "wss:" : "ws:";
    ws = new WebSocket(proto + "//" + location.host + "/api/v1/ws");
    ws.onopen = function () { setConn(true); backoff = 500; subscribe(); };
    ws.onclose = function () {
      setConn(false);
      ws = null;
      timer = setTimeout(connect, backoff);
      backoff = Math.min(backoff * 2, 10000);
    };
    ws.onerror = function () { if (ws) ws.close(); };
    ws.onmessage = function (e) {
      var msg;
      try { msg = JSON.parse(e.data); } catch (err) { return; }
      handle(msg);
    };
  }

  function subscribe() {
    var id = document.body.getAttribute("data-session-id");
    if (!id || !ws) return;
    ws.send(JSON.stringify({
      version: 1, kind: "command", requestId: "ui-sub-" + Date.now(),
      sessionId: id, method: "session.subscribe"
    }));
  }

  document.addEventListener("DOMContentLoaded", function () {
    document.body.setAttribute("data-session-id", document.body.getAttribute("data-session-id") || "");
    connect();
    // 页面切到新会话时重新订阅。
    document.body.addEventListener("htmx:afterSwap", function () {
      var sel = document.querySelector(".session-item.selected");
      if (sel) document.body.setAttribute("data-session-id", sel.getAttribute("data-turn-id") || "");
    });
  });
})();
