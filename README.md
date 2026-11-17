# waybar-lyrics

为 Waybar 显示当前播放器同步歌词的自定义模块。

歌词依次取自播放器 API、AMLL TTML 歌词库、网易云音乐和 LRCLib。

## 安装

### Arch Linux（AUR）

```bash
yay -S waybar-lyrics
```

包中二进制安装在 `/usr/bin/waybar-lyrics`，可直接用 `waybar-lyrics` 调用。

### 预编译二进制

从 [Releases](https://github.com/kndxhz/waybar-lyrics/releases) 下载对应架构的静态二进制，安装到 PATH 中的目录：

```bash
chmod +x waybar-lyrics-linux-amd64
sudo install -Dm755 waybar-lyrics-linux-amd64 /usr/local/bin/waybar-lyrics
```

### 手动编译

需要 Go。`mpris` / `splayer` 模式的播放控制需要安装 `playerctl`；`splayer-next` 模式只使用 HTTP API，不依赖 `playerctl`。仅显示歌词不需要 `playerctl`。

```bash
git clone https://github.com/kndxhz/waybar-lyrics.git
cd waybar-lyrics
go build -o waybar-lyrics
sudo install -Dm755 waybar-lyrics /usr/bin/waybar-lyrics
```

安装完成后可运行 `waybar-lyrics --version`（或 `-V`）确认版本。

## 配置

配置文件为 `~/.config/waybar-lyrics/waybar-lyrics.yaml`，首次运行自动生成：

```yaml
mode: splayer-next   # mpris、splayer 或 splayer-next
mpris-fallback: true # SPlayer 模式读不到歌曲时回退到 MPRIS；mpris 模式下忽略该项
splayer:
  address: 127.0.0.1 # SPlayer-Next API 地址
  port: 14558        # SPlayer-Next API 端口
  song_id_mode: auto # auto：优先使用播放器歌曲 ID；search：按歌名歌手搜索
  latency_ms: 100    # 歌词时间补偿（毫秒）：正数提前，负数延后，0 关闭
log:
  path: /tmp/waybar-lyrics.log
  level: INFO        # DEBUG、INFO、WARN 或 ERROR
```

`--mode`/`-m` 可临时覆盖配置文件中的模式。

`mpris-fallback` 为顶层布尔值，默认 `true`。旧配置文件缺少该项时会自动补写；`mode` 为 `mpris` 时该项无意义，程序启动时会记一条 `WARN` 日志说明已忽略。

日志默认写入 `/tmp/waybar-lyrics.log`，每次启动时清空；仅 `DEBUG` 级别记录完整歌词。

### 播放器模式

- `mpris`：普通 MPRIS 播放器（Spotify、VLC、mpv 等）。播放器没有网易云歌曲 ID，按歌名和歌手搜索。
- `splayer`：旧版 SPlayer，歌曲 ID 取自 MPRIS。
- `splayer-next`：通过 SPlayer-Next 外部 API 读取曲目和歌词，优先显示播放器提供的歌词，并补偿网络延迟。需在 SPlayer-Next 设置中开启外部 API。

#### `mpris` 模式下的播放器适配

同一时刻可能有多台播放器注册 MPRIS，浏览器即使没有播放也会常驻注册（`Metadata` 里只有时长），而且 `ListNames` 的返回顺序并不代表谁在播放。程序会遍历所有符合条件的播放器，跳过读不到可用歌名的服务，并按 `PlaybackStatus` 优先选择正在播放的那台（其次暂停），所以"浏览器空闲 + VLC 在播"也能取到 VLC 的歌词。

各播放器上报的字段差异及处理方式：

| 播放器 | 上报情况 | 处理 |
| --- | --- | --- |
| Chromium / Chrome / Edge | 网页 MediaSession 提供完整标题、歌手列表、专辑；空闲时只有 `mpris:length` | 多个歌手拼成 `"歌手A, 歌手B"`；无歌名直接跳过 |
| Firefox | `xesam:title` 是网页标题，`xesam:artist` 常是空字符串（或只含空串的列表） | 去掉 ` - YouTube Music` 等站点后缀；歌手为空时按歌名搜索 |
| VLC | 标签字段完整，但空闲时可能把播放器名当歌名 | 播放器名、`unknown` 等占位标题不参与搜索 |
| mpv（mpv-mpris） | 有标签时字段完整；播放网络流时歌名为空，只有 `xesam:url` | 歌名为空时从 URL 文件名推断，并去掉 `.m3u8` 等扩展名 |
| Telegram Desktop | 播放音乐文件时标题是文件名（`歌手 - 歌名.mp3`）；语音消息只有占位标题 | 去扩展名、按 ` - ` 拆分歌手歌名；`voice message`/`语音消息` 等占位标题视为没有歌名 |
| 本地文件（任意播放器） | 标题常是文件名，可能带音轨号（`01 - 歌手 - 歌名.flac`） | 去扩展名与音轨号，再拆分歌手歌名 |

三种模式都可用 `song_id_mode: search` 强制按歌名和歌手搜索。`splayer-next` 的自动模式在尚未取得有效歌曲 ID 时，每隔 5 秒检查一次，最多检查 5 次后按歌名和歌手搜索；等待期间仍可显示播放器 API 提供的歌词。网易云以外来源的 ID 不用于查询网易云歌词。歌词查询依次尝试 TTML、网易云和 LRCLib；网易云搜索不到 ID 时直接尝试 LRCLib，LRCLib 使用播放器提供的歌曲信息。

#### `splayer` / `splayer-next` 的 MPRIS 回退

`mpris-fallback`（默认 `true`）让 SPlayer 模式在以下情况自动改用 MPRIS 读取其他播放器的歌曲：

- `splayer-next`：SPlayer-Next 的外部 API 连不通，或接口读不到当前曲目；
- `splayer`：MPRIS 上找不到 SPlayer，或从 SPlayer 读不到歌名。

回退时沿用 `mpris` 模式的规则：遍历所有 MPRIS 播放器，排除 SPlayer 自身，优先正在播放的那台。回退得到的歌名来自 D-Bus，不做 `latency_ms` 网络延迟补偿，也不会等待播放器 API 歌词，直接按歌名和歌手搜索歌词。把 `mpris-fallback` 设为 `false` 可关闭回退，此时 SPlayer 读不到歌曲就显示为没有播放中的曲目。

## 播放控制

程序内提供统一控制入口，读取同一份 YAML 配置，也支持用 `--mode` / `-m` 临时切换：

```bash
waybar-lyrics play          # 播放 / 恢复
waybar-lyrics pause         # 暂停
waybar-lyrics play-pause    # 播放 / 暂停切换
waybar-lyrics next          # 下一首
waybar-lyrics previous      # 上一首
waybar-lyrics stop          # 停止
waybar-lyrics --mode splayer-next play-pause
```

- `splayer-next`：使用配置中的 `splayer.address` 和 `splayer.port`，按[官方 API 文档](https://github.com/SPlayer-Dev/SPlayer-Next/blob/dev/docs/api.md)调用 `POST /api/play`、`/api/pause`、`/api/next`、`/api/prev`、`/api/stop`。`play-pause` 先查询 `GET /api/status`，正在播放时暂停，否则播放；API 失败不会回退到 `playerctl`。
- `splayer` / `mpris`：程序直接执行系统中的 `playerctl`。先列出播放器，沿用歌词模式的筛选规则：`splayer` 只选择名称含 SPlayer 的播放器，`mpris` 排除 SPlayer；控制第一个匹配项，不向所有播放器广播。
- 控制命令执行一次后退出，不输出歌词、不清空歌词进程日志；失败时向标准错误输出原因并以非零状态退出。无命令时仍持续输出歌词。
- 选项应放在命令之前。使用 `waybar-lyrics --help` 查看帮助。
- 使用 `--version` 或 `-V` 打印版本号后退出，输出形如 `waybar-lyrics 1.4.2`。

## Waybar 配置

```json
"custom/lyrics": {
    "exec": "waybar-lyrics 2> /dev/null",
    "return-type": "json",
    "format": "{}",
    "on-click": "waybar-lyrics play-pause",
    "on-click-right": "waybar-lyrics next",
    "on-click-middle": "waybar-lyrics previous",
    "exec-on-event": false,
    "tooltip": true,
    "justify": "center"
},
```

把 `custom/lyrics` 加入 `modules-left`、`modules-center` 或 `modules-right`。左键播放/暂停，右键下一首，中键上一首。`exec-on-event: false` 避免点击后重新启动持续运行的歌词进程。若 `exec` 使用 `--mode splayer-next` 等命令行覆盖模式，点击命令也应添加相同的模式选项，例如 `waybar-lyrics --mode splayer-next play-pause`；或者直接在 YAML 中统一设置模式。

`exec` 与点击命令都直接写 `waybar-lyrics`，不要写绝对路径或 `~/.local/bin` 下的旧副本：程序安装后就在 PATH 中（`/usr/bin` 或 `/usr/local/bin`），写死路径会导致副本过期、遮蔽包管理器的更新。

tooltip 显示“歌手 - 歌名”和歌词来源（`API`、`TTML`、`NCM`、`LRCLib`）。没有播放中的曲目时模块自动隐藏。

## 许可证

GPL License
