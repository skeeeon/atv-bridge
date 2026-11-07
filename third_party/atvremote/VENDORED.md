# Vendored: atvremote

| | |
| --- | --- |
| Source | https://github.com/jkiddo/atvremote |
| Commit | `ab5a73b3c68d815df5b3ac8a96aa47fda66745da` (2026-08-06) |
| Copied | `pkg/v2/remote` → `remote/`, `pkg/v2/proto` → `proto/` |
| Licences | MIT: `LICENCE` (drosoCode), `proto/LICENCE` (louis49) |

That commit is a Go port of [tronikos/androidtvremote2](https://github.com/tronikos/androidtvremote2),
the Python library behind Home Assistant's Android TV Remote integration.
When the protocol changes, diff against that library.

v1, the CLI and `pkg/common` are not copied: the bridge speaks v2 only.

## Local patches

1. The `proto` import path in `remote/*.go` is rewritten to
   `github.com/stone-age-io/atv-bridge/third_party/atvremote/proto`.

Nothing else is changed. The generated `.pb.go` files and the `.proto`
sources are byte-for-byte upstream, including the upstream `go_package`
option, so the build never needs `protoc`. If they must be regenerated, pass
`--go_opt=M<file>=<our import path>` for each `.proto` file.

## Updating

Copy the two directories again at the new commit, re-apply the patches
above, update the commit in this file, and run `go test ./...`.
