# 第三方声明

本仓库的**界面设计与样式**移植自 [pi-web](https://github.com/agegr/pi-web)，按 MIT 许可使用。
具体范围：`pi-webui-htmx/src/styles/tokens.css` 的五套主题调色板，以及基于这些令牌的
布局尺寸与组件观感。渲染方式不同——本项目由 Go 模板在服务端输出 HTML 片段，
而不是浏览器端组件。

其余第三方依赖见 `pi-bridge-go/go.mod` 与 `pi-webui-htmx/package.json`；
它们的许可证均与 AGPL-3.0 兼容（EPL-2.0 的 `elkjs` 只出现在前端构建产物中，
不入库，见 README 的「许可证」一节）。

---

## pi-web

来源：<https://github.com/agegr/pi-web>

```
MIT License

Copyright (c) 2026 agegr

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
