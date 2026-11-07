// Generates a platform dashboard export: a remote for one atv-bridge TV.
// Usage: node remote-dashboard.js <bridge_id> <device_id> [title] > remote.dashboard.json
const [BRIDGE = 'ATV-V7Q-NB9', DEVICE = 'bedroom', TITLE = 'Bedroom TV remote'] = process.argv.slice(2)
const base = `atv.${BRIDGE}.${DEVICE}`
const stateKey = `${BRIDGE}.${DEVICE}`
let n = 0
const id = () => `widget_remote_${++n}`
const common = (title, x, y, w, h) => ({ id: id(), title, x, y, w, h, dataSource: { type: 'subscription' }, buffer: { maxCount: 10 } })

const button = (title, label, x, y, subject, payload, color, w = 2, h = 2) => ({
  ...common(title, x, y, w, h), type: 'button',
  buttonConfig: { label, publishSubject: subject, payload: JSON.stringify(payload), actionType: 'publish', timeout: 2000, ...(color ? { color } : {}) },
})
const key = (label, k, x, y, color, w, h) => button(label, label, x, y, `${base}.key`, { key: k }, color, w, h)

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
  button('Power on', 'On', 6, 0, `${base}.power`, { on: true }, GREEN, 3),
  button('Power off', 'Off', 9, 0, `${base}.power`, { on: false }, RED, 3),

  // Left: now playing
  { ...common('Now playing', 0, 2, 6, 4), type: 'markdown',
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
  key('Back', 'BACK', 6, 2, GREY), key('Up', 'DPAD_UP', 8, 2, NAV), key('Home', 'HOME', 10, 2, GREY),
  key('Left', 'DPAD_LEFT', 6, 4, NAV), key('OK', 'DPAD_CENTER', 8, 4, OK), key('Right', 'DPAD_RIGHT', 10, 4, NAV),
  key('Menu', 'MENU', 6, 6, GREY), key('Down', 'DPAD_DOWN', 8, 6, NAV), key('Settings', 'SETTINGS', 10, 6, GREY),

  // Left: apps
  button('YouTube', 'YouTube', 0, 6, `${base}.launch`, { app: 'com.google.android.youtube.tv' }, RED),
  button('Netflix', 'Netflix', 2, 6, `${base}.launch`, { app: 'com.netflix.ninja' }, '#b91c1c'),
  button('Prime Video', 'Prime', 4, 6, `${base}.launch`, { app: 'com.amazon.amazonvideo.livingroom' }, '#0369a1'),

  // Row 8: playback and volume
  key('Rewind', 'MEDIA_REWIND', 0, 8, MEDIA), key('Play/Pause', 'MEDIA_PLAY_PAUSE', 2, 8, MEDIA), key('Fast forward', 'MEDIA_FAST_FORWARD', 4, 8, MEDIA),
  key('Volume down', 'VOLUME_DOWN', 6, 8, GREY), key('Mute', 'VOLUME_MUTE', 8, 8, GREY), key('Volume up', 'VOLUME_UP', 10, 8, GREY),

  // Row 10: typing
  { ...common('Type text', 0, 10, 12, 4), type: 'publisher',
    publisherConfig: { defaultSubject: `${base}.text`, defaultPayload: '{"text": "type here"}', history: [], timeout: 2000 } },
]

const now = Date.now()
const out = { version: '1.0', dashboards: [{
  id: `dashboard_${DEVICE}_remote`, name: TITLE,
  description: `Remote for ${DEVICE} on atv-bridge ${BRIDGE}. Needs publish on ${base}.> and read on the atv_state KV bucket.`,
  created: now, modified: now, widgets, variables: [], isLocked: true, storage: 'local', columnCount: 12,
}] }
process.stdout.write(JSON.stringify(out, null, 2) + '\n')
