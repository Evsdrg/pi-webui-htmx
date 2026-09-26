// 终端：xterm.js。数据面走 WS，不走 htmx——PTY 是字节流，没有 HTML 表示。
(function () {
  "use strict";
  function loadScript(src) {
    return new Promise(function (resolve, reject) {
      var s = document.createElement("script");
      s.src = src; s.onload = resolve;
      s.onerror = function () { reject(new Error("加载失败: " + src)); };
      document.head.appendChild(s);
    });
  }
  function loadCss(href) {
    var l = document.createElement("link");
    l.rel = "stylesheet"; l.href = href;
    document.head.appendChild(l);
  }
  var terms = {};
  var ready = null;
  function ensure() {
    if (ready) return ready;
    loadCss("/assets/vendor/xterm.css");
    ready = Promise.all([
      loadScript("/assets/vendor/xterm.js"),
      loadScript("/assets/vendor/fitaddon.min.js"),
    ]);
    return ready;
  }
  // open 在容器内创建终端并返回句柄；input 由调用方经 WS 送出。
  window.openTerminal = function (terminalId, container, onInput, onResize) {
    ensure().then(function () {
      if (!window.Terminal || terms[terminalId]) return;
      var term = new Terminal({ cursorBlink: true, fontSize: 13, convertEol: true });
      var fit = new FitAddon.FitAddon();
      term.loadAddon(fit);
      term.open(container);
      fit.fit();
      term.onData(function (d) { if (onInput) onInput(d); });
      term.onResize(function (size) { if (onResize) onResize(size.cols, size.rows); });
      terms[terminalId] = { term: term, fit: fit };
    }).catch(function (err) { console.warn("[terminal]", err.message); });
    return terminalId;
  };
  window.writeTerminal = function (terminalId, data) {
    var t = terms[terminalId];
    if (t) t.term.write(data);
  };
  window.fitTerminal = function (terminalId) {
    var t = terms[terminalId];
    if (t) { try { t.fit.fit(); } catch (e) {} }
  };
  window.closeTerminal = function (terminalId) {
    var t = terms[terminalId];
    if (t) { t.term.dispose(); delete terms[terminalId]; }
  };
})();
