// 图表：mermaid 约 2.5 MB，必须动态 import 且只在出现节点时加载。
// 首屏绝不包含它。
(function () {
  "use strict";
  var loaded = false, loading = null;
  function ensure() {
    if (loading) return loading;
    loading = new Promise(function (resolve, reject) {
      var s = document.createElement("script");
      s.src = "/assets/vendor/mermaid.min.js";
      s.onload = function () {
        if (!window.mermaid) return reject(new Error("mermaid 未就绪"));
        window.mermaid.initialize({ startOnLoad: false, theme: "dark" });
        loaded = true;
        resolve();
      };
      s.onerror = function () { reject(new Error("加载失败: mermaid")); };
      document.head.appendChild(s);
    });
    return loading;
  }
  window.renderMermaid = function (root) {
    var nodes = (root || document).querySelectorAll(".mermaid:not([data-rendered])");
    if (!nodes.length) return;
    ensure().then(function () {
      nodes.forEach(function (el, i) {
        var src = el.textContent || "";
        var id = "mmd-" + Date.now() + "-" + i;
        window.mermaid.render(id, src).then(function (r) {
          el.innerHTML = r.svg;
          el.setAttribute("data-rendered", "1");
        }).catch(function (err) {
          console.warn("[mermaid]", err.message);
          el.setAttribute("data-rendered", "1");
        });
      });
    }).catch(function (err) { console.warn("[mermaid]", err.message); });
  };
})();
