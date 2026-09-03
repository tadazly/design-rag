# Third-party notices

The DRAG Plugin runtime is a statically linked Go executable. The following
direct runtime dependencies are redistributed in binary form under their
respective licenses:

- Model Context Protocol Go SDK (`github.com/modelcontextprotocol/go-sdk`) — Apache-2.0 OR MIT during the v1.7 license transition; the staged distribution includes the upstream license files
- Excelize (`github.com/xuri/excelize/v2`) — BSD-3-Clause
- local read-only fork of nkiri/xls (`github.com/nkiri/xls`, source in `third_party/nkiri-xls`) — MIT; patched to preserve BIFF8 formula tokens without evaluation and to bound hostile CFB/BIFF inputs
- giraffesyo/pdf (`github.com/giraffesyo/pdf`) — MIT
- modernc.org/sqlite — BSD-3-Clause
- Go extended libraries (`golang.org/x/*`) — BSD-3-Clause

The Go stage builder records exact modules, replacements, source-manifest hash,
local-patch hash, and every discovered `LICENSE*`, `NOTICE*`, and `COPYING*`
file in its generated runtime dependency manifest. Exact versions are also
recorded by `go.mod` and `go.sum`. Copyright remains with the respective
authors. This notice does not change their license terms.
