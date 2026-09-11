# doubao-asr

把豆包(火山引擎)语音识别接入 Linux 桌面(为 Omarchy / Hyprland 打造)的语音听写工具。

按热键开始录音,说完再按一次,识别结果用 `wtype` 直接打进当前焦点窗口。
整段录音先缓存在内存里,停止后**一次性**发给豆包**非流式**端点识别,因此没有
流式拼接导致的词汇重复/零碎问题。

实现是一个单文件 Go 二进制,不需要 Python / uv / venv,`systemd` 直接运行它。

> ⚠️ 密钥只放在 `~/.config/doubao-dictate/.env`(建议 `chmod 600`),**不要提交到仓库**。

## 一键安装(推荐)

不用装 Go,直接从 GitHub Release 下载预编译二进制并完成配置:

```bash
curl -fsSL https://raw.githubusercontent.com/geoochi/doubao-asr/main/install.sh | bash
```

脚本会(全部是用户级操作,**不需要 sudo**):

1. 按架构下载 `doubao-dictate`(linux amd64 / arm64)到 `~/.local/bin/`,并校验 SHA256;
2. 首次安装时创建 `~/.config/doubao-dictate/.env`(已存在则原样保留);
3. 写入并启动 systemd 用户服务 `doubao-dictate`;
4. 往 `~/.config/hypr/bindings.lua` 追加 `F9` 热键(已存在则跳过)并 `hyprctl reload`;
5. 打印控制台地址(有图形会话时顺手用浏览器打开),然后在**终端里等你粘贴 API Key**,
   写进 `.env`(0600)并自动重启服务。

可以重复执行(幂等),升级时再跑一次即可。`.env` 里已经有 Key 时第 5 步会直接跳过;
非交互环境(没有终端)会跳过询问并提示你手动填。

装完后按 `F9` 说一句、再按一次,文字就出来了。如果第 5 步跳过了,手动补:

```bash
$EDITOR ~/.config/doubao-dictate/.env     # 填 DOUBAO_API_KEY
systemctl --user restart doubao-dictate
```

拿到 Key 的方式见下面「获取 API Key」。

> 卸载:`systemctl --user disable --now doubao-dictate`,删除
> `~/.local/bin/doubao-dictate`、`~/.config/doubao-dictate/`、
> `~/.config/systemd/user/doubao-dictate.service`,以及 `bindings.lua` 里那段
> `doubao-dictate (managed by install.sh)` 的绑定,最后 `hyprctl reload`。

## 从源码安装

想自己编译、或不想用上面的脚本时,按下面的步骤来。


### 0. 前置依赖

| 用途 | 依赖 |
| --- | --- |
| 录音 | `parecord`(pipewire-pulse,Omarchy 自带) |
| 打字输出 | `wtype` |
| 桌面通知(可选) | `notify-send`(libnotify) |
| 编译 | Go ≥ 1.23 |
| 热键 / 开机自启 | Hyprland + systemd 用户会话 |

Omarchy / Arch 下:

```bash
omarchy-pkg-add wtype libnotify        # 或 sudo pacman -S wtype libnotify
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
mkdir -p ~/.config/doubao-dictate
cp .env.example ~/.config/doubao-dictate/.env
$EDITOR ~/.config/doubao-dictate/.env      # 填 DOUBAO_API_KEY=<你的 key>
chmod 600 ~/.config/doubao-dictate/.env
```

配置查找顺序:`$DOUBAO_DICTATE_ENV` → `~/.config/doubao-dictate/.env` → `./.env`;
真实的同环境变量优先于文件。完整说明见 `.env.example`。关键项:

- `DOUBAO_URL`:非流式端点,默认 `…/bigmodel_nostream`(别用 `bigmodel`)
- `DOUBAO_DEVICE`:默认 `default`(跟随系统默认输入源),也可写具体 pulse 源名
- `DOUBAO_MODE`:`type`(默认,wtype 打字)/ `clipboard`(wl-copy)

### 4. 编译安装

```bash
go build -o doubao-dictate .
install -m755 doubao-dictate ~/.local/bin/
```

没有 Go 的话:`mise use -g go@latest`(或从 https://go.dev/dl/ 安装)。
国内拉模块若卡在 `proxy.golang.org`,用镜像:`GOPROXY=https://goproxy.cn,direct go build -o doubao-dictate .`

### 5. 让后端常驻后台(systemd 用户服务)

```bash
mkdir -p ~/.config/systemd/user
cp deploy/doubao-dictate.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now doubao-dictate

# 检查
doubao-dictate status            # 期望: {"ok":true,"state":"idle"}
systemctl --user is-active doubao-dictate
```

这是个**用户级**服务:登录图形会话后自动启动,退出登录时结束,不需要 root。
改了 `.env` 后执行 `systemctl --user restart doubao-dictate` 生效;升级二进制后
执行 `systemctl --user restart doubao-dictate` 即可。

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

按 `F9` → 说一句 → 再按 `F9`,文字应出现在光标处(停止后会有一小段识别等待)。

没反应时依次排查:

```bash
pactl list short sources                    # 有没有麦克风输入源
journalctl --user -u doubao-dictate -f      # 服务日志
doubao-dictate status                       # idle / recording
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
systemctl --user restart doubao-dictate    # 改配置 / 升级二进制后重启
systemctl --user status  doubao-dictate    # 服务状态
doubao-dictate status                      # idle / recording
journalctl --user -u doubao-dictate -f     # 实时日志
```

调试日志:给服务加 `DOUBAO_LOG_LEVEL=debug` 环境变量(或直接手动运行
`DOUBAO_LOG_LEVEL=debug doubao-dictate daemon`)可看到更详细输出。

## 组件

| 位置 | 作用 |
| --- | --- |
| 仓库内的 `*.go` / `go.mod` | 源码(编译成单个二进制) |
| `~/.local/bin/doubao-dictate` | 编译产物 |
| `~/.config/doubao-dictate/.env` | 配置(含 `DOUBAO_API_KEY`) |
| `~/.config/systemd/user/doubao-dictate.service` | 常驻守护进程 |
| `~/.config/hypr/bindings.lua` | Hyprland 热键绑定 |

## 配置(完整)

`.env`(`KEY=VALUE`),模板见 `.env.example`。真实环境变量优先于文件;
也可用 `DOUBAO_DICTATE_ENV=/path/to/file` 指定别的文件。

- `DOUBAO_API_KEY`:API Key(必填)
- `DOUBAO_RESOURCE_ID`:资源 ID,默认 `volc.seedasr.sauc.duration`
- `DOUBAO_URL`:识别端点,默认 `wss://openspeech.bytedance.com/api/v3/sauc/bigmodel_nostream`
- `DOUBAO_DEVICE` / `DOUBAO_SAMPLE_RATE` / `DOUBAO_SEGMENT_MS`:录音参数
- `DOUBAO_MAX_DURATION_SECS`:单次录音上限(默认 120)
- `DOUBAO_MODE`:`type`(wtype 打字)/ `clipboard`(wl-copy)
- `DOUBAO_NOTIFY`:桌面通知开关(默认 true)

## 故障排查

- **没有输出**:确认系统存在麦克风输入源:`pactl list short sources`。
  若只有 `*.monitor`,说明当前没有麦克风设备,需要接入麦克风并把
  系统默认输入源切到它(或改 `DOUBAO_DEVICE`)。
- **提示 ASR 错误 401/403**:API Key 不对或服务未开通,检查
  `~/.config/doubao-dictate/.env` 里的 `DOUBAO_API_KEY`。
- **识别重复/零碎**:确认 `DOUBAO_URL` 用的是 `…/bigmodel_nostream`(非流式整段识别)。
- **服务起不来/无响应**:`systemctl --user status doubao-dictate` 看日志。
- **不打字**:确认守护进程环境里有 `WAYLAND_DISPLAY`
  (`systemctl --user show-environment | grep WAYLAND_DISPLAY`),以及 `wtype` 已安装。
- **命令连不上 daemon**:`doubao-dictate status` 若报 `daemon not running`,
  检查服务是否在跑;socket 在 `$XDG_RUNTIME_DIR/doubao-dictate.sock`。
- **热键冲突**:本工具在 `bindings.lua` 里 `hl.unbind` 掉 voxtype 的
  `F9` / `SUPER+CTRL+X`;改动后执行 `hyprctl reload`。

## 工作原理

1. 收到 `start`/`toggle`:用 `parecord` 采 16k 单声道 PCM,按 200ms 一块缓存到内存,
   同时把状态写成 `recording`。
2. 收到 `stop`(或达到时长上限):结束录音。
3. 用 websocket 连 `DOUBAO_URL`(非流式端点),带 `X-Api-Key` 等鉴权头;
   先发一帧会话配置(JSON + gzip),再把整段 PCM 分块发送,最后一帧用负序号
   标记结束。
4. 读取服务器的最终包,取出 `result.text`,用 `wtype -` 打进焦点窗口
   (`DOUBAO_MODE=clipboard` 时改用 `wl-copy`)。
5. 状态回到 `idle`。

守护进程只通过 `$XDG_RUNTIME_DIR/doubao-dictate.sock` 上的简单文本命令控制
(`start`/`stop`/`toggle`/`cancel`/`status`),Hyprland 绑定就是用 `doubao-dictate`
这个客户端去发命令。
