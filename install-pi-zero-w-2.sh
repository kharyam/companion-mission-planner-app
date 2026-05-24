#!/usr/bin/env bash
#
# install.sh — turnkey installer for the kam-transfer companion daemon on a
# fresh Raspberry Pi (built/tested on a Pi Zero 2 W, Raspberry Pi OS bookworm).
#
# It installs Go + all build/runtime dependencies, clones (or updates) the
# repo into your home directory, builds the daemon, deploys the binary and
# systemd units, enables the Display HAT Mini buses, and seeds a config.
# After it finishes: reboot, and the daemon (and front-panel screen, if the
# HAT is fitted) come up automatically.
#
#   Usage:   ./install.sh          # run as your normal login user, NOT sudo
#
# Everything below is overridable from the environment, e.g.
#   BUILD_MTP=false BIND_ADDR=127.0.0.1 ./install.sh
#
# ---------------------------------------------------------------------------
set -euo pipefail

# ---- tunables --------------------------------------------------------------
REPO_URL="${REPO_URL:-https://github.com/kharyam/companion-mission-planner-app.git}"
REPO_BRANCH="${REPO_BRANCH:-main}"
CLONE_DIR="${CLONE_DIR:-$HOME/companion-mission-planner-app}"

BUILD_MTP="${BUILD_MTP:-true}"      # true = DJI RC 2 (cgo+libmtp) support; false = ADB-only, fast
INSTALL_DISPLAY="${INSTALL_DISPLAY:-true}"  # Display HAT Mini status screen + boot splash

BIND_ADDR="${BIND_ADDR:-0.0.0.0}"   # 0.0.0.0 = reachable from the remote KAM UI; 127.0.0.1 = local only
ENABLE_AUTH="${ENABLE_AUTH:-true}"  # true = seed a random auth.token; false = no auth
AUTH_TOKEN="${AUTH_TOKEN:-}"        # leave empty to auto-generate when ENABLE_AUTH=true
SERVER_PORT="${SERVER_PORT:-8765}"
DISPLAY_ROTATION="${DISPLAY_ROTATION:-0}"   # 0 or 180 — match how the HAT is mounted

SET_MULTI_USER="${SET_MULTI_USER:-true}"    # boot to console (no graphical login)
SWAP_MB="${SWAP_MB:-1024}"          # bumped for the memory-tight cgo build on a 512 MB board
GO_VERSION="${GO_VERSION:-}"        # empty = fetch the latest stable from go.dev
GO_VERSION_FALLBACK="go1.25.4"

# ---- pretty logging --------------------------------------------------------
BOLD=$'\033[1m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; BLUE=$'\033[36m'; RST=$'\033[0m'
step() { printf '\n%s==> %s%s\n' "$BLUE$BOLD" "$*" "$RST"; }
info() { printf '%s   %s%s\n' "$GREEN" "$*" "$RST"; }
warn() { printf '%s   warning: %s%s\n' "$YELLOW" "$*" "$RST" >&2; }
die()  { printf '%s   error: %s%s\n' "$RED" "$*" "$RST" >&2; exit 1; }

# ---- preflight -------------------------------------------------------------
if [ "$(id -u)" -eq 0 ]; then
  die "run this as your normal login user (NOT root / sudo). It calls sudo itself when it needs to."
fi
command -v sudo >/dev/null 2>&1 || die "sudo is required but not installed."

SERVICE_USER="$(id -un)"
SERVICE_HOME="$HOME"
step "kam-transfer installer  (user=$SERVICE_USER  home=$SERVICE_HOME)"
info "build=$([ "$BUILD_MTP" = true ] && echo 'MTP+ADB' || echo 'ADB-only')  bind=$BIND_ADDR:$SERVER_PORT  display=$INSTALL_DISPLAY"

# Prime the sudo timestamp once so later steps don't each prompt.
sudo -v || die "could not obtain sudo privileges."

# Detect Go architecture from the running kernel.
case "$(uname -m)" in
  aarch64|arm64)            GOARCH_PKG="arm64" ;;
  armv7l|armv6l|arm|armhf)  GOARCH_PKG="armv6l" ;;   # Go ships one 32-bit ARM build
  x86_64|amd64)             GOARCH_PKG="amd64" ;;     # allows dry-runs on a desktop
  *) die "unsupported CPU architecture: $(uname -m)" ;;
esac
info "architecture $(uname -m) -> Go $GOARCH_PKG"

# ---- 1. system packages ----------------------------------------------------
step "Installing system packages (apt)"
sudo apt-get update -y

# Required: build toolchain, git, USB/media helpers.
REQUIRED_PKGS=(git curl ca-certificates build-essential pkg-config
               ffmpeg i2c-tools usbutils)
if [ "$BUILD_MTP" = true ]; then
  # libmtp + libusb dev headers to build; runtime libs come in as deps.
  REQUIRED_PKGS+=(libmtp-dev libusb-1.0-0-dev libmtp-runtime)
fi
sudo apt-get install -y "${REQUIRED_PKGS[@]}"

# ADB: package name varies by release ('adb' on bookworm, 'android-tools-adb'
# on older Debian/Ubuntu). Best-effort — the daemon spawns adb-server itself
# when 'adb' is on PATH, and runs fine without it (just no ADB-mode devices).
if command -v adb >/dev/null 2>&1; then
  info "adb already present"
elif sudo apt-get install -y adb >/dev/null 2>&1; then
  info "installed adb"
elif sudo apt-get install -y android-tools-adb >/dev/null 2>&1; then
  info "installed android-tools-adb"
else
  warn "could not install adb — ADB-mode devices won't be detected (MTP still works)"
fi

# Handy utilities — best-effort, a missing one must not abort the install.
step "Installing utilities (tmux, btop, …)"
for pkg in tmux btop htop vim jq git-lfs; do
  if sudo apt-get install -y "$pkg" >/dev/null 2>&1; then
    info "installed $pkg"
  else
    warn "could not install '$pkg' (skipping)"
  fi
done

# ---- 2. Go toolchain -------------------------------------------------------
step "Installing the Go toolchain"
if [ -z "$GO_VERSION" ]; then
  GO_VERSION="$(curl -fsSL 'https://go.dev/VERSION?m=text' 2>/dev/null | head -n1 || true)"
  [ -n "$GO_VERSION" ] || GO_VERSION="$GO_VERSION_FALLBACK"
fi
info "target $GO_VERSION (linux-$GOARCH_PKG)"

if /usr/local/go/bin/go version 2>/dev/null | grep -q "$GO_VERSION"; then
  info "$GO_VERSION already installed at /usr/local/go — skipping download"
else
  TARBALL="${GO_VERSION}.linux-${GOARCH_PKG}.tar.gz"
  info "downloading $TARBALL"
  curl -fSL "https://go.dev/dl/${TARBALL}" -o "/tmp/${TARBALL}"
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf "/tmp/${TARBALL}"
  rm -f "/tmp/${TARBALL}"
  info "installed Go to /usr/local/go"
fi

# Make Go available in every future login shell.
sudo tee /etc/profile.d/go.sh >/dev/null <<'EOF'
export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin"
EOF
sudo chmod 0644 /etc/profile.d/go.sh
# …and in *this* script's shell, for the build below.
export PATH="/usr/local/go/bin:$PATH"
go version

# ---- 3. swap (only when the cgo build needs the headroom) ------------------
if [ "$BUILD_MTP" = true ]; then
  step "Ensuring at least ${SWAP_MB} MB of swap for the build"
  if command -v dphys-swapfile >/dev/null 2>&1 && [ -f /etc/dphys-swapfile ]; then
    cur="$(grep -E '^CONF_SWAPSIZE=' /etc/dphys-swapfile 2>/dev/null | cut -d= -f2 | tr -dc '0-9' || true)"
    if [ "${cur:-0}" -lt "$SWAP_MB" ]; then
      sudo dphys-swapfile swapoff 2>/dev/null || true
      sudo sed -i '/^#\?CONF_SWAPSIZE=/d;/^#\?CONF_MAXSWAP=/d' /etc/dphys-swapfile
      printf 'CONF_SWAPSIZE=%s\nCONF_MAXSWAP=%s\n' "$SWAP_MB" "$SWAP_MB" | sudo tee -a /etc/dphys-swapfile >/dev/null
      sudo dphys-swapfile setup
      sudo dphys-swapfile swapon
      info "swap set to ${SWAP_MB} MB via dphys-swapfile"
    else
      info "existing swap (${cur} MB) already >= ${SWAP_MB} MB"
    fi
  else
    # Non-Pi-OS fallback: a plain swapfile.
    swap_now="$(free -m | awk '/^Swap:/{print $2}' || true)"; swap_now="${swap_now:-0}"
    if [ "$swap_now" -lt "$SWAP_MB" ] && [ ! -f /var/swap.kam ]; then
      sudo fallocate -l "${SWAP_MB}M" /var/swap.kam || sudo dd if=/dev/zero of=/var/swap.kam bs=1M count="$SWAP_MB"
      sudo chmod 600 /var/swap.kam
      sudo mkswap /var/swap.kam
      sudo swapon /var/swap.kam
      grep -q '/var/swap.kam' /etc/fstab || echo '/var/swap.kam none swap sw 0 0' | sudo tee -a /etc/fstab >/dev/null
      info "created /var/swap.kam (${SWAP_MB} MB)"
    else
      info "swap already sufficient"
    fi
  fi
fi

# ---- 4. source: clone into ~/ or reuse an existing checkout ----------------
step "Fetching source"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -d "$CLONE_DIR/.git" ]; then
  SRC_DIR="$CLONE_DIR"
elif [ -f "$SCRIPT_DIR/go.mod" ] && grep -q 'kam-transfer' "$SCRIPT_DIR/go.mod" 2>/dev/null; then
  SRC_DIR="$SCRIPT_DIR"           # we're running from inside an existing checkout
else
  SRC_DIR="$CLONE_DIR"
fi

if [ -d "$SRC_DIR/.git" ]; then
  info "using existing checkout at $SRC_DIR — updating"
  git -C "$SRC_DIR" fetch --quiet origin "$REPO_BRANCH" || warn "git fetch failed; building current checkout"
  git -C "$SRC_DIR" pull --ff-only origin "$REPO_BRANCH" 2>/dev/null \
    || warn "could not fast-forward (local changes?); building current checkout"
else
  info "cloning $REPO_URL -> $SRC_DIR"
  if ! git clone --branch "$REPO_BRANCH" "$REPO_URL" "$SRC_DIR"; then
    die "clone failed. If the repo is private, set up a deploy key / token, or set REPO_URL to a reachable URL."
  fi
fi

# ---- 5. build --------------------------------------------------------------
step "Building the daemon (this can take a while on a Pi Zero 2 W)"
cd "$SRC_DIR"
if [ "$BUILD_MTP" = true ]; then
  # Limit build parallelism to keep peak memory sane on a 512 MB board.
  GOFLAGS="-p=2" make build-mtp
  BUILT_BIN="$SRC_DIR/dist/kam-transfer-mtp"
else
  make build
  BUILT_BIN="$SRC_DIR/dist/kam-transfer"
fi
[ -x "$BUILT_BIN" ] || die "build did not produce $BUILT_BIN"

step "Installing binary -> /usr/local/bin/kam-transfer"
sudo install -m 0755 "$BUILT_BIN" /usr/local/bin/kam-transfer
/usr/local/bin/kam-transfer version || true

# ---- 6. USB device access (udev) -------------------------------------------
step "Installing udev rule for DJI USB devices"
sudo tee /etc/udev/rules.d/51-dji.rules >/dev/null <<'EOF'
# DJI controllers / drones — allow non-root USB access for kam-transfer.
SUBSYSTEM=="usb", ATTR{idVendor}=="2ca3", MODE="0666", GROUP="plugdev"
EOF
sudo udevadm control --reload-rules
sudo udevadm trigger || true

# Ensure the service user can reach USB devices.
getent group plugdev >/dev/null || sudo groupadd plugdev
sudo usermod -aG plugdev "$SERVICE_USER"

# ---- 7. Display HAT Mini: SPI/I2C buses, groups, shutdown rule -------------
if [ "$INSTALL_DISPLAY" = true ]; then
  step "Enabling SPI + I2C for the Display HAT Mini"
  if command -v raspi-config >/dev/null 2>&1; then
    sudo raspi-config nonint do_spi 0 || warn "raspi-config do_spi failed"
    sudo raspi-config nonint do_i2c 0 || warn "raspi-config do_i2c failed"
  else
    # Fallback: edit the firmware config directly.
    CONFIG_TXT=/boot/firmware/config.txt
    [ -f "$CONFIG_TXT" ] || CONFIG_TXT=/boot/config.txt
    if [ -f "$CONFIG_TXT" ]; then
      grep -q '^dtparam=spi=on'     "$CONFIG_TXT" || echo 'dtparam=spi=on'     | sudo tee -a "$CONFIG_TXT" >/dev/null
      grep -q '^dtparam=i2c_arm=on' "$CONFIG_TXT" || echo 'dtparam=i2c_arm=on' | sudo tee -a "$CONFIG_TXT" >/dev/null
      info "added dtparam spi/i2c to $CONFIG_TXT"
    else
      warn "no raspi-config and no config.txt found — enable SPI/I2C manually"
    fi
  fi

  step "Granting the service user access to GPIO/SPI/I2C"
  for grp in spi i2c gpio; do
    getent group "$grp" >/dev/null || sudo groupadd "$grp"
  done
  sudo usermod -aG spi,i2c,gpio "$SERVICE_USER"

  step "Installing sudoers rule for the front-panel safe-shutdown (button Y)"
  SYSTEMCTL_BIN="$(command -v systemctl || echo /usr/bin/systemctl)"
  SUDOERS_TMP="$(mktemp)"
  printf '%s ALL=(root) NOPASSWD: %s poweroff\n' "$SERVICE_USER" "$SYSTEMCTL_BIN" > "$SUDOERS_TMP"
  if sudo visudo -cf "$SUDOERS_TMP" >/dev/null; then
    sudo install -m 0440 -o root -g root "$SUDOERS_TMP" /etc/sudoers.d/kam-transfer-poweroff
    info "installed /etc/sudoers.d/kam-transfer-poweroff"
  else
    warn "generated sudoers file failed validation — skipping (safe shutdown won't work)"
  fi
  rm -f "$SUDOERS_TMP"
fi

# ---- 8. config seed --------------------------------------------------------
step "Seeding config.yaml"
CONFIG_DIR="$SERVICE_HOME/.config/kam-transfer"
CONFIG_FILE="$CONFIG_DIR/config.yaml"
if [ -f "$CONFIG_FILE" ]; then
  warn "$CONFIG_FILE already exists — leaving it untouched"
else
  mkdir -p "$CONFIG_DIR"
  if [ "$ENABLE_AUTH" = true ] && [ -z "$AUTH_TOKEN" ]; then
    AUTH_TOKEN="$(openssl rand -hex 16 2>/dev/null || tr -dc 'a-f0-9' </dev/urandom | head -c 32)"
  fi
  [ "$ENABLE_AUTH" = true ] || AUTH_TOKEN=""

  {
    echo "server:"
    echo "  port: $SERVER_PORT"
    echo "  bind: $BIND_ADDR"
    echo "  corsOrigins:"
    echo "    - http://localhost:5173"
    echo "    - http://127.0.0.1:5173"
    echo "    # Add your KAM Mission Planner origin(s) here, e.g.:"
    echo "    # - https://kam.<your-tailnet>.ts.net"
    echo ""
    echo "logging:"
    echo "  level: info"
    echo ""
    echo "auth:"
    echo "  token: \"$AUTH_TOKEN\""
    if [ "$INSTALL_DISPLAY" = true ]; then
      echo ""
      echo "display:"
      echo "  refreshInterval: 5s"
      echo "  brightness: 80"
      echo "  rotation: $DISPLAY_ROTATION"
      echo "  allowShutdown: true"
    fi
  } > "$CONFIG_FILE"
  info "wrote $CONFIG_FILE"
fi

# ---- 9. systemd units ------------------------------------------------------
step "Installing systemd units"

# Main daemon. SupplementaryGroups is uncommented when the display is enabled
# so the service user reaches the SPI/I2C/GPIO nodes.
SUPP_GROUPS_LINE="# (display disabled — no SupplementaryGroups)"
[ "$INSTALL_DISPLAY" = true ] && SUPP_GROUPS_LINE="SupplementaryGroups=spi i2c gpio"

sudo tee /etc/systemd/system/kam-transfer.service >/dev/null <<EOF
# Generated by install.sh
[Unit]
Description=KAM Mission Planner companion daemon
Documentation=https://github.com/kamdynamics/kam-transfer
After=network-online.target kam-transfer-splash.service
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
WorkingDirectory=$SERVICE_HOME
# Free the SPI bus from the boot splash before we open the HAT. '+' runs as
# root; '-' ignores failure when the splash isn't installed/running.
ExecStartPre=-+/usr/bin/systemctl stop kam-transfer-splash.service
ExecStart=/usr/local/bin/kam-transfer serve
Restart=on-failure
RestartSec=5
# Mounting camera/drone USB storage read-only needs CAP_SYS_ADMIN.
AmbientCapabilities=CAP_SYS_ADMIN
$SUPP_GROUPS_LINE

[Install]
WantedBy=multi-user.target
EOF
info "installed kam-transfer.service (User=$SERVICE_USER)"

sudo systemctl daemon-reload
sudo systemctl enable kam-transfer.service

if [ "$INSTALL_DISPLAY" = true ]; then
  sudo tee /etc/systemd/system/kam-transfer-splash.service >/dev/null <<EOF
# Generated by install.sh — front-panel boot splash (optional, self-disables
# on a Pi without the HAT).
[Unit]
Description=KAM Mission Planner front-panel boot splash
Documentation=https://github.com/kamdynamics/kam-transfer
DefaultDependencies=no
After=systemd-udevd.service systemd-journald.service
Before=kam-transfer.service shutdown.target
Conflicts=shutdown.target

[Service]
Type=simple
User=$SERVICE_USER
SupplementaryGroups=spi gpio systemd-journal
ExecStart=/usr/local/bin/kam-transfer splash
Restart=no
TimeoutStopSec=5

[Install]
WantedBy=multi-user.target
EOF
  sudo systemctl daemon-reload
  sudo systemctl enable kam-transfer-splash.service
  info "installed + enabled kam-transfer-splash.service"
fi

# ---- 10. boot target -------------------------------------------------------
if [ "$SET_MULTI_USER" = true ]; then
  step "Setting default boot target to multi-user (console, no graphical login)"
  sudo systemctl set-default multi-user.target
  info "default target: $(sudo systemctl get-default)"
fi

# ---- done ------------------------------------------------------------------
step "Done"
cat <<EOF

${BOLD}kam-transfer is installed and enabled.${RST}

  binary    : /usr/local/bin/kam-transfer  ($([ "$BUILD_MTP" = true ] && echo 'MTP+ADB' || echo 'ADB-only'))
  config    : $CONFIG_FILE
  service   : kam-transfer.service (starts on boot)$([ "$INSTALL_DISPLAY" = true ] && echo '
  splash    : kam-transfer-splash.service (front-panel boot screen)')
  listening : http://${BIND_ADDR}:${SERVER_PORT}   (UI at /ui, health at /api/health)
EOF

if [ "$ENABLE_AUTH" = true ] && [ -n "${AUTH_TOKEN:-}" ]; then
  printf '\n  %sAUTH TOKEN: %s%s\n' "$YELLOW$BOLD" "$AUTH_TOKEN" "$RST"
  echo  "  Send it from KAM as the 'X-KAM-Token' header (or ?token=… ). It's stored in the config above."
fi

cat <<EOF

${YELLOW}${BOLD}Reboot to finish:${RST}  the SPI/I2C buses and your new group memberships
(spi/i2c/gpio/plugdev) only take effect after a reboot, and the daemon +
front-panel screen come up automatically on the next boot.

  ${BOLD}sudo reboot${RST}

After reboot, verify with:
  systemctl status kam-transfer
  curl http://127.0.0.1:${SERVER_PORT}/api/health
EOF
