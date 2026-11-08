// Generates a platform dashboard export: a remote for atv-bridge TVs. A `tv`
// select variable picks the TV, so one dashboard covers them all.
// Usage: node remote-dashboard.js <bridge>.<device> [<bridge>.<device> ...] > tv-remote.dashboard.json
// The first TV is the default.
const tvs = process.argv.slice(2)
if (tvs.length === 0) tvs.push('ATV-V7Q-NB9.bedroom')
for (const tv of tvs) {
  // Bridge and device ids are [A-Za-z0-9_-]+ (config/config.go), so one dot.
  if (!/^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(tv)) {
    console.error(`not a <bridge>.<device> id: ${tv}`)
    process.exit(1)
  }
}

// {{tv}} is the TV's key in atv_state and the middle of its subjects.
const base = 'atv.{{tv}}'
const stateKey = '{{tv}}'
let n = 0
const id = () => `widget_remote_${++n}`
const common = (title, x, y, w, h) => ({ id: id(), title, x, y, w, h, dataSource: { type: 'subscription' }, buffer: { maxCount: 10 } })

// A button pad keeps its shape on a phone, where separate buttons are paired
// two to a row. `cells` is rows of [label, subject suffix, payload, colour];
// null is a gap.
const pad = (title, x, y, w, h, cells) => ({
  ...common(title, x, y, w, h), type: 'buttonpad',
  buttonpadConfig: {
    rows: cells.length,
    cols: Math.max(...cells.map(r => r.length)),
    cells: cells.flatMap((r, row) => r.flatMap((c, col) => c
      ? [{ row, col, label: c[0], subject: `${base}.${c[1]}`, payload: JSON.stringify(c[2]), color: c[3] }]
      : [])),
  },
})
const key = (label, k, color) => [label, 'key', { key: k }, color]
const app = (label, pkg, color) => [label, 'launch', { app: pkg }, color]

const status = (title, x, y, path, mappings) => ({
  ...common(title, x, y, 3, 2), type: 'status', jsonPath: path,
  dataSource: { type: 'kv', kvBucket: 'atv_state', kvKey: stateKey },
  statusConfig: {
    mappings: mappings.map((m, i) => ({ id: String(i + 1), blink: false, ...m })),
    defaultColor: 'var(--muted)', defaultLabel: 'Unknown', showStale: false,
  },
})

const GREEN = '#16a34a', RED = '#dc2626', NAV = '#2563eb', OK = '#7c3aed', MEDIA = '#0f766e', GREY = '#475569'

const widgets = [
  // Row 0: state and power
  status('Connection', 0, 0, '$.available', [
    { value: 'true', color: 'var(--color-success)', label: 'Connected' },
    { value: 'false', color: 'var(--color-error)', label: 'Offline' },
  ]),
  status('Power', 3, 0, '$.on', [
    { value: 'true', color: 'var(--color-success)', label: 'On' },
    { value: 'false', color: 'var(--muted)', label: 'Off' },
  ]),
  pad('Power buttons', 6, 0, 6, 2, [[
    ['On', 'power', { on: true }, GREEN], ['Off', 'power', { on: false }, RED],
  ]]),

  // Left: now playing
  { ...common('Now playing', 0, 2, 6, 6), type: 'markdown',
    dataSource: { type: 'kv', kvBucket: 'atv_state', kvKey: stateKey },
    markdownConfig: { content: [
      '**App:** `{{app}}`',
      '',
      '**Volume:** {{volume.level}} of {{volume.max}} (muted: {{volume.muted}})',
      '',
      '**Paired:** {{paired}} · **Device:** {{device.manufacturer}} {{device.model}}',
      '',
      '**Last change:** {{since}}',
    ].join('\n') } },

  // Right: D-pad
  pad('Navigation', 6, 2, 6, 6, [
    [key('Back', 'BACK', GREY), key('↑', 'DPAD_UP', NAV), key('Home', 'HOME', GREY)],
    [key('←', 'DPAD_LEFT', NAV), key('OK', 'DPAD_CENTER', OK), key('→', 'DPAD_RIGHT', NAV)],
    [key('Menu', 'MENU', GREY), key('↓', 'DPAD_DOWN', NAV), key('Settings', 'SETTINGS', GREY)],
  ]),

  // Row 8: playback and volume
  pad('Playback', 0, 8, 6, 2, [[
    key('Rewind', 'MEDIA_REWIND', MEDIA), key('Play/Pause', 'MEDIA_PLAY_PAUSE', MEDIA), key('Forward', 'MEDIA_FAST_FORWARD', MEDIA),
  ]]),
  pad('Volume', 6, 8, 6, 2, [[
    key('Vol −', 'VOLUME_DOWN', GREY), key('Mute', 'VOLUME_MUTE', GREY), key('Vol +', 'VOLUME_UP', GREY),
  ]]),

  // Row 10: apps
  pad('Apps', 0, 10, 12, 2, [[
    app('YouTube', 'com.google.android.youtube.tv', RED),
    app('Netflix', 'com.netflix.ninja', '#b91c1c'),
    app('Prime Video', 'com.amazon.amazonvideo.livingroom', '#0369a1'),
  ]]),

  // Row 12: typing
  { ...common('Type text', 0, 12, 12, 4), type: 'publisher',
    publisherConfig: { defaultSubject: `${base}.text`, defaultPayload: '{"text": "type here"}', history: [], timeout: 2000 } },
]

const now = Date.now()
const out = { version: '1.0', dashboards: [{
  id: 'dashboard_tv_remote', name: 'TV remote',
  description: 'Remote for atv-bridge TVs. Pick the TV in the variable bar; each option is <bridge>.<device>, the TV\'s key in atv_state. Needs publish on atv.<bridge>.<device>.> for each TV and read on the atv_state KV bucket.',
  created: now, modified: now, widgets,
  variables: [{ id: 'var_tv', name: 'tv', label: 'TV', type: 'select', options: tvs, defaultValue: tvs[0] }],
  isLocked: true, storage: 'local', columnCount: 12,
}] }
process.stdout.write(JSON.stringify(out, null, 2) + '\n')
