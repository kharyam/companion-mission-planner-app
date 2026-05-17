# Installation

`kam-transfer` ships as a single static binary per platform. There is no installer.

## Prerequisites

- **Android Platform Tools** (provides `adb`). The daemon talks to a running `adb-server`; it does not bundle one.
  - macOS: `brew install --cask android-platform-tools`
  - Linux: `apt install android-tools-adb` / `dnf install android-tools` / equivalent
  - Windows: download from <https://developer.android.com/tools/releases/platform-tools>

The daemon will spawn `adb-server` for you if it's on PATH. Otherwise start it manually with `adb start-server`.

## Linux

```bash
curl -L -o kam-transfer https://github.com/kamdynamics/kam-transfer/releases/latest/download/kam-transfer-linux-amd64
chmod +x kam-transfer
./kam-transfer serve
```

To allow USB access without `sudo`, add a udev rule for DJI devices:

```
# /etc/udev/rules.d/51-dji.rules
SUBSYSTEM=="usb", ATTR{idVendor}=="2ca3", MODE="0666", GROUP="plugdev"
```

Reload: `sudo udevadm control --reload-rules && sudo udevadm trigger`.

### MTP backend (for DJI RC 2 and other ADB-disabled devices)

The default pre-built binary uses ADB only. The DJI RC 2 ships with developer options stripped and **does not support ADB** — it must be reached via MTP. To enable the MTP backend you have to build from source with cgo, which means you need a C toolchain in addition to Go.

```bash
# Fedora
sudo dnf install libmtp libmtp-devel gcc pkgconf-pkg-config

# Debian / Ubuntu
sudo apt install libmtp-dev libmtp-runtime build-essential pkg-config

# Arch
sudo pacman -S libmtp pkgconf base-devel

# Then build (CGO_ENABLED=1 happens inside the Makefile target)
make build-mtp
./dist/kam-transfer-mtp serve
```

`make build-mtp` invokes `go build` with `CGO_ENABLED=1`; the build picks up `libmtp` through `pkg-config` (the cgo directive in `internal/mtp/client_linux.go`). If the build fails with "pkg-config: command not found" or "libmtp.pc not found", one of the packages above is missing. The output binary is `dist/kam-transfer-mtp`, separate from the ADB-only `dist/kam-transfer`.

The MTP backend coexists with ADB: both transports are scanned, any device showing up on both is deduplicated (ADB wins for shadows of MTP-only DJI hardware that ADB enumerates but can never authorize). If the RC 2 doesn't appear in `list-devices`, run `mtp-detect` (from `libmtp-examples`) to confirm libmtp sees the device.

#### Desktop interference (GVFS / KDE / adb-server)

On a typical GNOME or KDE desktop, the moment a DJI USB device enumerates, **`gvfsd-mtp` and/or `kiod6` immediately claim its MTP interface** (so the file-manager sidebar can browse it) and **`adb-server` auto-claims the USB device** for any vendor in its allowlist — even a controller that can't speak ADB. libmtp's `libusb_claim_interface` then fails with "device is busy."

The daemon transparently handles this on its first failed open: it asks GVFS to release the volume (`gio mount -u`), kills the relevant `kiod6` / `gvfsd-mtp` workers (they respawn lazily), and stops `adb-server` (the user can restart it). If you see one-off `releaseGVFS step …` log lines around an MTP open, that's the recovery happening — not an error. Each subcommand has a 3-second timeout so a confused desktop daemon can't hang the request.

#### Raspberry Pi / ARM Linux

An MTP binary is published for ARM Linux on every release — handy when the controller plugs into a Pi rather than a desktop. Pick the file matching the Pi's OS architecture (`uname -m`):

- `kam-transfer-mtp-linux-arm64` — 64-bit Raspberry Pi OS (`uname -m` → `aarch64`)
- `kam-transfer-mtp-linux-armv7` — 32-bit Raspberry Pi OS (`uname -m` → `armv7l` / `armv6l`)

On the Pi, install the libmtp **runtime** (not the `-dev` headers — those are only needed to build) and run it:

```bash
sudo apt install libmtp9 libmtp-runtime libusb-1.0-0
curl -L -o kam-transfer https://github.com/kamdynamics/kam-transfer/releases/latest/download/kam-transfer-mtp-linux-arm64
chmod +x kam-transfer
./kam-transfer serve
```

The release binaries are built against Debian 12 "bookworm" — the current Raspberry Pi OS base. On an older Pi OS (bullseye) the glibc/libmtp versions won't match; build from source on the Pi instead, or use the cross-build below.

To run it unattended (survives reboots and SSH disconnects), install the systemd unit from [`deploy/kam-transfer.service`](../deploy/kam-transfer.service):

```bash
sudo cp kam-transfer /usr/local/bin/
sudo cp deploy/kam-transfer.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now kam-transfer
```

**Building the ARM MTP binary yourself.** cgo can't cross-compile libmtp, so the Makefile builds inside an emulated target-arch container. From any machine with `podman` (or `docker`) and qemu binfmt:

```bash
podman run --rm --privileged docker.io/tonistiigi/binfmt --install arm64,arm
make build-mtp-linux-arm64      # → dist/kam-transfer-mtp-linux-arm64
make build-mtp-linux-armv7      # → dist/kam-transfer-mtp-linux-armv7
# docker instead of podman:  make build-mtp-linux-arm64 CONTAINER=docker
```

You can also build natively on the Pi itself with `make build-mtp` after installing `libmtp-dev libmtp-runtime build-essential pkg-config` — but on a 512 MB board (e.g. Pi Zero 2 W) the compile is slow and memory-tight, so the cross-build or release download is preferred.

## macOS

```bash
curl -L -o kam-transfer https://github.com/kamdynamics/kam-transfer/releases/latest/download/kam-transfer-macos-arm64
chmod +x kam-transfer
xattr -d com.apple.quarantine kam-transfer   # remove Gatekeeper quarantine for unsigned builds
./kam-transfer serve
```

Builds are not yet code-signed. The `xattr` step or a one-time "right-click → Open" is required for the first launch.

## Windows

Download `kam-transfer-windows-amd64.exe` from the releases page. Double-click or run from PowerShell:

```powershell
.\kam-transfer-windows-amd64.exe serve
```

Windows SmartScreen may flag unsigned builds the first time; click "More info" → "Run anyway".

## From source

```bash
git clone https://github.com/kamdynamics/kam-transfer.git
cd kam-transfer
make build
./dist/kam-transfer serve
```

Requires Go 1.25+. `make build` is CGO-off and pure Go; `make build-mtp` additionally needs a C toolchain, `pkg-config`, and `libmtp` development headers — see the MTP-backend subsection above.
