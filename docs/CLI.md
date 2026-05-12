# CLI reference

All commands accept `--config <path>` and `--log-level debug|info|warn|error`.

## `kam-transfer serve`

Start the local HTTP API.

```bash
kam-transfer serve [--port 8765] [--bind 127.0.0.1]
```

Running `kam-transfer` with no subcommand is equivalent to `serve`.

## `kam-transfer list-devices`

List connected DJI controllers and phones.

```bash
kam-transfer list-devices
# abc123    DJI RC 2    adb    authorized=true    dji-fly=true
```

## `kam-transfer list-slots --device <id>`

List waypoint slots on a device.

```bash
kam-transfer list-slots --device abc123
# 550E8400-E29B-41D4-A716-446655440000    Slot 550E8400    4321 bytes    preview=true
```

## `kam-transfer transfer <kmz-file> --device <id> --slot <guid> [--name "..."]`

Push a KMZ into a specific slot.

```bash
kam-transfer transfer ./north-field.kmz \
  --device abc123 \
  --slot 550E8400-E29B-41D4-A716-446655440000 \
  --name "North Field Survey"
```

## `kam-transfer clear-slot --device <id> --slot <guid>`

Mark a slot as available.

## `kam-transfer version`

Print the version string baked in at build time.
