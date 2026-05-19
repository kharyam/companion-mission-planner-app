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

**Building the ARM MTP binary yourself.** cgo can't cross-compile libmtp, so the Makefile builds inside an emulated target-arch container. Install qemu first (Fedora: `sudo dnf install qemu-user-static`; Debian/Ubuntu: `sudo apt install qemu-user-static`).

The container must run **rootful** — rootless podman gets its own user namespace and can't see the host's `binfmt_misc` handlers, so an emulated exec fails with `Exec format error`. Use `sudo podman` or `docker`:

```bash
make build-mtp-linux-arm64 CONTAINER="sudo podman"   # → dist/kam-transfer-mtp-linux-arm64
make build-mtp-linux-armv7 CONTAINER="sudo podman"   # → dist/kam-transfer-mtp-linux-armv7
# or, if docker is installed (its daemon is already rootful):
make build-mtp-linux-arm64 CONTAINER=docker
```

You can also build natively on the Pi itself with `make build-mtp` after installing `libmtp-dev libmtp-runtime build-essential pkg-config` — but on a 512 MB board (e.g. Pi Zero 2 W) the compile is slow and memory-tight, so the cross-build or release download is preferred.

#### Browsing drone/camera media off USB storage

Modern DJI drones (e.g. the Mini 5 Pro) expose their footage as **USB Mass Storage**, not MTP — read either by plugging the drone in directly or, more reliably on a low-power board, by putting its microSD card in a USB card reader. The daemon detects such a volume, mounts it **read-only**, and surfaces its photos/videos in the media gallery.

Mounting needs the `CAP_SYS_ADMIN` capability. The bundled `kam-transfer.service` already grants it via `AmbientCapabilities=CAP_SYS_ADMIN`; if you run the daemon some other way, grant that capability or the USB-media feature is simply skipped (the rest of the daemon is unaffected). exFAT cards need kernel exFAT support — built in since Linux 5.4, so any current Raspberry Pi OS has it.

> A USB card reader exposes the **SD card** only, not a drone's internal storage. Recording to the SD card (the normal field setup) keeps everything reachable.

Installing `ffmpeg` (`sudo apt install ffmpeg`) is optional but recommended: with it, the gallery shows a poster-frame thumbnail for each video instead of a generic icon (one frame is decoded on first view and cached). Without ffmpeg, videos just get the icon — nothing else changes. Note that ffmpeg is used only for these still thumbnails; generating full low-res playback proxies is too slow for low-power boards like the Pi Zero, so video playback streams the original file.

#### Front-panel status screen (Display HAT Mini + PiSugar 3)

If the Pi is fitted with a [Pimoroni Display HAT Mini](https://shop.pimoroni.com/products/display-hat-mini) (a 2.0" 320×240 LCD with four buttons) and, optionally, a [PiSugar 3](https://www.pisugar.com/) battery UPS, the daemon drives an on-device status screen — server URL, battery, network, DJI controller state. It is **auto-detected**: the same binary is a silent no-op on a Pi without the HAT, so no separate build is needed (the feature is pure Go — it ships in the ordinary `make build`).

Enable the SPI and I2C buses the hardware needs, and grant the service user access to them:

```bash
# Enable SPI + I2C — either via raspi-config (Interface Options),
# or directly in /boot/firmware/config.txt:
#   dtparam=spi=on
#   dtparam=i2c_arm=on
sudo raspi-config nonint do_spi 0
sudo raspi-config nonint do_i2c 0

# Let the service user reach the GPIO/SPI/I2C device nodes:
sudo usermod -aG spi,i2c,gpio pi
sudo reboot
```

After the reboot, `ls /dev/spidev0.1 /dev/i2c-1` should both succeed, and `i2cdetect -y 1` should show a device at `0x57` if a PiSugar 3 is attached. Start the daemon (`kam-transfer serve`) and the screen lights up; the log line `status display active` confirms detection (or `status display inactive` with a reason if not).

**Buttons:** **A** cycles pages · **B** toggles the backlight · **X** rescans devices · **Y** tap shows a QR code of the server URL.

**Safe shutdown (optional):** holding **Y** for 3 seconds powers the Pi down — useful with the PiSugar UPS — but only when `display.allowShutdown: true` in `config.yaml` *and* the service user may run `systemctl poweroff`. Grant that with a sudoers drop-in, e.g.:

```
# /etc/sudoers.d/kam-transfer-poweroff
pi ALL=(root) NOPASSWD: /usr/bin/systemctl poweroff
```

Tune the screen with the `display:` block in `config.yaml` — see [CONFIGURATION.md](CONFIGURATION.md#status-display). If the HAT is mounted the other way up, set `rotation: 0`.

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
