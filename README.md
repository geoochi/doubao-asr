# omarchy-doubao-asr

在 Omarchy / Hyprland 上用**豆包(火山引擎)语音识别**做语音听写:按一下热键开始说话,
再按一下停止,识别结果用 `wtype` **直接打进当前光标位置**。

- **一条命令安装**,全程用户级,不需要 `sudo`,不需要装 Python 或 Go
- **单文件 Go 二进制**,常驻内存只有几 MB
- **整段识别**(非流式):停止后才把完整音频发给豆包,不会出现流式识别那种
  词汇重复 / 句子被截断的问题
- 支持 linux **amd64 / arm64**

## 安装

```bash
curl -fsSL https://raw.githubusercontent.com/geoochi/omarchy-doubao-asr/main/install.sh | bash
```

脚本会做这些事:

1. 按架构下载 `doubao-dictate` 到 `~/.local/bin/`,并校验 SHA256;
2. 首次安装时创建配置 `~/.config/doubao-dictate/.env`(已存在则原样保留);
3. 安装并启动 systemd **用户服务** `doubao-dictate`(登录图形会话时自动运行);
4. 往 `~/.config/hypr/bindings.lua` 追加热键绑定并 `hyprctl reload`;
5. 打印取 Key 的页面(有图形会话时会顺手用浏览器打开),然后在**终端里等你粘贴 API Key**,
   写进 `.env`(权限 0600)并自动重启服务。

装完后按 `F9` 说一句、再按一次,文字就出来了。

> 脚本可以重复执行(幂等):`.env` 里已有 Key 时会跳过第 5 步;升级也只要重跑一次。

## 获取 API Key

1. 打开豆包语音 → [服务管理](https://console.volcengine.com/speech/new/setting/activate?ResourceID=volc.service_type.10074&projectName=default)
2. 开通「流式语音识别(2.0)」。
3. 进入左下角的「API key」新建一个 **API Key**。
4. 资源 ID(`Resource ID`)用默认的 `volc.seedasr.sauc.duration` 即可(新版控制台鉴权)。

> 本工具用的是新版控制台的 **X-Api-Key** 鉴权,只需要 API Key + Resource ID,
> 不需要老版的 App ID / Access Token。

如果安装时跳过了这一步,之后可以手动填:

```bash
$EDITOR ~/.config/doubao-dictate/.env     # 填 DOUBAO_API_KEY=<你的 key>
systemctl --user restart doubao-dictate
```

## 使用

| 热键 | 行为 |
| --- | --- |
| `F9` | 按一下开始录音,再按一下停止并开始识别 |
| `SUPER + CTRL + X` | 同上(等同按 `F9`) |
| `SUPER + CTRL + ESC` | 取消本次听写(丢弃,不出字) |

录音中右上角会弹出 `🎤 Recording` 通知,停止后弹出 `Transcribing…`,识别完自动打字。

> 用的是**按一下开 / 按一下关**,而不是长按说话。因为在部分环境下 Hyprland 的"松键"
> 事件会偶发丢失,长按会停不下来。另有一层保险:即使忘了按停止,达到
> `DOUBAO_MAX_DURATION_SECS`(默认 120 秒)也会自动停止。

## 配置

配置文件在 `~/.config/doubao-dictate/.env`(`KEY=VALUE` 格式),模板见仓库里的 `.env.example`。
查找顺序:`$DOUBAO_DICTATE_ENV` → `~/.config/doubao-dictate/.env` → 当前目录 `.env`;
真实的同名环境变量优先于文件内容。

| 变量 | 说明 |
| --- | --- |
| `DOUBAO_API_KEY` | **必填**,豆包 API Key |
| `DOUBAO_RESOURCE_ID` | 资源 ID,默认 `volc.seedasr.sauc.duration` |
| `DOUBAO_URL` | 识别端点,默认非流式的 `…/bigmodel_nostream`(**别改成 `bigmodel`**) |
| `DOUBAO_DEVICE` | 输入设备,默认 `default`(跟随系统默认输入源),也可写具体 pulse 源名 |
| `DOUBAO_SAMPLE_RATE` / `DOUBAO_SEGMENT_MS` | 录音参数,一般不用动 |
| `DOUBAO_MAX_DURATION_SECS` | 单次录音上限,默认 120 |
| `DOUBAO_MODE` | `type`(默认,用 wtype 打字)/ `clipboard`(用 wl-copy 复制) |
| `DOUBAO_NOTIFY` | 桌面通知开关,默认 `true` |

改完配置要重启服务生效:

```bash
systemctl --user restart doubao-dictate
```

## 升级 / 卸载

**升级**:重跑一遍安装命令即可(会替换二进制、保留你的配置和 Key)。

```bash
curl -fsSL https://raw.githubusercontent.com/geoochi/omarchy-doubao-asr/main/install.sh | bash
```

**卸载**:

```bash
systemctl --user disable --now doubao-dictate
rm -f  ~/.local/bin/doubao-dictate
rm -f  ~/.config/systemd/user/doubao-dictate.service
rm -rf ~/.config/doubao-dictate
systemctl --user daemon-reload
# 再删掉 ~/.config/hypr/bindings.lua 里那段以
#   -- doubao-dictate (managed by install.sh)
# 开头的绑定,然后:
hyprctl reload
```

## 常见问题

- **按 F9 没反应 / 没有通知**
  先看服务在不在:`systemctl --user is-active doubao-dictate`,
  再看实时日志:`journalctl --user -u doubao-dictate -f`。
- **没有输出**
  确认有麦克风输入源:`pactl list short sources`。若只有 `*.monitor`,
  说明当前没有麦克风设备,需要接入麦克风并把系统默认输入源切到它
  (或改 `DOUBAO_DEVICE`)。
- **提示 ASR 错误 401 / 403**
  API Key 不对,或控制台里还没开通「流式语音识别」。检查 `.env` 里的 `DOUBAO_API_KEY`。
- **不打字**
  确认守护进程环境里有 `WAYLAND_DISPLAY`
  (`systemctl --user show-environment | grep WAYLAND_DISPLAY`),以及 `wtype` 已安装。
- **识别重复 / 句子被截断**
  检查 `DOUBAO_URL` 是 `…/bigmodel_nostream`(非流式整段识别)。
- **命令连不上守护进程**
  `doubao-dictate status` 报 `daemon not running`,说明服务没在跑;
  socket 位于 `$XDG_RUNTIME_DIR/doubao-dictate.sock`。
- **热键和别的软件冲突**
  本工具会 `hl.unbind` 掉 Omarchy 默认给 voxtype 的 `F9` / `SUPER+CTRL+X`。
  建议保持 `voxtype.service` 为 disabled(默认就是),避免抢热键。
- **想看详细日志**
  给服务加 `DOUBAO_LOG_LEVEL=debug`,或手动运行
  `DOUBAO_LOG_LEVEL=debug doubao-dictate daemon`。

## 工作原理

1. 收到 `toggle`:用 `parecord` 采 16kHz 单声道 PCM,按 200ms 一块缓存到内存,
   状态写成 `recording`;
2. 再次收到 `toggle`(或到达时长上限):结束录音;
3. 用 WebSocket 连 `DOUBAO_URL`,带 `X-Api-Key` 等鉴权头;先发一帧会话配置(JSON + gzip),
   再把整段 PCM 分块发出去,最后一帧用负序号标记结束;
4. 读服务器返回的最终包,取出 `result.text`,用 `wtype -` 打进焦点窗口
   (`DOUBAO_MODE=clipboard` 时改用 `wl-copy`);
5. 状态回到 `idle`。

守护进程只通过 `$XDG_RUNTIME_DIR/doubao-dictate.sock` 上的简单文本命令控制
(`start` / `stop` / `toggle` / `cancel` / `status`),Hyprland 的热键绑定就是用
`doubao-dictate` 这个客户端去发命令。

装好的东西:

| 位置 | 作用 |
| --- | --- |
| `~/.local/bin/doubao-dictate` | 主程序(单文件二进制) |
| `~/.config/doubao-dictate/.env` | 配置(含 API Key,权限 600) |
| `~/.config/systemd/user/doubao-dictate.service` | 常驻守护进程 |
| `~/.config/hypr/bindings.lua` | 热键绑定 |

## 从源码构建(开发用)

普通用户用上面的安装脚本就够了,下面这些只是给想自己编译的人。

需要 `parecord`(pipewire-pulse)、`wtype`、可选的 `notify-send`(libnotify)、
`Go ≥ 1.23`、Hyprland + systemd 用户会话。Omarchy / Arch:

```bash
omarchy-pkg-add wtype libnotify        # 或 sudo pacman -S wtype libnotify
git clone https://github.com/geoochi/omarchy-doubao-asr.git
cd omarchy-doubao-asr
go build -o doubao-dictate .
install -m755 doubao-dictate ~/.local/bin/
```

没有 Go 的话:`mise use -g go@latest`(或从 https://go.dev/dl/ 安装)。
国内拉模块若卡在 `proxy.golang.org`,用镜像:
`GOPROXY=https://goproxy.cn,direct go build -o doubao-dictate .`

然后手动落地服务和热键(仓库里有两份模板):

```bash
mkdir -p ~/.config/systemd/user
cp deploy/doubao-dictate.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now doubao-dictate

# 配置文件
mkdir -p ~/.config/doubao-dictate
cp .env.example ~/.config/doubao-dictate/.env
$EDITOR ~/.config/doubao-dictate/.env
chmod 600 ~/.config/doubao-dictate/.env
systemctl --user restart doubao-dictate

# 热键(会先解绑 voxtype 占用的 F9 / SUPER+CTRL+X)
cat deploy/hyprland-bindings.lua >> ~/.config/hypr/bindings.lua
hyprctl reload
```

跑一遍测试和静态检查:

```bash
go vet ./...
go test ./...
```

发版:打 `v*` tag 后由 `.github/workflows/release.yml` 自动编译双架构二进制并附加到 Release。
