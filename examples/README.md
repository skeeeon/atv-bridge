# Examples

## A TV remote dashboard

[`tv-remote.dashboard.json`](tv-remote.dashboard.json) is a dashboard for the platform console's Visualizer that works as a remote for atv-bridge TVs. Import it from the Visualizer's **Import**.

- **Status:** connected or offline, on or off, and the current app, volume and model, all read from the TV's key in `atv_state`.
- **Controls:** power on and off, a D-pad with Back, Home, Menu and Settings, playback, volume, four app shortcuts, and a box for typing text. Each group is one Button Pad widget, so it keeps its shape on a phone. Import needs a console version that has the Button Pad widget.

The buttons publish without waiting for a reply, so an error, such as the TV being offline, shows on the Connection tile rather than on the button.

### Choosing the TV

A `tv` variable selects the TV. Each option is `<bridge>.<device>`, such as `ATV-V7Q-NB9.bedroom`: that string is the TV's key in `atv_state` and the middle of its subjects (`atv.<bridge>.<device>.key`), so every widget uses it as `{{tv}}`. Switch TVs from the variable bar (`V`, or **Variables** in the ⋮ menu on a phone). The choice resets to the default when the page reloads.

To add a TV, unlock the dashboard and add an option to the variable, or generate the dashboard with every TV listed. The first one is the default:

```bash
node remote-dashboard.js ATV-V7Q-NB9.bedroom ATV-V7Q-NB9.living-room > tv-remote.dashboard.json
```

The NATS identity you view it with needs publish on `atv.<bridge>.<device>.>` for each TV, and read access to the `atv_state` KV bucket.

Edit `remote-dashboard.js` to change the layout or the app shortcuts. The app shortcuts send https links rather than package ids, because some TVs ignore a launch by package id ([keys and apps](../docs/keys.md#3-launching-apps)).
