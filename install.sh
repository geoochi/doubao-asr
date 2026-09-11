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
fail() { printf '错误: %s\n' "$*" >&2; exit 1; }

for arg in "$@"; do
  case "$arg" in
    -h | --help)
      cat <<'USAGE'
doubao-asr 一键安装脚本

用法:
  install.sh            安装或升级(可重复执行,幂等)
  install.sh --help     显示本帮助

会安装:doubao-dictate 二进制、systemd 用户服务、F9 热键绑定。
装完会提示你粘贴豆包 API Key(也可以直接回车跳过,之后再填)。
全程用户级操作,不需要 sudo。

API Key 获取:https://console.volcengine.com/speech/
USAGE
      exit 0
      ;;
    *) fail "未知参数:$arg" ;;
  esac
done

case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) fail "不支持的架构:$(uname -m)(当前只发布 linux amd64/arm64)" ;;
esac

command -v curl >/dev/null 2>&1 || fail "需要 curl,请先安装后再运行"

# ---- 1. the binary ------------------------------------------------------
base="https://github.com/${REPO}/releases/latest/download"
asset="doubao-dictate-linux-${ARCH}"
mkdir -p "$BIN_DIR"
tmp="$(mktemp)"
sha="$(mktemp)"
trap 'rm -f "$tmp" "$sha"' EXIT

say "正在下载 ${asset}…"
if [ -t 2 ]; then progress="--progress-bar"; else progress="-sS"; fi
curl -fL $progress --retry 3 --connect-timeout 15 "${base}/${asset}" -o "$tmp" ||
  fail "下载失败,请查看 https://github.com/${REPO}/releases"

if curl -fsL --retry 3 --connect-timeout 15 "${base}/${asset}.sha256" -o "$sha" 2>/dev/null; then
  expected="$(awk '{print $1}' "$sha")"
  actual="$(sha256sum "$tmp" | awk '{print $1}')"
  [ "$expected" = "$actual" ] || fail "SHA256 校验失败(期望 ${expected},实际 ${actual})"
  say "SHA256 校验通过"
fi

install -m 755 "$tmp" "$BIN"
say "已安装 ${BIN}"

# ---- 2. config (leave an existing file alone) ---------------------------
if [ -f "$ENV_FILE" ]; then
  say "保留已有配置 ${ENV_FILE}"
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
  say "已创建 ${ENV_FILE}(稍后填入 API Key)"
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
say "已写入服务单元 ${UNIT}"

if command -v systemctl >/dev/null 2>&1; then
  systemctl --user daemon-reload || fail "systemctl --user daemon-reload 执行失败"
  systemctl --user enable doubao-dictate >/dev/null 2>&1 || true
  systemctl --user restart doubao-dictate || say "警告:服务启动失败,请查看 systemctl --user status doubao-dictate"
  # The socket appears a moment after the restart; wait for it so a status
  # call straight after the install does not race the daemon.
  sock="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/doubao-dictate.sock"
  for _ in $(seq 1 20); do
    [ -S "$sock" ] && break
    sleep 0.1
  done
  service_state="$(systemctl --user is-active doubao-dictate 2>/dev/null || echo unknown)"
  case "$service_state" in
    active) service_state="运行中" ;;
    inactive) service_state="未运行" ;;
    failed) service_state="启动失败" ;;
    *) service_state="未知" ;;
  esac
  say "服务 doubao-dictate:${service_state}"
else
  say "警告:未找到 systemctl,请手动启动:doubao-dictate daemon"
fi

# ---- 4. Omarchy / Hyprland hotkey (append once, then reload) ------------
if [ -f "$HYPR_BINDINGS" ] && grep -qF "doubao-dictate toggle" "$HYPR_BINDINGS"; then
  say "热键绑定已存在,跳过"
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
  say "已把热键绑定写入 ${HYPR_BINDINGS}"
  if command -v hyprctl >/dev/null 2>&1; then
    hyprctl reload >/dev/null 2>&1 || true
    say "已重载 Hyprland"
  fi
fi

# ---- 5. API key ---------------------------------------------------------
key_ready=0
if grep -qE '^[[:space:]]*DOUBAO_API_KEY=.+' "$ENV_FILE" 2>/dev/null; then
  say "已配置 API Key,跳过设置"
  key_ready=1
else
  # stdin is the script itself when piped through curl, so the prompt must go
  # through the terminal. /dev/tty can exist yet be unusable (no controlling
  # terminal), and a plain write there would abort the script under set -e,
  # so probe it by actually opening it.
  have_tty=0
  if { exec 3<>/dev/tty; } 2>/dev/null; then have_tty=1; fi

  key=""
  if [ "$have_tty" -eq 1 ]; then
    say ""
    say "获取 API Key:"
    say "  1. 打开 https://console.volcengine.com/speech/"
    say "  2. 开通「流式语音识别」,然后在「应用管理」里创建一个 API Key"
    if command -v xdg-open >/dev/null 2>&1 &&
      { [ -n "${WAYLAND_DISPLAY:-}" ] || [ -n "${DISPLAY:-}" ]; }; then
      (xdg-open "https://console.volcengine.com/speech/" >/dev/null 2>&1 &) || true
      say "  (已在浏览器中打开该页面)"
    fi

    printf '粘贴你的豆包 API Key(直接回车跳过): ' >&3
    IFS= read -r -s key <&3 || key=""
    printf '\n' >&3
  else
    say ""
    say "当前没有终端,无法交互输入(无人值守安装)。"
    say "API Key 获取地址:https://console.volcengine.com/speech/"
  fi
  exec 3<&- 3>&- 2>/dev/null || true

  # API keys are UUID-shaped; drop anything that is not safe in a .env value.
  key="$(printf '%s' "$key" | tr -d '[:space:]' | tr -cd 'A-Za-z0-9._-')"
  if [ -n "$key" ]; then
    tmp_env="$(mktemp)"
    awk -v k="$key" '
      /^[[:space:]]*DOUBAO_API_KEY=/ { print "DOUBAO_API_KEY=" k; found = 1; next }
      { print }
      END { if (!found) print "DOUBAO_API_KEY=" k }
    ' "$ENV_FILE" > "$tmp_env"
    install -m 600 "$tmp_env" "$ENV_FILE"
    rm -f "$tmp_env"
    say "已保存 API Key 到 ${ENV_FILE}"
    systemctl --user restart doubao-dictate >/dev/null 2>&1 || true
    say "已重启 doubao-dictate"
    key_ready=1
  elif [ "$have_tty" -eq 1 ]; then
    say "已跳过 — 之后可以手动添加:\$EDITOR ${ENV_FILE}"
  fi
fi

say ""
if [ "$key_ready" -eq 1 ]; then
  say "完成。按 F9 开始/停止听写。"
else
  say "已安装,但还没有 API Key。填好后重启服务:"
  say "  \$EDITOR ${ENV_FILE}"
  say "  systemctl --user restart doubao-dictate"
fi
