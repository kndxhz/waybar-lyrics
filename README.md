# waybar-lyrics

一个用于 Waybar 的自定义模块，可显示播放器同步歌词。歌词来源按 AMLL TTML 歌词库、网易云音乐、LRCLib 的顺序检索。

## 功能特性

- **三种播放器模式**：普通 MPRIS、旧版 SPlayer（MPRIS）和 SPlayer-Next（HTTP API）彼此独立。
- **SPlayer-Next 歌词优先**：切歌后先等待播放器 API 加载歌词，避免过早回退到外部歌词源；API 一直不可用时才读取网易云歌曲 ID，必要时按标题和歌手搜索。若外部歌词先加载、API 歌词随后才就绪，会自动切回播放器歌词。
- **歌词来源**：AMLL TTML DB 优先，逐词 TTML 会转换为逐行显示；未命中后依次使用网易云音乐和 LRCLib。
- **来源提示**：tooltip 在“歌手 - 歌名”后显示歌词来源，取值为 `API`（播放器 API）、`TTML`（AMLL TTML DB）、`NCM`（网易云音乐）、`LRCLib`（LRCLib）。未解析到歌词时不显示来源。
- **双语支持**：支持显示原文歌词与翻译歌词（如有）。
- **Waybar 兼容**：输出符合 Waybar custom 模块要求的 JSON 格式。
- **控制能力**：支持通过 Waybar 点击事件控制播放/暂停、上一首、下一首。

## 安装

### Arch Linux（AUR）

可从 AUR 安装（包文件已准备，上传后可使用）：

```bash
yay -S waybar-lyrics-git
```

### 手动安装

1. **依赖项**：
   - Go（用于构建）
   - playerctl（可选，用于 Waybar 点击控制命令）

2. **构建**：

   ```bash
   git clone https://github.com/kndxhz/waybar-lyrics.git
   cd waybar-lyrics
   go build -o waybar-lyrics
   ```

3. **安装**：

   ```bash
   sudo install -Dm755 waybar-lyrics /usr/bin/waybar-lyrics
   ```

## 播放器模式与配置

配置文件路径为 `$XDG_CONFIG_HOME/waybar-lyrics/waybar-lyrics.yaml`；未设置 `XDG_CONFIG_HOME` 时使用 `$HOME/.config/waybar-lyrics/waybar-lyrics.yaml`。首次启动时会自动创建配置目录和默认配置文件（默认模式为 `mpris`）。

可通过命令行参数临时覆盖 YAML 中的播放器模式，优先级高于配置文件且不会修改配置文件：

```bash
waybar-lyrics --mode splayer-next
# 简写
waybar-lyrics -m splayer
# 查看帮助
waybar-lyrics --help
```

`--mode`/`-m` 可选值为 `mpris`、`splayer`、`splayer-next`。未传入参数时使用 YAML 配置中的 `mode`。

```yaml
# 可选值：mpris、splayer、splayer-next
mode: splayer-next

splayer:
  # SPlayer-Next 外部 API 地址和端口
  address: 127.0.0.1
  port: 14558
  # 旧版 SPlayer 的本地 ControlAPI 端口（mode: splayer 时用于读取歌曲 ID）
  control_port: 25884
  # auto：优先使用播放器提供的网易云歌曲 ID；search：始终按标题和歌手搜索
  song_id_mode: auto
```

### `mpris`：普通 MPRIS 播放器

通过桌面会话的 D-Bus MPRIS 接口读取播放器元数据，自动选择一个非 SPlayer 的 MPRIS 播放器。适用于 Spotify、VLC、mpv 等兼容 MPRIS 的播放器。此模式不会连接 SPlayer-Next HTTP API，也不会选择名称中包含 `splayer` 的 MPRIS 服务。

### `splayer`：旧版 SPlayer

通过 D-Bus MPRIS 读取旧版 SPlayer 元数据，只选择名称中包含 `splayer` 的 MPRIS 服务，不会回退到其他播放器。旧版 SPlayer 由 Electron 暴露 MPRIS，`mpris:trackid` 不一定是网易云歌曲 ID，因此会额外请求其本地 ControlAPI `GET /api/control/song-info` 获取当前歌曲 ID（默认端口 `25884`，可用 `control_port` 修改）。ControlAPI 不可用或返回非网易云歌曲时才退回 MPRIS 元数据。此模式不调用 SPlayer-Next HTTP API。

### `splayer-next`：SPlayer-Next

通过本机 HTTP API 读取当前曲目信息和播放进度，并优先使用 API 返回的同步歌词。由于播放进度经 HTTP 返回时会比实际播放略慢，此模式下的歌词会提前 100ms 显示，以抵消网络延迟。切歌后会先等待最多 2 秒，让 SPlayer-Next 加载自己的歌词；等待期结束仍无歌词时才回退到外部歌词源。如果外部歌词已经先加载、API 歌词随后才就绪，会自动切换回 API 歌词，使显示内容与 SPlayer-Next 保持一致。使用 API 歌词时，切歌 5 秒后还会再读取一次，以同步 SPlayer-Next 后续加载的更高质量歌词（尤其是翻译）。API 无可用歌词时，继续使用播放器提供的网易云歌曲 ID 或按标题和歌手搜索。

SPlayer-Next 外部 API 默认关闭，需要先在 SPlayer-Next 的“设置 → 外部 API”中开启。默认监听地址为 `127.0.0.1:14558`。配置示例：

```yaml
mode: splayer-next
splayer:
  address: 127.0.0.1
  port: 14558
  song_id_mode: auto
```

地址和端口也支持写在 `splayer.api` 下：

```yaml
mode: splayer-next
splayer:
  song_id_mode: auto
  api:
    address: 127.0.0.1
    port: 14558
```

### 歌曲识别与歌词回退

`song_id_mode` 对三种模式都生效：

- `auto`：尝试从播放器提供的曲目 ID 中提取网易云歌曲 ID；`splayer` 模式会先向旧版 SPlayer 的 ControlAPI 查询当前歌曲 ID。无法提取时，按标题和歌手搜索网易云歌曲。默认值为 `auto`。
- `search`：忽略曲目 ID，始终按标题和歌手搜索网易云歌曲（`splayer` 模式下也不会请求 ControlAPI）。

得到网易云歌曲 ID 后，依次尝试 AMLL TTML DB、网易云音乐歌词和 LRCLib。TTML 中的逐词时间会合并为逐行歌词；SPlayer-Next API 自带的歌词则在这些歌词库之前直接使用。

## Waybar 配置

将以下模块添加到 Waybar 配置（或 modules.json）：

```json
"custom/lyrics": {
    "exec": "waybar-lyrics 2> /dev/null",
    "return-type": "json",
    "format": "{}",
    "on-click": "playerctl play-pause",
    "on-click-right": "playerctl next",
    "on-click-middle": "playerctl previous",
    "tooltip": true,
    "justify": "center"
},
```

然后把 `custom/lyrics` 加入 `modules-left`、`modules-center` 或 `modules-right` 列表。

## 使用说明

- **左键**：播放/暂停
- **右键**：下一首
- **中键**：上一首

当没有活跃播放器或播放器停止时，模块会自动隐藏。

## 许可证

GPL License
