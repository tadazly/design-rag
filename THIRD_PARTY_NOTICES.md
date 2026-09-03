# Third-party notices

DRAG 使用并随不同分发产物打包多个开源项目。各项目仍遵循其原始许可证和版权声明。

## Codex Plugin runtime

Plugin 分发的 Go runtime 直接使用：

- [Model Context Protocol Go SDK](https://github.com/modelcontextprotocol/go-sdk) — Apache-2.0 OR MIT
- [Excelize](https://github.com/qax-os/excelize) — BSD-3-Clause
- [nkiri/xls](https://github.com/nkiri/xls) — MIT（项目内含受限的本地修改）
- [giraffesyo/pdf](https://github.com/giraffesyo/pdf) — MIT
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — BSD-3-Clause
- [Go extended libraries](https://pkg.go.dev/golang.org/x) — BSD-3-Clause

Plugin 打包器会将实际分发的直接与传递依赖许可证、确切版本和校验信息写入 Plugin 包内的 `THIRD_PARTY_NOTICES/`。

## Desktop application

桌面客户端分发时会包含 Electron、React、React DOM、Lucide React、React Markdown、Remark GFM、Cheerio、Fast Glob、Fast XML Parser、Iconv Lite、JSZip、Mammoth、PDF Parse、SheetJS、Zod 等运行时依赖。确切版本由 `package-lock.json` 记录，最终分发物应保留打包工具生成的完整许可证文件。

## External Codex integration

桌面客户端可以启动用户已安装的 [Codex CLI](https://github.com/openai/codex) `app-server`。Codex CLI 和 app-server 不包含在 DRAG Plugin 或桌面客户端的分发物中，其授权与版权声明以上游项目为准。
