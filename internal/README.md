# internal

`natsconn` and `health` are copied unchanged from
[stone-age-io/services](https://github.com/stone-age-io/services) at commit
`fca77da`. They are `internal/` there, so Go can't import them from this repo.

Keep them in step by hand: when either changes in services, copy it here
again and note the new commit above. If a third repository needs them, move
both into a public module instead.

`faketv` is a fake Android TV for this repo's tests. Nothing outside tests
imports it.
