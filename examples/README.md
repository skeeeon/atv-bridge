# Examples

## A TV remote dashboard

[`bedroom-remote.dashboard.json`](bedroom-remote.dashboard.json) is a dashboard for the platform console's Visualizer that works as a remote for one TV: `bedroom` on bridge `ATV-V7Q-NB9`. Import it from the Visualizer's **Import**.

- **Status:** connected or offline, on or off, and the current app, volume and model, all read from the TV's key in `atv_state`.
- **Controls:** power on and off, a D-pad with Back, Home, Menu and Settings, playback, volume, three app shortcuts, and a box for typing text. Each group is one Button Pad widget, so it keeps its shape on a phone. Import needs a console version that has the Button Pad widget.

The buttons publish without waiting for a reply, so an error, such as the TV being offline, shows on the Connection tile rather than on the button.

The NATS identity you view it with needs publish on `atv.<bridge>.<device>.>` and read access to the `atv_state` KV bucket.

### For another TV

The bridge and device are written into every widget, because a Markdown widget doesn't fill in dashboard variables in its KV key. Generate a copy for another TV:

```bash
node remote-dashboard.js ATV-V7Q-NB9 living-room "Living room TV remote" > living-room-remote.dashboard.json
```

Edit `remote-dashboard.js` to change the layout or the app shortcuts. The package ids are common ones; check each against the `app` field in the TV's state with the app open ([keys and apps](../docs/keys.md#3-launching-apps)).
