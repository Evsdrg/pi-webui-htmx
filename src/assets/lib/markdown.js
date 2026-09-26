// Markdown 渲染：marked + DOMPurify。
// DOMPurify 不可省略——模型输出与文件内容都不可信，marked 不过滤 HTML。
(function () {
  "use strict";
  var loading = null;

  function loadScript(src) {
    return new Promise(function (resolve, reject) {
      var s = document.createElement("script");
      s.src = src;
      s.onload = resolve;
      s.onerror = function () { reject(new Error("加载失败: " + src)); };
      document.head.appendChild(s);
    });
  }

  function ensure() {
    if (loading) return loading;
    loading = Promise.all([
      loadScript("/assets/vendor/marked.min.js"),
      loadScript("/assets/vendor/dompurify.min.js"),
    ]).then(function () {
      if (!window.marked || !window.DOMPurify) throw new Error("marked/DOMPurify 未就绪");
      marked.setOptions({ gfm: true, breaks: false });
    });
    return loading;
  }

  // 只渲染未处理过的节点；重复渲染会丢光标并造成闪烁。
  window.renderMarkdown = function (root) {
    ensure().then(function () {
      (root || document).querySelectorAll(".markdown:not([data-rendered])").forEach(function (el) {
        var html = marked.parse(el.textContent || "");
        el.innerHTML = DOMPurify.sanitize(html, { USE_PROFILES: { html: true } });
        el.setAttribute("data-rendered", "1");
      });
    }).catch(function (err) {
      console.warn("[markdown]", err.message);
    });
  };
})();
