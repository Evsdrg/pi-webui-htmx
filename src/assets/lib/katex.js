// 公式渲染：KaTeX + auto-render，懒加载。
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
  var ready = null;
  function ensure() {
    if (ready) return ready;
    ready = Promise.all([
      loadScript("/assets/vendor/katex.min.js"),
      loadScript("/assets/vendor/katex.auto-render.js"),
    ]);
    return ready;
  }
  window.renderMath = function (root) {
    ensure().then(function () {
      if (!window.katex || !window.renderMathInElement) return;
      (root || document).querySelectorAll(".math:not([data-rendered])").forEach(function (el) {
        try {
          renderMathInElement(el, { delimiters: [
            { left: "$$", right: "$$", display: true },
            { left: "$", right: "$", display: false },
          ]});
          el.setAttribute("data-rendered", "1");
        } catch (err) { console.warn("[katex]", err.message); }
      });
    }).catch(function (err) { console.warn("[katex]", err.message); });
  };
})();
