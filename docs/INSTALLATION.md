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

The default pre-built binary uses ADB only. The DJI RC 2 ships with developer options stripped and **does not support ADB** — it must be reached via MTP. To enable the MTP backend you have to build from source with cgo:

```bash
# Fedora
sudo dnf install libmtp libmtp-devel

# Debian / Ubuntu
sudo apt install libmtp-dev libmtp-runtime

# Then build
make build-mtp
./dist/kam-transfer-mtp serve
```

The MTP backend coexists with ADB: both transports are scanned, and any device showing up on ADB takes precedence over the same device showing up on MTP. If the RC 2 doesn't appear in `list-devices`, run `mtp-detect` (from `libmtp-examples`) to confirm libmtp sees the device.

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

Requires Go 1.22+.
