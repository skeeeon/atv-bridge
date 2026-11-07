# Keys and Apps

What you can send with `atv.{bridge}.{device}.key` and `atv.{bridge}.{device}.launch`. The [main documentation](atv-bridge.md) covers the rest.

---

## 1. Sending a Key

```bash
nats req atv.site-a.living-room.key '{"key":"HOME"}'
nats req atv.site-a.living-room.key '{"key":"KEYCODE_HOME"}'
```

- **Names are the Android key codes** in the protocol, with the `KEYCODE_` prefix optional.
- **Names are case-insensitive**, and so is `direction`. `HOME`, `home` and `keycode_home` are the same key. This page writes them in uppercase, as the protocol does.
- **A key the TV doesn't handle is still sent.** The reply is `{"ok": true}` because the bridge sent it, not because the TV acted on it. Which keys do something depends on the device and the app in the foreground.

### Long presses

`direction` is `SHORT` by default. For a long press, send `START_LONG`, wait, then `END_LONG` with the same key:

```bash
nats req atv.site-a.living-room.key '{"key":"DPAD_CENTER","direction":"START_LONG"}'
sleep 1
nats req atv.site-a.living-room.key '{"key":"DPAD_CENTER","direction":"END_LONG"}'
```

A `START_LONG` with no `END_LONG` leaves the key held down on the TV.

### Text

To type into a search box, use the `text` endpoint rather than one key per letter:

```bash
nats req atv.site-a.living-room.text '{"text":"stranger things"}'
```

---

## 2. Common Keys

These are the keys a TV remote has. The full list is in section 4.

| Group | Keys |
|---|---|
| Navigation | `DPAD_UP` `DPAD_DOWN` `DPAD_LEFT` `DPAD_RIGHT` `DPAD_CENTER` `BACK` `HOME` |
| System | `MENU` `SETTINGS` `SEARCH` `ASSIST` `APP_SWITCH` `ALL_APPS` `GUIDE` `INFO` `NOTIFICATION` |
| Playback | `MEDIA_PLAY_PAUSE` `MEDIA_PLAY` `MEDIA_PAUSE` `MEDIA_STOP` `MEDIA_REWIND` `MEDIA_FAST_FORWARD` `MEDIA_NEXT` `MEDIA_PREVIOUS` `MEDIA_SKIP_FORWARD` `MEDIA_SKIP_BACKWARD` |
| Playback options | `CAPTIONS` `MEDIA_AUDIO_TRACK` |
| Volume | `VOLUME_UP` `VOLUME_DOWN` `VOLUME_MUTE` |
| Power | `POWER` `SLEEP` `WAKEUP` `TV_POWER` |
| Channels | `CHANNEL_UP` `CHANNEL_DOWN` `LAST_CHANNEL` `0` … `9` `ENTER` |
| Inputs | `TV_INPUT` `TV_INPUT_HDMI_1` `TV_INPUT_HDMI_2` `TV_INPUT_HDMI_3` `TV_INPUT_HDMI_4` |
| Colour buttons | `PROG_RED` `PROG_GREEN` `PROG_YELLOW` `PROG_BLUE` |
| Editing | `DEL` (backspace) `FORWARD_DEL` `MOVE_HOME` `MOVE_END` |

### Easy to confuse

| Key | Does | Not to be confused with |
|---|---|---|
| `VOLUME_MUTE` | Mutes the speaker. | `MUTE`, which mutes the microphone. |
| `POWER` | Toggles power. | `SLEEP` and `WAKEUP`, which only turn off or only turn on, and do nothing if the TV is already in that state. |
| `TV_POWER` | Powers the TV panel, on devices that control one, such as a Google TV streamer driving a TV over HDMI-CEC. | `POWER`, which powers the device itself. |
| `DEL` | Deletes the character before the cursor (backspace). | `FORWARD_DEL`, which deletes the one after it. |
| `DPAD_CENTER` | Selects the focused item. | `ENTER`, which some apps treat differently. |

The `power` endpoint sends `POWER` only when the TV's state differs from the one asked for. `SLEEP` and `WAKEUP` are an alternative when you'd rather not depend on the bridge's view of the power state; not every device handles them.

Volume can only be stepped. To go up 5 steps, send `VOLUME_UP` 5 times. The current level is in the TV's state (`volume.level` of `volume.max`).

---

## 3. Launching Apps

```bash
nats req atv.site-a.living-room.launch '{"app":"com.google.android.youtube.tv"}'
nats req atv.site-a.living-room.launch '{"app":"https://www.netflix.com/title/80057281"}'
```

`app` is one of:

- **A package id**, such as `com.google.android.youtube.tv`. The bridge sends it as `market://launch?id=<package>`, which opens the app if it's installed.
- **A link**: anything with a scheme, such as an `https://` deep link. It's sent as is, and the TV opens whichever app claims it. That's how you open a specific title or channel.

**To find an app's package id**, open the app on the TV and read the state:

```bash
nats req atv.site-a.living-room.state '' | jq -r .app
```

Common ones, to check against `.app` on your own TV, since apps differ by device and region:

| App | Package id |
|---|---|
| YouTube | `com.google.android.youtube.tv` |
| YouTube Music | `com.google.android.youtube.tvmusic` |
| Netflix | `com.netflix.ninja` |
| Prime Video | `com.amazon.amazonvideo.livingroom` |
| Disney+ | `com.disney.disneyplus` |
| Spotify | `com.spotify.tv.android` |
| Plex | `com.plexapp.android` |

---

## 4. Every Key

Every name the protocol defines, without the `KEYCODE_` prefix, sorted. Many are for keyboards, game controllers, phones or other hardware, and a TV ignores them.

**0–9** `0` · `1` · `11` · `12` · `2` · `3` · `3D_MODE` · `4` · `5` · `6` · `7` · `8` · `9`

**A** `A` · `ALL_APPS` · `ALT_LEFT` · `ALT_RIGHT` · `APOSTROPHE` · `APP_SWITCH` · `ASSIST` · `AT` · `AVR_INPUT` · `AVR_POWER`

**B** `B` · `BACK` · `BACKSLASH` · `BOOKMARK` · `BREAK` · `BRIGHTNESS_DOWN` · `BRIGHTNESS_UP` · `BUTTON_1` · `BUTTON_10` · `BUTTON_11` · `BUTTON_12` · `BUTTON_13` · `BUTTON_14` · `BUTTON_15` · `BUTTON_16` · `BUTTON_2` · `BUTTON_3` · `BUTTON_4` · `BUTTON_5` · `BUTTON_6` · `BUTTON_7` · `BUTTON_8` · `BUTTON_9` · `BUTTON_A` · `BUTTON_B` · `BUTTON_C` · `BUTTON_L1` · `BUTTON_L2` · `BUTTON_MODE` · `BUTTON_R1` · `BUTTON_R2` · `BUTTON_SELECT` · `BUTTON_START` · `BUTTON_THUMBL` · `BUTTON_THUMBR` · `BUTTON_X` · `BUTTON_Y` · `BUTTON_Z`

**C** `C` · `CALCULATOR` · `CALENDAR` · `CALL` · `CAMERA` · `CAPS_LOCK` · `CAPTIONS` · `CHANNEL_DOWN` · `CHANNEL_UP` · `CLEAR` · `CLOSE` · `COMMA` · `CONTACTS` · `COPY` · `CTRL_LEFT` · `CTRL_RIGHT` · `CUT`

**D** `D` · `DEL` · `DEMO_APP_1` · `DEMO_APP_2` · `DEMO_APP_3` · `DEMO_APP_4` · `DICTATE` · `DO_NOT_DISTURB` · `DPAD_CENTER` · `DPAD_DOWN` · `DPAD_DOWN_LEFT` · `DPAD_DOWN_RIGHT` · `DPAD_LEFT` · `DPAD_RIGHT` · `DPAD_UP` · `DPAD_UP_LEFT` · `DPAD_UP_RIGHT` · `DVR`

**E** `E` · `EISU` · `EMOJI_PICKER` · `ENDCALL` · `ENTER` · `ENVELOPE` · `EQUALS` · `ESCAPE` · `EXPLORER`

**F** `F` · `F1` · `F10` · `F11` · `F12` · `F13` · `F14` · `F15` · `F16` · `F17` · `F18` · `F19` · `F2` · `F20` · `F21` · `F22` · `F23` · `F24` · `F3` · `F4` · `F5` · `F6` · `F7` · `F8` · `F9` · `FEATURED_APP_1` · `FEATURED_APP_2` · `FEATURED_APP_3` · `FEATURED_APP_4` · `FOCUS` · `FORWARD` · `FORWARD_DEL` · `FULLSCREEN` · `FUNCTION`

**G** `G` · `GRAVE` · `GUIDE`

**H** `H` · `HEADSETHOOK` · `HELP` · `HENKAN` · `HOME`

**I** `I` · `INFO` · `INSERT`

**J** `J`

**K** `K` · `KANA` · `KATAKANA_HIRAGANA` · `KEYBOARD_BACKLIGHT_DOWN` · `KEYBOARD_BACKLIGHT_TOGGLE` · `KEYBOARD_BACKLIGHT_UP`

**L** `L` · `LANGUAGE_SWITCH` · `LAST_CHANNEL` · `LEFT_BRACKET` · `LOCK`

**M** `M` · `MACRO_1` · `MACRO_2` · `MACRO_3` · `MACRO_4` · `MANNER_MODE` · `MEDIA_AUDIO_TRACK` · `MEDIA_CLOSE` · `MEDIA_EJECT` · `MEDIA_FAST_FORWARD` · `MEDIA_NEXT` · `MEDIA_PAUSE` · `MEDIA_PLAY` · `MEDIA_PLAY_PAUSE` · `MEDIA_PREVIOUS` · `MEDIA_RECORD` · `MEDIA_REWIND` · `MEDIA_SKIP_BACKWARD` · `MEDIA_SKIP_FORWARD` · `MEDIA_STEP_BACKWARD` · `MEDIA_STEP_FORWARD` · `MEDIA_STOP` · `MEDIA_TOP_MENU` · `MENU` · `META_LEFT` · `META_RIGHT` · `MINUS` · `MOVE_END` · `MOVE_HOME` · `MUHENKAN` · `MUSIC` · `MUTE`

**N** `N` · `NAVIGATE_IN` · `NAVIGATE_NEXT` · `NAVIGATE_OUT` · `NAVIGATE_PREVIOUS` · `NEW` · `NOTIFICATION` · `NUM` · `NUMPAD_0` · `NUMPAD_1` · `NUMPAD_2` · `NUMPAD_3` · `NUMPAD_4` · `NUMPAD_5` · `NUMPAD_6` · `NUMPAD_7` · `NUMPAD_8` · `NUMPAD_9` · `NUMPAD_ADD` · `NUMPAD_COMMA` · `NUMPAD_DIVIDE` · `NUMPAD_DOT` · `NUMPAD_ENTER` · `NUMPAD_EQUALS` · `NUMPAD_LEFT_PAREN` · `NUMPAD_MULTIPLY` · `NUMPAD_RIGHT_PAREN` · `NUMPAD_SUBTRACT` · `NUM_LOCK`

**O** `O`

**P** `P` · `PAGE_DOWN` · `PAGE_UP` · `PAIRING` · `PASTE` · `PERIOD` · `PICTSYMBOLS` · `PLUS` · `POUND` · `POWER` · `PRINT` · `PROFILE_SWITCH` · `PROG_BLUE` · `PROG_GREEN` · `PROG_RED` · `PROG_YELLOW`

**Q** `Q`

**R** `R` · `RECENT_APPS` · `REFRESH` · `RIGHT_BRACKET` · `RO`

**S** `S` · `SCREENSHOT` · `SCROLL_LOCK` · `SEARCH` · `SEMICOLON` · `SETTINGS` · `SHIFT_LEFT` · `SHIFT_RIGHT` · `SLASH` · `SLEEP` · `SOFT_LEFT` · `SOFT_RIGHT` · `SOFT_SLEEP` · `SPACE` · `STAR` · `STB_INPUT` · `STB_POWER` · `STEM_1` · `STEM_2` · `STEM_3` · `STEM_PRIMARY` · `STYLUS_BUTTON_PRIMARY` · `STYLUS_BUTTON_SECONDARY` · `STYLUS_BUTTON_TAIL` · `STYLUS_BUTTON_TERTIARY` · `SWITCH_CHARSET` · `SYM` · `SYSRQ` · `SYSTEM_NAVIGATION_DOWN` · `SYSTEM_NAVIGATION_LEFT` · `SYSTEM_NAVIGATION_RIGHT` · `SYSTEM_NAVIGATION_UP`

**T** `T` · `TAB` · `THUMBS_DOWN` · `THUMBS_UP` · `TV` · `TV_ANTENNA_CABLE` · `TV_AUDIO_DESCRIPTION` · `TV_AUDIO_DESCRIPTION_MIX_DOWN` · `TV_AUDIO_DESCRIPTION_MIX_UP` · `TV_CONTENTS_MENU` · `TV_DATA_SERVICE` · `TV_INPUT` · `TV_INPUT_COMPONENT_1` · `TV_INPUT_COMPONENT_2` · `TV_INPUT_COMPOSITE_1` · `TV_INPUT_COMPOSITE_2` · `TV_INPUT_HDMI_1` · `TV_INPUT_HDMI_2` · `TV_INPUT_HDMI_3` · `TV_INPUT_HDMI_4` · `TV_INPUT_VGA_1` · `TV_MEDIA_CONTEXT_MENU` · `TV_NETWORK` · `TV_NUMBER_ENTRY` · `TV_POWER` · `TV_RADIO_SERVICE` · `TV_SATELLITE` · `TV_SATELLITE_BS` · `TV_SATELLITE_CS` · `TV_SATELLITE_SERVICE` · `TV_TELETEXT` · `TV_TERRESTRIAL_ANALOG` · `TV_TERRESTRIAL_DIGITAL` · `TV_TIMER_PROGRAMMING` · `TV_ZOOM_MODE`

**U** `U`

**V** `V` · `VIDEO_APP_1` · `VIDEO_APP_2` · `VIDEO_APP_3` · `VIDEO_APP_4` · `VIDEO_APP_5` · `VIDEO_APP_6` · `VIDEO_APP_7` · `VIDEO_APP_8` · `VOICE_ASSIST` · `VOLUME_DOWN` · `VOLUME_MUTE` · `VOLUME_UP`

**W** `W` · `WAKEUP` · `WINDOW`

**X** `X`

**Y** `Y` · `YEN`

**Z** `Z` · `ZENKAKU_HANKAKU` · `ZOOM_IN` · `ZOOM_OUT`
