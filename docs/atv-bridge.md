# atv-bridge

atv-bridge lets anything on a tenant's NATS control the Google TV and Android TV devices on one site: send keys, type text, launch apps, and turn a TV on or off. It writes each TV's power, app and volume to a KV bucket, so the console can show it and rule-router can react to it.

It speaks the Android TV Remote v2 protocol, the one the Google TV app uses, through a vendored copy of [atvremote](../third_party/atvremote/VENDORED.md).

---

## 1. How It Works

```
 console, rule-router, nats req ──► atv.site-a.<device>.<verb> ──┐
                                                                 ▼
 KV atv_devices ──watch──► atv-bridge (site-a) ◄── TLS 6466/6467 ──► TVs on the LAN
                               │
                               ├──► KV atv_state ──► console, rule-router
                               └──► atv.site-a.heartbeat ──► watchdog
```

1. **TVs** live in the `atv_devices` KV bucket, one key per TV, named `{bridge}.{device}`. Each bridge watches only its own prefix.
2. **The bridge keeps each TV connected.** After the first connect, the library reconnects on its own with backoff. A TV that no longer accepts the bridge's certificate shows `paired: false` until it's paired again.
3. **State** is written to the `atv_state` KV bucket when it changes, and all of it again after a NATS reconnect.
4. **Commands and pairing** are NATS micro requests on `atv.{bridge}.{device}.{verb}`.

Run one bridge per site, on a machine on the TVs' LAN, usually the site gateway. Two bridges with the same `bridge_id` would split commands between them.

---

## 2. Run It

### Container

```bash
docker run -d --name atv-bridge --restart unless-stopped \
  -v /etc/atv-bridge/config.yaml:/etc/atv-bridge/config.yaml:ro \
  -v atv-creds:/creds:ro \
  -v atv-session:/session \
  -p 8080:8080 \
  ghcr.io/skeeeon/atv-bridge:latest
```

Images are published for `linux/amd64` and `linux/arm64`. Pin a version tag in production.

- **`/session` must be a persistent volume.** It holds the client certificate every TV is paired with. Lose it and every TV must be paired again.
- **Normal Docker networking is enough.** The bridge only makes outbound connections, to the TVs and to NATS.
- **Give each TV a DHCP reservation.** A TV whose IP changes is `unreachable` until its record is updated.
- **creds-keeper** can keep `/creds` current, exactly as for the services in stone-age-io/services. The fixed uid 10001 makes the shared volume work.

### Binary

Download an archive from the [releases page](https://github.com/skeeeon/atv-bridge/releases), or build from source:

```bash
go build -o bin/atv-bridge ./cmd/atv-bridge
bin/atv-bridge -config /etc/atv-bridge/config.yaml
```

Run it under a restart policy, such as systemd's `Restart=always`.

### Leaf node or hub

`nats.url` can point at a local leaf node (`nats://127.0.0.1:4222`) or straight at the hub cluster (`tls://hub-1:4222,tls://hub-2:4222`). The bridge doesn't know which.

- **Use a leaf node** when the site has other local NATS clients or an unreliable uplink.
- **Skip it** when the bridge is the only NATS client on the site.
- **If the leaf node runs its own JetStream domain**, set `nats.js_domain` to the hub's domain so the buckets are the hub's. With no leaf node, or a leaf node without JetStream, leave it empty.

---

## 3. Configuration

Settings come from defaults, then the config file, then environment variables, each overriding the one before. The file is read from `-config <path>` or `CONFIG_FILE`, else `/etc/atv-bridge/config.yaml` if it exists. Unknown keys are an error, and every problem is reported at once at startup. [`config.example.yaml`](../config.example.yaml) lists every key.

| YAML key | Env var | Default | |
|---|---|---|---|
| `bridge_id` | `BRIDGE_ID` | none | **Required.** `A-Z a-z 0-9 - _`, case-sensitive: `ATV-V7Q-NB9` and `atv-v7q-nb9` are two bridges. Subject token and KV key prefix. |
| `nats.url` | `NATS_URL` | `nats://localhost:4222` | Comma-separated server list. |
| `nats.creds` | `NATS_CREDS` | none | **Required.** The account user's creds file. |
| `nats.js_domain` | `NATS_JS_DOMAIN` | empty | See above. |
| `http_addr` | `HTTP_ADDR` | `:8080` | `/api/ready` and `/metrics`. |
| `session_dir` | `SESSION_DIR` | `/session` | The client certificate and key. |
| `client_name` | `CLIENT_NAME` | `Stone Age bridge` | Shown in the TV's paired-devices list. |
| `devices_bucket` | `DEVICES_BUCKET` | `atv_devices` | |
| `state_bucket` | `STATE_BUCKET` | `atv_state` | |
| `command_timeout` | `COMMAND_TIMEOUT` | `5s` | Connecting to a TV, starting a pairing, KV writes. |
| `pair_timeout` | `PAIR_TIMEOUT` | `60s` | An unfinished pairing is dropped after this. |
| `heartbeat_interval` | `HEARTBEAT_INTERVAL` | `30s` | `0` turns it off. |

The bridge creates a missing bucket with one replica and never modifies an existing one. On a clustered JetStream, create them first:

```bash
nats kv add atv_devices --replicas 3
nats kv add atv_state --replicas 3
```

### Permissions

The least the bridge's user needs, for `bridge_id: site-a` and the default bucket names. `TestLeastPrivilegePermissions` runs the bridge with exactly this list.

```yaml
publish:
  - "atv.site-a.heartbeat"
  - "$KV.atv_state.site-a.>"
  - "$JS.API.INFO"
  - "$JS.API.STREAM.INFO.KV_atv_devices"
  - "$JS.API.STREAM.INFO.KV_atv_state"
  - "$JS.API.STREAM.CREATE.KV_atv_devices"
  - "$JS.API.STREAM.CREATE.KV_atv_state"
  - "$JS.API.CONSUMER.CREATE.KV_atv_devices.>"
  - "$JS.API.CONSUMER.CREATE.KV_atv_state.>"
  - "$JS.API.CONSUMER.DELETE.KV_atv_devices.>"
  - "$JS.API.CONSUMER.DELETE.KV_atv_state.>"
  - "$JS.FC.KV_atv_devices.>"
  - "$JS.FC.KV_atv_state.>"
subscribe:
  - "_INBOX.>"
  - "atv.site-a.>"
  - "$SRV.>"
allow_responses: true
```

- **Buckets created beforehand:** you can drop the two `STREAM.CREATE` lines.
- **`js_domain` set:** every `$JS.API.` becomes `$JS.<domain>.API.`.

---

## 4. Add and Pair a TV

```bash
nats kv put atv_devices site-a.living-room '{"host":"192.168.1.40","name":"Living room"}'
```

| Field | | |
|---|---|---|
| `host` | required | IP address or hostname. Ports 6466 (commands) and 6467 (pairing). |
| `name` | optional | Display name. Defaults to the device id. |

The device id (`living-room`) uses `A-Z a-z 0-9 - _`, so it's one subject token. Ids are case-sensitive, as subjects and KV keys are. Unknown fields are errors: a bad record writes `{"error": "..."}` to the TV's state key instead of starting it.

| You | The bridge |
|---|---|
| put a record | connects to the TV |
| change a record | disconnects and connects again from the new record |
| delete a record | disconnects and deletes the TV's state key |
| put a bad record | disconnects, and writes the error to the TV's state key |

A new TV shows `paired: false`. Pair it once:

```bash
nats req atv.site-a.living-room.pair.start ''
# The TV shows a 6-character code.
nats req atv.site-a.living-room.pair.finish '{"pin":"4F2A9C"}'
```

The TV connects as soon as `pair.finish` succeeds.

- **One pairing at a time per bridge.** A `pair.start` for another TV while one is open gets `409`. A `pair.start` for the same TV starts over, for when the code was missed.
- **A wrong code** gets `400` and keeps the pairing open, so you can try again until `pair_timeout`.
- **The first pairing creates the client certificate** in `session_dir`.

To find TVs on the LAN, run `discover` on the gateway. In a container it needs host networking for mDNS:

```bash
docker run --rm --network host ghcr.io/skeeeon/atv-bridge:latest discover
```

```json
{"name":"Living room TV","host":"Chromecast.local.","ip":"192.168.1.40","port":6466}
```

---

## 5. Commands

Every command is a request. The reply is `{"ok": true}`, or a micro error with a code in the `Nats-Service-Error-Code` header.

A plain publish (no reply subject), like a console button, works too: the command runs and no reply is sent. A failed command is then only logged and counted in `atv_commands_total`.

**[Keys and Apps](keys.md)** lists every key name, the common ones grouped by what they do, long presses, and how to find an app's package id.

```bash
nats req atv.site-a.living-room.key '{"key":"HOME"}'
nats req atv.site-a.living-room.key '{"key":"DPAD_CENTER","direction":"START_LONG"}'
nats req atv.site-a.living-room.text '{"text":"stranger things"}'
nats req atv.site-a.living-room.launch '{"app":"https://www.netflix.com/title/80057281"}'
nats req atv.site-a.living-room.power '{"on":false}'
```

| Subject | Request | Does |
|---|---|---|
| `atv.{bridge}.{device}.key` | `{"key": "HOME", "direction": "SHORT"}` | Sends one key. Names are case-insensitive, and `KEYCODE_` is optional. `direction` is `SHORT` (default), `START_LONG` or `END_LONG`, in any case. See [keys](keys.md). |
| `atv.{bridge}.{device}.text` | `{"text": "..."}` | Types into the focused field. |
| `atv.{bridge}.{device}.launch` | `{"app": "..."}` | Opens an app link, or a package id such as `com.google.android.youtube.tv`. |
| `atv.{bridge}.{device}.power` | `{"on": true}` | Sends POWER only when the TV's state differs. Replies `{"ok": true, "changed": false}` when it didn't need to. |
| `atv.{bridge}.{device}.state` | empty | The TV's live state, the same shape as KV. |
| `atv.{bridge}.{device}.pair.start` | empty | Asks the TV to show a pairing code. |
| `atv.{bridge}.{device}.pair.finish` | `{"pin": "..."}` | Finishes pairing. |
| `atv.{bridge}.devices` | empty | `{"devices": [...]}` with each TV's status. |

| Code | Means |
|---|---|
| `400` | Bad request: invalid JSON, a missing field, an unknown key, or a wrong pairing code. |
| `404` | No such device on this bridge. |
| `409` | Not paired, an invalid device record, or a pairing conflict. |
| `503` | The TV isn't connected, or the send failed. |

Commands to one TV run in order. Volume can only be stepped (`VOLUME_UP`, `VOLUME_DOWN`, `VOLUME_MUTE`): the protocol reports the level but can't set it.

The bridge is a NATS micro service named `atv-bridge`, with `bridge` in its metadata, so `nats micro ls` lists it and `$SRV.PING`, `$SRV.INFO` and `$SRV.STATS` work.

---

## 6. Read State

```bash
nats kv get atv_state site-a.living-room
```

```json
{"name": "Living room", "host": "192.168.1.40", "available": true, "paired": true,
 "on": true, "app": "com.netflix.ninja", "volume": {"level": 12, "max": 100, "muted": false},
 "device": {"manufacturer": "Google", "model": "Chromecast", "sw_version": "1.36"},
 "since": "2026-11-07T19:02:11Z"}
```

| Field | |
|---|---|
| `available` | The bridge is connected to the TV and can send it commands. |
| `paired` | The TV accepts the bridge's certificate. `false` until paired, and again after a factory reset. |
| `on`, `app`, `volume`, `device` | As the TV last reported them. Kept while the TV is unavailable. |
| `error` | Set instead of everything else when the device record is invalid. |
| `since` | When anything in the record last changed. |

A dead bridge leaves its TVs' last state in KV. To know whether the bridge itself is alive, use its heartbeat: `atv.{bridge}.heartbeat` every `heartbeat_interval`, with `{"bridge": "site-a", "devices": 2, "connected": 2}`. A watchdog check on it:

```bash
nats kv put watchdog_checks atv-bridges '{"subject": "atv.*.heartbeat", "target_token": 1, "timeout": "2m"}'
```

---

## 7. Health

`GET /api/ready` returns `503` only when the bridge itself can't work: NATS is unreachable, or a bucket can't be bound. A TV that is off or unreachable is reported but keeps it `200`, because TVs are turned off all the time. An invalid device record, or TVs with no client certificate yet, is a `warn`.

`GET /metrics`:

| Metric | |
|---|---|
| `atv_devices{status}` | TVs by status: `connected`, `unreachable`, `unpaired`, `invalid`. |
| `atv_commands_total{verb, result}` | Requests by endpoint and result: `ok` or the error code. |
| `atv_state_writes_total{result}` | Writes to the state bucket: `ok` or `error`. |
