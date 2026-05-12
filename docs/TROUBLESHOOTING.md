# Troubleshooting

## "DJI RC 2 not detected at all"

The consumer DJI RC 2 (controller bundled with Mini 4 Pro / Air 3) **does not support ADB**. DJI strips developer options out of the OS, so the 7-tap unlock trick used on other Android controllers doesn't apply. Use the MTP backend instead — see `INSTALLATION.md` → "MTP backend".

Quick checks if MTP-built `list-devices` still doesn't see the RC 2:

```bash
mtp-detect           # ships with libmtp-examples; confirms libmtp sees the device
gio mount --list     # if a "GProxyVolumeMonitorMTP" line shows the controller, the OS sees it
```

If `mtp-detect` works as root but not as your user, your udev rules are missing. libmtp ships them as `/usr/lib/udev/rules.d/69-libmtp.rules` — replug after the first install.

## "No devices found"

1. Plug the USB cable into the controller and the host.
2. Confirm `adb-server` is running: `adb devices` should list the controller (even if `unauthorized`).
3. If `adb` reports nothing, try a different USB cable or port. Most "doesn't work" reports trace back to a charge-only cable.
4. On Linux, check the udev rule (`docs/INSTALLATION.md` → Linux).

## "Authorization required"

The RC 2 / RC Pro displays a dialog the first time a new computer connects: **"Allow USB debugging?"** with this computer's RSA key fingerprint.

1. Tap **Always allow from this computer** then **OK** on the controller screen.
2. The daemon will pick up the new state on the next `/api/devices` call (or send a `device.authorized` WebSocket event).

If the dialog never appears: revoke USB debugging authorizations in DJI RC settings, replug, and retry.

## "DJI Fly not detected"

The daemon checks for `/sdcard/Android/data/dji.go.v5/files/waypoint`. If DJI Fly was never opened (or the slot folder doesn't exist), this is the symptom. Open DJI Fly on the controller, create a placeholder mission, then refresh.

## "Slot doesn't exist"

DJI Fly only recognises slots it created itself. Externally-created GUIDs are rejected. Workflow:

1. In DJI Fly, **create** N placeholder waypoint missions on the controller (any waypoints, any names).
2. Call `POST /api/devices/{id}/refresh` so the daemon re-scans.
3. Transfer over the placeholders.

## ClearSlot does nothing

Currently `DELETE /api/devices/{id}/slots/{guid}` returns `NOT_IMPLEMENTED`. There's no clean "empty mission" format in DJI Fly. Planned approaches:

1. **Minimal-mission overwrite** — push a 1-waypoint KMZ named "Available". Simple, leaves the slot visibly empty in DJI Fly.
2. **Rename in place** — keep existing content but tag it. Avoids data churn but is confusing.

The current build picks neither; the slot remains whatever was last transferred. File an issue with your preference.

## Preview generation fails

Preview render requires outbound HTTPS to `server.arcgisonline.com`. On air-gapped machines, set `map.provider: solid` in config to skip tile fetches — previews will be a flat backdrop with waypoint markers and text.

## Transfer cancelled mid-flight

The daemon writes to a temp filename then renames into place. If the device disconnects between push and rename, the slot's previous contents survive. The daemon does **not** currently surface a `device.disconnected` event on every drop — refresh manually if your UI looks stuck.

## Bug reports

Re-run with `--log-level debug` and attach the stderr log when filing an issue.
