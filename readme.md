# doubao-asr

把豆包(火山引擎)语音识别接入 Omarchy 语音听写的工具。

> 密钥只放在仓库根目录的 `.env`(已 gitignore),**不要提交到仓库**。

## Omarchy 语音输入接入(dictate.py)

`dictate.py` 把豆包 ASR 接入 Omarchy 的语音听写:按热键开始录音,说完再按一次,
识别结果用 wtype 打进当前焦点窗口。默认是**整段批量识别**(非流式),质量最好。
它**旁路 voxtype**,因此请保持 `voxtype.service` 为 disabled(默认就是),避免抢热键。

### 组件

| 位置 | 作用 |
| --- | --- |
| `~/.config/systemd/user/doubao-dictate.service` | 常驻守护进程 |
| `~/.local/bin/doubao-dictate` | CLI 包装脚本 |
| `.env`(与本文件同目录) | 配置(含 `DOUBAO_API_KEY`,已 gitignore) |
| `~/.config/hypr/bindings.lua` | Hyprland 热键绑定 |
| `~/.local/state/doubao-dictate/daemon.log` | 运行日志 |

### 热键

- `F9` 按一下开始录音,再按一下停止(纯切换,不依赖"松开"事件)
- `SUPER + CTRL + X`:切换开始/停止(同义)
- `SUPER + CTRL + ESC`:取消本次听写(丢弃)

> F9 采用纯 toggle,是因为在部分环境下 Hyprland 的"松键"绑定会偶发丢失,
> 导致长按(push-to-talk)有时停不下来。改成按一下开/关后不再依赖松开事件。
> 另有一层保险:即使忘了按停止,达到 `DOUBAO_MAX_DURATION_SECS`(默认 120s)
> 也会自动停止。

### 配置

配置是**与本文件同目录的 `.env`**(`KEY=VALUE`),模板见 `.env.example`。
真实环境变量优先于 `.env`;也可用 `DOUBAO_DICTATE_ENV=/path/to/file` 指定别的文件。
关键项:

- `DOUBAO_RECOGNIZE`:
  - `"batch"`(**默认,推荐**):整段录完再一次性发给豆包非流式端点,不做实时出字。
    识别质量最好,也不会出现流式拼接导致的**词汇重复/零碎**。
  - `"stream"`:边说边发、实时打字;延迟低但可能有流式伪影。
- `DOUBAO_URL` / `DOUBAO_BATCH_URL`:分别对应 `stream` / `batch` 模式用的端点
- `DOUBAO_ENABLE_NONSTREAM`:仅 `stream` 模式用于二遍精修
- `DOUBAO_MODE`:`type`(默认,wtype 打字)或 `clipboard`(wl-copy)
- `DOUBAO_FLUSH_DELAY_MS`:`stream` 模式的打字防抖,越大越不容易看到尾部回退
- `DOUBAO_DEVICE`:默认 `default`(跟随系统默认输入源),也可指定 pulse 源名
- `DOUBAO_SAVE_AUDIO`:把每次录音存成 WAV(`~/.local/state/doubao-dictate/sessions/`,留最近 20 个)

### 运维

```
systemctl --user restart doubao-dictate    # 改配置后重启
doubao-dictate status                      # 查看状态(idle/recording)
journalctl --user -u doubao-dictate -f     # 实时日志
```

### 离线测试 / 复现(不占用麦克风)

先用一次真实听写(会存下 WAV),再回放对比:

```
# 用保存下来的录音回放,比较 batch / stream 两种识别
f=$(ls -t ~/.local/state/doubao-dictate/sessions/*.wav | head -1)

.venv/bin/python dictate.py transcribe "$f" --print --fast            # 流式
.venv/bin/python dictate.py transcribe "$f" --print --fast --nonstream # 流式+二遍精修
DOUBAO_URL=wss://openspeech.bytedance.com/api/v3/sauc/bigmodel_nostream \
  .venv/bin/python dictate.py transcribe "$f" --print --fast           # 非流式(整段)
```

### 故障排查

- **没有输出**:确认系统存在麦克风输入源:`pactl list short sources`。
  若只有 `*.monitor`,说明当前没有麦克风设备,需要接入麦克风并把
  系统默认输入源切到它(或改 `audio.device`)。
- **识别重复/零碎**:改回 `audio.recognize = "batch"`(默认),用非流式端点整段识别。
- **服务起不来/无响应**:`systemctl --user status doubao-dictate`,看日志。
- **不打字**:确认守护进程环境里有 `WAYLAND_DISPLAY`
  (`systemctl --user show-environment | grep WAYLAND_DISPLAY`),以及 `wtype` 已安装。
- **热键冲突**:本工具已在 `bindings.lua` 里 `hl.unbind` 掉 voxtype 的
  `F9` / `SUPER+CTRL+X`,改动后执行 `hyprctl reload`。

### 工作原理

两种模式都由 `parecord` 采 16k 单声道 PCM:

- **batch**(默认):先把整段语音缓存到内存(可选存 WAV),你按停止后,
  把**完整音频**一次性发给豆包非流式端点 `bigmodel_nostream`,拿到唯一最终
  结果后用 `wtype` 打出。质量最好,没有流式拼接伪影。
- **stream**:每 200ms 一帧 gzip 后经 websocket 发给流式端点,服务器返回
  **累计文本**(尾部会被修订),客户端用公共前缀对账(必要时
  `wtype -k BackSpace` 回退)再 `wtype -` 打出增量文本。


