# Development

## Layout

```
cmd/kam-transfer/        CLI (cobra, all subcommands)
cmd/kam-transfer-server/ thin server-only entrypoint
internal/adb/            ADB transport (wraps goadb)
internal/mtp/            MTP fallback (stub)
internal/device/         registry, controller interface, slot paths
internal/preview/        ESRI tile renderer
internal/kmz/            parse, validate, GUID rewrite
internal/config/         platform-aware YAML loader
internal/api/            net/http server + WebSocket
internal/version/        ldflags-injected version string
pkg/kamtransfer/         public embedding API
```

`internal/` packages are not importable from outside the module. `pkg/kamtransfer` re-exports the minimum surface for embedders.

## Build

```bash
make build         # current platform, ADB-only (CGO_ENABLED=0)
make build-mtp     # adds libmtp backend (CGO_ENABLED=1, needs libmtp-devel)
make build-all     # linux/amd64 + macos amd64+arm64 + windows/amd64, ADB-only
```

Binaries land in `dist/`. Version is `git describe --tags --always --dirty` injected via `-ldflags`.

### Why two build flavors

The default build is pure Go so it cross-compiles to all four platform targets from any host. The MTP build pulls in libmtp via cgo, which breaks easy cross-compile — each platform needs a native runner with its own libmtp install. We ship MTP behind a build tag (`linux && cgo`) so the default binary stays portable. The MTP path is opt-in for users who need it (most often, RC 2 owners).

## Test

```bash
make test
```

Unit tests are colocated (`*_test.go`). Integration tests need a real RC 2 or phone — manual checklist below.

### Manual integration checklist

- [ ] `kam-transfer list-devices` shows connected RC 2
- [ ] Approve USB debugging on RC 2 → next list-devices shows `authorized=true`
- [ ] `kam-transfer list-slots --device <id>` matches the slots visible in DJI Fly
- [ ] `kam-transfer transfer ./fixture.kmz --device <id> --slot <guid>` succeeds
- [ ] Mission appears in DJI Fly with the new name
- [ ] Preview JPEG is generated and uploaded
- [ ] `POST /api/devices/<id>/refresh` picks up newly-created placeholder missions
- [ ] WebSocket emits `device.connected` / `device.disconnected` on cable events
- [ ] Multiple devices: list shows both, transfer routes to the right one
- [ ] Disconnect mid-transfer leaves the previous slot intact

## Adding API endpoints

1. Add a handler method on `*Server` in `internal/api/handlers.go`.
2. Wire the route in `Server.routes` (`internal/api/server.go`). Use Go 1.22's `METHOD /path/{param}` pattern syntax.
3. Update `docs/API.md` with the request/response shape and any new error codes.
4. If the handler emits events, call `s.broadcast(...)` so WebSocket subscribers see them.

## Adding error codes

Add the constant to `internal/api/errors.go`, document it in `docs/API.md`, and prefer wrapping device-layer errors with `errors.Is`-friendly sentinel errors so `handleRegistryError` can map them.

## Replacing goadb

`internal/adb/{client,transport,sync}.go` is the only place goadb is referenced. To swap for a custom protocol implementation:

1. Keep the `Client`, `Transport`, `Device` types and their method signatures.
2. Replace the imports and bodies.
3. The rest of the tree compiles unchanged.

## Releasing

Tag a version: `git tag v0.1.0 && git push --tags`. GitHub Actions (`.github/workflows/release.yml`) builds all platforms and attaches binaries to the release.
