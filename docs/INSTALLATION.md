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
