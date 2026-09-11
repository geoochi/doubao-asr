#!/usr/bin/env bash
#
# One-click installer for doubao-asr.
#
# Downloads the prebuilt `doubao-dictate` binary, sets up its config, installs
# the systemd user service and adds the Omarchy/Hyprland hotkey binding.
# Everything is user-scoped (no sudo) and the script is safe to re-run: an
# existing .env is never overwritten and the hotkey block is added only once.
#
#   curl -fsSL https://raw.githubusercontent.com/geoochi/doubao-asr/main/install.sh | bash
#
# Run with --help for options.

set -euo pipefail

REPO="geoochi/doubao-asr"
BIN_DIR="${HOME}/.local/bin"
BIN="${BIN_DIR}/doubao-dictate"
CONF_DIR="${HOME}/.config/doubao-dictate"
ENV_FILE="${CONF_DIR}/.env"
UNIT_DIR="${HOME}/.config/systemd/user"
UNIT="${UNIT_DIR}/doubao-dictate.service"
HYPR_BINDINGS="${HOME}/.config/hypr/bindings.lua"

say() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

for arg in "$@"; do
  case "$arg" in
    -h | --help)
      sed -n '2,12p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) fail "unknown argument: $arg" ;;
  esac
done

case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) fail "unsupported architecture: $(uname -m) (only linux amd64/arm64 are published)" ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required"

# ---- 1. the binary ------------------------------------------------------
base="https://github.com/${REPO}/releases/latest/download"
asset="doubao-dictate-linux-${ARCH}"
mkdir -p "$BIN_DIR"
tmp="$(mktemp)"
sha="$(mktemp)"
trap 'rm -f "$tmp" "$sha"' EXIT

say "Downloading ${asset}…"
if [ -t 2 ]; then progress="--progress-bar"; else progress="-sS"; fi
curl -fL $progress --retry 3 --connect-timeout 15 "${base}/${asset}" -o "$tmp" ||
  fail "download failed — see https://github.com/${REPO}/releases"

if curl -fsL --retry 3 --connect-timeout 15 "${base}/${asset}.sha256" -o "$sha" 2>/dev/null; then
  expected="$(awk '{print $1}' "$sha")"
  actual="$(sha256sum "$tmp" | awk '{print $1}')"
  [ "$expected" = "$actual" ] || fail "checksum mismatch (expected ${expected}, got ${actual})"
  say "Checksum verified"
fi

install -m 755 "$tmp" "$BIN"
say "Installed ${BIN}"

# ---- 2. config (leave an existing file alone) ---------------------------
if [ -f "$ENV_FILE" ]; then
  say "Keeping existing ${ENV_FILE}"
else
  mkdir -p "$CONF_DIR"
  cat > "$ENV_FILE" <<'EOF'
# Doubao dictation configuration. Fill in DOUBAO_API_KEY below.
DOUBAO_API_KEY=
DOUBAO_URL=wss://openspeech.bytedance.com/api/v3/sauc/bigmodel_nostream
DOUBAO_RESOURCE_ID=volc.seedasr.sauc.duration
DOUBAO_DEVICE=default
DOUBAO_SAMPLE_RATE=16000
DOUBAO_SEGMENT_MS=200
DOUBAO_MAX_DURATION_SECS=120
DOUBAO_MODE=type
DOUBAO_NOTIFY=true
EOF
  chmod 600 "$ENV_FILE"
  say "Created ${ENV_FILE} (add your API key here)"
fi

# ---- 3. systemd user service -------------------------------------------
mkdir -p "$UNIT_DIR"
cat > "$UNIT" <<'EOF'
[Unit]
Description=Doubao dictation daemon
PartOf=graphical-session.target
After=graphical-session.target pipewire-pulse.service

[Service]
ExecStart=%h/.local/bin/doubao-dictate daemon
Restart=on-failure
RestartSec=3

[Install]
WantedBy=graphical-session.target
EOF
say "Wrote ${UNIT}"

if command -v systemctl >/dev/null 2>&1; then
  systemctl --user daemon-reload || fail "systemctl --user daemon-reload failed"
  systemctl --user enable doubao-dictate >/dev/null 2>&1 || true
  systemctl --user restart doubao-dictate || say "warning: could not start the service"
  # The socket appears a moment after the restart; wait for it so a status
  # call straight after the install does not race the daemon.
  sock="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/doubao-dictate.sock"
  for _ in $(seq 1 20); do
    [ -S "$sock" ] && break
    sleep 0.1
  done
  say "Service doubao-dictate: $(systemctl --user is-active doubao-dictate 2>/dev/null || echo unknown)"
else
  say "warning: systemctl not found; start it manually with: doubao-dictate daemon"
fi

# ---- 4. Omarchy / Hyprland hotkey (append once, then reload) ------------
if [ -f "$HYPR_BINDINGS" ] && grep -qF "doubao-dictate toggle" "$HYPR_BINDINGS"; then
  say "Hotkey binding already present"
else
  mkdir -p "$(dirname "$HYPR_BINDINGS")"
  cat >> "$HYPR_BINDINGS" <<'EOF'

-- doubao-dictate (managed by install.sh)
hl.unbind("SUPER + CTRL + X")
hl.unbind("F9")
o.bind("SUPER + CTRL + X", "Doubao dictation (toggle)", "doubao-dictate toggle")
o.bind("F9", "Doubao dictation (press to start/stop)", "doubao-dictate toggle")
o.bind("SUPER + CTRL + ESCAPE", "Doubao dictation cancel", "doubao-dictate cancel")
EOF
  say "Added hotkey bindings to ${HYPR_BINDINGS}"
  if command -v hyprctl >/dev/null 2>&1; then
    hyprctl reload >/dev/null 2>&1 || true
    say "Reloaded Hyprland"
  fi
fi

say ""
say "Done. Next: put your API key in ${ENV_FILE}, then"
say "  systemctl --user restart doubao-dictate"
say "and press F9 to dictate."
