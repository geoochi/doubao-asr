# doubao-asr

把豆包(火山引擎)语音识别接入 Linux 桌面(为 Omarchy / Hyprland 打造)的语音听写工具。

按热键开始录音,说完再按一次,识别结果用 `wtype` 直接打进当前焦点窗口。
默认走**整段批量识别**(非流式),质量最好;也支持边说边出的流式模式。

> ⚠️ 密钥只放在仓库根目录的 `.env`(已 gitignore,建议 `chmod 600`),**不要提交到仓库**。

## 第一次使用(从零安装)

### 0. 前置依赖

| 用途 | 依赖 |
| --- | --- |
| 录音 | `parecord`(pipewire-pulse,Omarchy 自带) |
| 打字输出 | `wtype` |
| 桌面通知(可选) | `notify-send`(libnotify) |
| 离线转写任意音频(可选) | `ffmpeg` |
| 运行 | Python ≥ 3.12、[uv](https://docs.astral.sh/uv/) |
| 热键 / 开机自启 | Hyprland + systemd 用户会话 |

Omarchy / Arch 下:

```bash
omarchy-pkg-add wtype libnotify ffmpeg     # 或 sudo pacman -S wtype libnotify ffmpeg
```

### 1. 克隆仓库

```bash
git clone https://github.com/geoochi/doubao-asr.git
cd doubao-asr
```

### 2. 获取 API Key

1. 打开豆包语音 → [服务管理](https://console.volcengine.com/speech/new/setting/activate?ResourceID=volc.service_type.10074&projectName=default)
2. 开通「流式语音识别(2.0)」。
3. 进入左下角的「API key」新建一个 **API Key**。
4. 资源 ID(`Resource ID`)用默认的 `volc.seedasr.sauc.duration` 即可(新版控制台鉴权)。

> 本工具用的是新版控制台的 **X-Api-Key** 鉴权,只需要 API Key + Resource ID,
> 不需要老版的 App ID / Access Token。

### 3. 写配置

```bash
cp .env.example .env
$EDITOR .env          # 填 DOUBAO_API_KEY=<你的 key>
chmod 600 .env
```

其余项一般不用改,完整说明见 `.env.example`。关键项:

- `DOUBAO_RECOGNIZE`:`batch`(默认,整段识别,质量最好)/ `stream`(实时出字)
- `DOUBAO_DEVICE`:默认 `default`(跟随系统默认输入源),也可写具体 pulse 源名
- `DOUBAO_MODE`:`type`(默认,wtype 打字)/ `clipboard`(wl-copy)

### 4. 安装 Python 依赖

```bash
uv sync        # 创建 .venv 并安装 aiohttp、loguru
```

没有 uv 的话:`curl -LsSf https://astral.sh/uv/install.sh | sh`

### 5. 让后端常驻后台(systemd 用户服务)

**在仓库根目录执行**(下面用 `$PWD` 记下当前路径):

```bash
# 1) 生成 CLI 包装脚本
mkdir -p ~/.local/bin
cat > ~/.local/bin/doubao-dictate <<EOF
#!/bin/bash
exec "$PWD/.venv/bin/python" "$PWD/dictate.py" "\$@"
EOF
chmod +x ~/.local/bin/doubao-dictate

# 2) 安装并启动用户服务
mkdir -p ~/.config/systemd/user
cp deploy/doubao-dictate.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now doubao-dictate

# 3) 检查
doubao-dictate status            # 期望: {"ok": true, "state": "idle"}
systemctl --user is-active doubao-dictate
```

这是个**用户级**服务:登录图形会话后自动启动,退出登录时结束,不需要 root。
改了 `.env` 后执行 `systemctl --user restart doubao-dictate` 生效。

> 如果以后把仓库目录挪走,记得重新生成第 1 步的包装脚本。

### 6. 绑定热键(Hyprland)

```bash
cat deploy/hyprland-bindings.lua >> ~/.config/hypr/bindings.lua
hyprctl reload
```

该片段会先 `hl.unbind` 掉 Omarchy 默认给 voxtype 的 `F9` / `SUPER+CTRL+X`,再绑到本工具。
请保持 `voxtype.service` 为 disabled(默认就是),避免抢热键:

```bash
systemctl --user is-enabled voxtype.service    # 期望: disabled
```

> 如果你已经手动加过这几条绑定,就跳过本步(重复追加会出现重复绑定)。

### 7. 试一下

按 `F9` → 说一句 → 再按 `F9`,文字应出现在光标处。

没反应时依次排查:

```bash
pactl list short sources                    # 有没有麦克风输入源
journalctl --user -u doubao-dictate -f      # 服务日志
doubao-dictate status                        # idle / recording
```

## 日常使用

### 热键

| 键 | 行为 |
| --- | --- |
| `F9` | 按一下开始录音,再按一下停止 |
| `SUPER + CTRL + X` | 同上(切换) |
| `SUPER + CTRL + ESC` | 取消本次听写(丢弃) |

> `F9` 用纯 toggle(而不是长按 push-to-talk),因为在部分环境下 Hyprland 的"松键"
> 绑定会偶发丢失,导致长按停不下来。另有一层保险:即使忘了按停止,达到
> `DOUBAO_MAX_DURATION_SECS`(默认 120s)也会自动停止。

### 运维

```bash
systemctl --user restart doubao-dictate    # 改配置后重启
systemctl --user status  doubao-dictate    # 服务状态
doubao-dictate status                      # idle / recording
journalctl --user -u doubao-dictate -f     # 实时日志
```

## 组件

| 位置 | 作用 |
| --- | --- |
| 仓库内的 `dictate.py` / `protocol.py` | 主程序 |
| `.env`(仓库根目录) | 配置(含 `DOUBAO_API_KEY`,已 gitignore) |
| `~/.local/bin/doubao-dictate` | CLI 包装脚本 |
| `~/.config/systemd/user/doubao-dictate.service` | 常驻守护进程 |
| `~/.config/hypr/bindings.lua` | Hyprland 热键绑定 |
| `~/.local/state/doubao-dictate/daemon.log` | 运行日志 |

## 配置(完整)

配置是仓库根目录的 `.env`(`KEY=VALUE`),模板见 `.env.example`。
真实环境变量优先于 `.env`;也可用 `DOUBAO_DICTATE_ENV=/path/to/file` 指定别的文件。

- `DOUBAO_API_KEY`:API Key(必填)
- `DOUBAO_RESOURCE_ID`:资源 ID,默认 `volc.seedasr.sauc.duration`
- `DOUBAO_URL` / `DOUBAO_BATCH_URL`:分别对应 `stream` / `batch` 模式的端点
- `DOUBAO_RECOGNIZE`:`batch`(默认)/ `stream`
- `DOUBAO_ENABLE_NONSTREAM`:仅 `stream` 模式的二遍精修
- `DOUBAO_DEVICE` / `DOUBAO_SAMPLE_RATE` / `DOUBAO_SEGMENT_MS`:录音参数
- `DOUBAO_MAX_DURATION_SECS`:单次录音上限(默认 120)
- `DOUBAO_MODE`:`type` / `clipboard`
- `DOUBAO_STREAM_TYPING` / `DOUBAO_FLUSH_DELAY_MS`:仅 `stream` 模式的打字行为
- `DOUBAO_NOTIFY` / `DOUBAO_SOUND`:桌面通知 / 提示音
- `DOUBAO_SAVE_AUDIO`:把每次录音存成 WAV(`~/.local/state/doubao-dictate/sessions/`,留最近 20 个);默认 `false`

## 离线测试 / 复现(不占用麦克风)

先在 `.env` 里设 `DOUBAO_SAVE_AUDIO=true` 并 `systemctl --user restart doubao-dictate`,
做一次真实听写让它存下 WAV,再回放对比:

```bash
# 用保存下来的录音回放,比较 batch / stream 两种识别
f=$(ls -t ~/.local/state/doubao-dictate/sessions/*.wav | head -1)

.venv/bin/python dictate.py transcribe "$f" --print --fast             # 流式
.venv/bin/python dictate.py transcribe "$f" --print --fast --nonstream # 流式 + 二遍精修
DOUBAO_URL=wss://openspeech.bytedance.com/api/v3/sauc/bigmodel_nostream \
  .venv/bin/python dictate.py transcribe "$f" --print --fast           # 非流式(整段)
```

(用完记得把 `DOUBAO_SAVE_AUDIO` 改回 `false`。)

## 故障排查

- **没有输出**:确认系统存在麦克风输入源:`pactl list short sources`。
  若只有 `*.monitor`,说明当前没有麦克风设备,需要接入麦克风并把
  系统默认输入源切到它(或改 `DOUBAO_DEVICE`)。
- **提示 ASR 错误 401/403**:API Key 不对或服务未开通,检查 `.env` 里的 `DOUBAO_API_KEY`。
- **识别重复/零碎**:确认 `DOUBAO_RECOGNIZE=batch`(默认),用非流式端点整段识别。
- **服务起不来/无响应**:`systemctl --user status doubao-dictate` 看日志。
- **不打字**:确认守护进程环境里有 `WAYLAND_DISPLAY`
  (`systemctl --user show-environment | grep WAYLAND_DISPLAY`),以及 `wtype` 已安装。
- **热键冲突**:本工具在 `bindings.lua` 里 `hl.unbind` 掉 voxtype 的
  `F9` / `SUPER+CTRL+X`;改动后执行 `hyprctl reload`。

## 工作原理

两种模式都由 `parecord` 采 16k 单声道 PCM:

- **batch**(默认):先把整段语音缓存到内存(可选存 WAV),你按停止后,
  把**完整音频**一次性发给豆包非流式端点 `bigmodel_nostream`,拿到唯一最终
  结果后用 `wtype` 打出。质量最好,没有流式拼接伪影。
- **stream**:每 200ms 一帧 gzip 后经 websocket 发给流式端点,服务器返回
  **累计文本**(尾部会被修订),客户端用公共前缀对账(必要时
  `wtype -k BackSpace` 回退)再 `wtype -` 打出增量文本。
