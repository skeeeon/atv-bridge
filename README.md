# atv-bridge

Controls Google TV and Android TV devices over NATS, for the [Stone-Age.io](https://stone-age.io) platform.

- Send keys, type text, launch apps and turn TVs on or off with a NATS request.
- Each TV's power, app and volume is kept in a KV bucket, for the console and rule-router.
- Add and pair TVs over NATS, with no shell access to the gateway.
- One bridge per site, connected to a local leaf node or straight to the hub.

```bash
nats kv put atv_devices site-a.living-room '{"host":"192.168.1.40"}'
nats req atv.site-a.living-room.pair.start ''
nats req atv.site-a.living-room.pair.finish '{"pin":"4F2A9C"}'
nats req atv.site-a.living-room.launch '{"app":"com.google.android.youtube.tv"}'
```

**[Documentation](docs/atv-bridge.md)**: how it works, configuration, permissions, pairing, commands and state.

It follows the conventions of [stone-age-io/services](https://github.com/stone-age-io/services): one creds file, `/api/ready` and `/metrics`, a `nats micro` service, and one multi-arch image.

## Develop

```bash
go test -race ./...
docker build -t atv-bridge .
```

The tests run an embedded `nats-server` and a fake TV ([`internal/faketv`](internal/faketv)), so they need neither.

| Path | |
|---|---|
| `cmd/atv-bridge` | `main`: config, NATS, HTTP, signals; the `discover` subcommand |
| `config` | Defaults, YAML file, environment |
| `bridge` | The device list, each TV's connection and state, the micro service and pairing |
| `internal/natsconn`, `internal/health` | Copied from stone-age-io/services; see [internal/README.md](internal/README.md) |
| `third_party/atvremote` | The vendored protocol library; see [VENDORED.md](third_party/atvremote/VENDORED.md) |

## Licence

MIT. The vendored library keeps its own MIT licences.
