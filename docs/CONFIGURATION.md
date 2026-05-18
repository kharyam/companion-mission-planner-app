# Configuration

`kam-transfer` reads YAML from a platform-appropriate path:

| OS      | Path |
|---------|------|
| Linux   | `~/.config/kam-transfer/config.yaml` (or `$XDG_CONFIG_HOME/kam-transfer/config.yaml`) |
| macOS   | `~/Library/Application Support/kam-transfer/config.yaml` |
| Windows | `%APPDATA%\kam-transfer\config.yaml` |

Override with `--config <path>` or the `KAM_TRANSFER_CONFIG` environment variable.

A missing config file is **not** an error — built-in defaults apply.

## Full schema

```yaml
server:
  port: 8765
  bind: 127.0.0.1
  corsOrigins:
    - http://localhost:5173
    - http://127.0.0.1:5173
    - https://kam.example.ts.net
  readTimeout: 30s
  writeTimeout: 5m

map:
  provider: esri-world-imagery   # or "solid" for offline-only
  apiKey: ""                     # unused for ESRI; reserved for future providers
  tileSize: 256
  width: 1024
  height: 768

adb:
  serverHost: 127.0.0.1
  serverPort: 5037
  timeout: 30s
  keyPath: auto                  # uses platform default ($HOME/.android/adbkey)

logging:
  level: info                    # debug | info | warn | error
  file: ""                       # empty = stderr

auth:
  token: ""                      # empty disables auth (default for local dev)

display:
  # enabled: leave unset to auto-detect the Display HAT Mini; set
  # false to disable, or true to force the attempt.
  refreshInterval: 5s            # how often the status screen redraws
  brightness: 80                 # backlight, 0-100
  rotation: 180                  # 0 or 180, to match how the HAT is mounted
  allowShutdown: false           # gate the button-Y safe shutdown
```

## CORS

Browsers refuse to call `localhost:8765` from a remote KAM origin unless that origin is in `corsOrigins`. Add:

- Your Tailscale magic-DNS hostname (`https://kam.<tailnet>.ts.net`)
- Any LAN IPs you serve KAM from
- `http://localhost:5173` if you run KAM's Vite dev server locally

## Auth token

For multi-user machines (e.g. shared workstation), set `auth.token` to a random string and configure KAM to send it as `X-KAM-Token`. Without a token, any process on the same machine could trigger transfers.

## Status display

When the daemon runs on a Raspberry Pi fitted with a [Pimoroni Display HAT Mini](https://shop.pimoroni.com/products/display-hat-mini) (a 2.0" 320×240 LCD with four buttons) and, optionally, a [PiSugar 3](https://www.pisugar.com/) battery UPS, it drives a front-panel status screen showing the server URL, battery, network, and DJI controller state.

The hardware is auto-detected at startup; on a Pi without the HAT, or any non-Pi host, the `display` section is simply ignored. `enabled` is tri-state — omit it to auto-detect, set it `false` to disable the feature outright, or `true` to force the attempt (which surfaces hardware-init errors in the log).

Buttons: **A** cycles pages · **B** toggles the backlight · **X** rescans devices · **Y** taps to show a QR code of the server URL. Holding **Y** for 3 seconds triggers a safe shutdown, but only when `allowShutdown: true` — and the service user must be allowed to run `systemctl poweroff` (a polkit or sudoers rule). See `docs/INSTALLATION.md` for Pi wiring and setup.
