// 代码高亮：按元素处理，不做全局扫描。
(function () {
  "use strict";
  function loadScript(src) {
    return new Promise(function (resolve, reject) {
      var s = document.createElement("script");
      s.src = src;
      s.onload = resolve;
      s.onerror = function () { reject(new Error("加载失败: " + src)); };
      document.head.appendChild(s);
    });
  }
  var ready = null;
  function ensure() {
    if (ready) return ready;
    ready = Promise.all([
      loadScript("/assets/vendor/highlight.min.js"),
      loadScript("/assets/vendor/highlight.css.shim.js"),
    ]).catch(function () {
      // CSS shim 缺失不影响高亮本身。
      return loadScript("/assets/vendor/highlight.min.js");
    });
    return ready;
  }
  window.renderHighlight = function (root) {
    ensure().then(function () {
      if (!window.hljs) return;
      (root || document).querySelectorAll("pre code:not([data-highlighted])").forEach(function (el) {
        try {
          hljs.highlightElement(el);
          el.setAttribute("data-highlighted", "1");
        } catch (err) {
          console.warn("[highlight]", err.message);
        }
      });
    }).catch(function (err) { console.warn("[highlight]", err.message); });
  };
})();
