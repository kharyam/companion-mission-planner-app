# kam-transfer

Cross-platform companion daemon for [KAM Mission Planner](https://github.com/kamdynamics). Exposes a local HTTP API that the browser-based KAM UI calls to push KMZ waypoint missions onto USB-connected DJI controllers (RC 2, RC Pro) and Android phones running DJI Fly.

## Why it exists

KAM Mission Planner runs in a browser, often hosted remotely (e.g. TrueNAS over Tailscale). Browsers can't reach the USB bus. This daemon runs locally on the machine where the controller plugs in, handles all the ADB/MTP plumbing, and surfaces a clean HTTP API the remote UI can call.

## What it does

- **Device discovery.** ADB (for controllers / phones in developer mode) and MTP (for the consumer DJI RC 2, which ships without ADB) — both fed into a unified `/api/devices` view.
- **Slot listing.** Walks DJI Fly's `Android/data/dji.go.v5/files/waypoint` tree on the device and surfaces each slot with size, mtime, preview-availability, and an optional user-assigned name.
- **KMZ transfer.** Streams a mission KMZ into the chosen slot. Optionally renders a satellite-tile preview JPEG (ESRI World Imagery) plus one tile per waypoint, so DJI Fly's mission list shows real thumbnails.
- **Hotplug events.** A WebSocket at `/api/events` emits `device.connected` / `device.disconnected` / `device.refreshed` so the UI updates without polling. ADB events come straight from `adb-server`; MTP devices are detected via a libusb-level poll since libmtp has no hotplug API.
- **Embedded admin UI.** A built-in zero-dependency web UI at `/ui` lets you list devices, browse slots, push KMZs, regenerate previews, and download mission KMZs without launching Mission Planner.
- **Front-panel status screen (optional).** On a Raspberry Pi fitted with a Pimoroni Display HAT Mini + PiSugar 3, the daemon drives a 320×240 LCD showing the server URL, battery, network, and controller status, with four buttons for paging / rescan / QR code / safe shutdown. Auto-detected; a silent no-op on any other host.

## Quick start

```bash
make build              # CGO_ENABLED=0 — ADB only, cross-compiles cleanly
make build-mtp          # CGO_ENABLED=1 — adds the libmtp backend for DJI RC 2 support
                        # needs gcc + pkg-config + libmtp-devel; see docs/INSTALLATION.md

./dist/kam-transfer serve

# Health check
curl http://127.0.0.1:8765/api/health

# CLI alternatives to the API
./dist/kam-transfer list-devices
./dist/kam-transfer list-slots --device <serial>
./dist/kam-transfer transfer ./mission.kmz --device <serial> --slot <guid>
```

The admin UI is at `http://127.0.0.1:8765/ui`. If you've set `auth.token` in config, the UI prompts for it on first load and stores it in `sessionStorage`; or pass it as `?token=…` once and it'll capture and strip the URL.

On first startup the daemon writes a comment-rich starter `config.yaml` to its platform default (`~/.config/kam-transfer/config.yaml` on Linux, `~/Library/Application Support/kam-transfer/config.yaml` on macOS, `%APPDATA%\kam-transfer\config.yaml` on Windows) and prints the path to stderr. Edit it to set `auth.token`, change ports, etc.; subsequent runs leave the file alone.

## Cross-compile

```bash
make build-all          # linux/amd64, darwin/amd64+arm64, windows/amd64 — CGO off
```

Binaries land in `dist/`. MTP support requires cgo, so the cross-compiled binaries are ADB-only; build natively on each host with `make build-mtp` if you need RC 2 support there.

## Authentication

By default `auth.token` in `config.yaml` is empty, which disables auth (intended for local-only use). Set it to any opaque string to require it. The middleware accepts it three ways:

- `X-KAM-Token: <token>` header (preferred for REST)
- `Authorization: Bearer <token>` header
- `?token=<token>` query string (used by `<img>`/`<a href>`/WebSocket loads that can't carry custom headers)

`/ui` and `/ui/static/*` are unauthenticated by design — they contain no secrets, and gating them defeats the bootstrap flow that needs to load `app.js` before it can prompt for the token.

## Architecture

```
cmd/kam-transfer            CLI entrypoint (cobra)
cmd/kam-transfer-server     thin entrypoint that just invokes `serve`
internal/adb                ADB transport (wraps goadb)
internal/mtp                MTP transport — cgo+libmtp on linux, stub elsewhere
internal/device             registry, hotplug poller, slot operations
internal/preview            ESRI World Imagery satellite-tile preview JPEGs
internal/kmz                KMZ parse/validate, placeholder-mission generator
internal/api                HTTP + WebSocket server
internal/api/web            embedded admin UI (go:embed static/)
internal/display            optional Raspberry Pi status screen (linux build tag + stub)
internal/config             platform-aware YAML config
internal/names              host-side cache of user-assigned slot names
internal/slotorder          host-side cache of user-chosen slot ordering
internal/managed            host-side per-slot "managed" flag store
pkg/kamtransfer             public embedding API
```

See `docs/` for installation, API reference, CLI usage, configuration, troubleshooting, and development guides.

## Dependencies

- Go 1.25+
- A reachable `adb-server` for ADB-mode devices. Most platforms bundle this with the Android Platform Tools; the daemon will spawn one on `127.0.0.1:5037` if nothing is listening.
- **For DJI RC 2 (MTP) support:** a C toolchain (`gcc` / `build-essential`), `pkg-config`, and `libmtp` + `libmtp-devel`. Build with `make build-mtp` (which sets `CGO_ENABLED=1`); the output binary is `dist/kam-transfer-mtp`, separate from the ADB-only `dist/kam-transfer`. See `docs/INSTALLATION.md` for distro-specific package names. On Linux desktops the daemon will best-effort evict GVFS / `kiod6` / `adb-server` from a fresh DJI USB interface so libmtp can claim it.

## License

TBD.
