import { defineConfig } from "vite";
import tailwindcss from "@tailwindcss/vite";
import { resolve } from "node:path";

// Vite 只负责 JS/CSS。Go 模板在 src/templates/，由桥渲染，
// Vite 不碰它们——但 Tailwind 需要扫描它们才能产出用到的类。
export default defineConfig({
  plugins: [tailwindcss()],
  resolve: {
    alias: {
      "@": resolve(import.meta.dirname, "src"),
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // 桥需要 manifest 把逻辑名（app.css）解析成带哈希的真实文件名。
    manifest: true,
    // mermaid 一个库就 6 MB，默认阈值会一直告警。
    chunkSizeWarningLimit: 1600,
    // 产物要轻：关掉 sourcemap，开 brotli 友好的长缓存命名。
    sourcemap: false,
    target: "es2022",
    cssCodeSplit: true,
    modulePreload: { polyfill: false },
    rollupOptions: {
      input: {
        app: resolve(import.meta.dirname, "src/entry/app.ts"),
      },
      output: {
        // 内容哈希 + 长期不可变缓存。
        entryFileNames: "assets/[name]-[hash].js",
        chunkFileNames: "assets/[name]-[hash].js",
        assetFileNames: "assets/[name]-[hash][extname]",
        // 把 htmx 单独切出来：它占首屏 gzip 约 18 KiB 且几乎不变，
        // 独立成块后我们改自己的代码不会顶掉它的缓存；预算也才能把
        // 「供应商代码」与「自己写的代码」分开度量（见 check-contract.mjs）。
        // 注意：rolldown 的 ManualChunks 只接受函数形式，对象写法会直接被类型拒绝。
        manualChunks(id) {
          // 只把 htmx 单独切出来；其余供应商库保持默认分块，避免打散树共享。
          if (id.includes("node_modules/htmx.org")) return "htmx";
          return null;
        },
      },
    },
  },
  // 重库不进首屏：显式声明为惰性 chunk 的边界。
  optimizeDeps: { include: ["htmx.org", "marked", "dompurify"] },
});
