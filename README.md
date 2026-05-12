# kam-transfer

Cross-platform companion daemon for [KAM Mission Planner](https://github.com/kamdynamics). Exposes a local HTTP API that the browser-based KAM UI calls to push KMZ waypoint missions onto USB-connected DJI controllers (RC 2, RC Pro) and Android phones running DJI Fly.

## Why it exists

KAM Mission Planner runs in a browser, often hosted remotely (e.g. TrueNAS over Tailscale). Browsers can't reach the USB bus. This daemon runs locally on the machine where the controller plugs in, handles all the ADB/MTP plumbing, and surfaces a clean HTTP API the remote UI can call.

## Status

**Scaffold.** The project structure, CLI, HTTP API surface, and module boundaries are in place. Most operations return `NOT_IMPLEMENTED` — see the per-file TODOs. The ESRI preview generator and goadb wrapper have functional skeletons; device protocol details still need real-hardware iteration.

## Quick start

```bash
# Build
make build

# Run the server
./dist/kam-transfer serve

# Health check
curl http://127.0.0.1:8765/api/health

# List connected devices
./dist/kam-transfer list-devices
```

## Cross-compile

```bash
make build-all      # linux/amd64, darwin/amd64+arm64, windows/amd64
```

Binaries land in `dist/`.

## Architecture

```
cmd/kam-transfer        CLI entrypoint (cobra)
cmd/kam-transfer-server thin entrypoint that just invokes `serve`
internal/adb            ADB transport (wraps goadb)
internal/mtp            MTP fallback (stub)
internal/device         device detection + slot management
internal/preview        ESRI World Imagery preview JPEGs
internal/kmz            KMZ parse/validate/rewrite
internal/config         platform-aware YAML config
internal/api            HTTP + WebSocket server
pkg/kamtransfer         public embedding API
```

See `docs/` for installation, API reference, CLI usage, configuration, troubleshooting, and development guides.

## Dependencies

- Go 1.22+
- `adb` server reachable on `127.0.0.1:5037` (goadb speaks to a running adb-server). Most platforms bundle this with the Android Platform Tools.
- **For DJI RC 2 support:** libmtp (build with `make build-mtp` after installing `libmtp-devel`). The consumer RC 2 doesn't support ADB. See `docs/INSTALLATION.md`.

## License

TBD.
