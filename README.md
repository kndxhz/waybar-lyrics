# waybar-lyrics

为 Waybar 显示当前播放器同步歌词的自定义模块。

歌词依次取自播放器 API、AMLL TTML 歌词库、网易云音乐和 LRCLib。

## 安装

### Arch Linux（AUR）

```bash
yay -S waybar-lyrics-git
```

### 手动安装

需要 Go。`mpris` / `splayer` 模式的播放控制需要安装 `playerctl`；`splayer-next` 模式只使用 HTTP API，不依赖 `playerctl`。仅显示歌词不需要 `playerctl`。

```bash
git clone https://github.com/kndxhz/waybar-lyrics.git
cd waybar-lyrics
go build -o waybar-lyrics
sudo install -Dm755 waybar-lyrics /usr/bin/waybar-lyrics
```

## 配置

配置文件为 `~/.config/waybar-lyrics/waybar-lyrics.yaml`，首次运行自动生成：

```yaml
mode: splayer-next   # mpris、splayer 或 splayer-next
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

日志默认写入 `/tmp/waybar-lyrics.log`，每次启动时清空；仅 `DEBUG` 级别记录完整歌词。

### 播放器模式

- `mpris`：普通 MPRIS 播放器（Spotify、VLC、mpv 等）。播放器没有网易云歌曲 ID，按歌名和歌手搜索。
- `splayer`：旧版 SPlayer，歌曲 ID 取自 MPRIS。
- `splayer-next`：通过 SPlayer-Next 外部 API 读取曲目和歌词，优先显示播放器提供的歌词，并补偿网络延迟。需在 SPlayer-Next 设置中开启外部 API。

三种模式都可用 `song_id_mode: search` 强制按歌名和歌手搜索。`splayer-next` 的自动模式在尚未取得有效歌曲 ID 时，每隔 5 秒检查一次，最多检查 5 次后按歌名和歌手搜索；等待期间仍可显示播放器 API 提供的歌词。网易云以外来源的 ID 不用于查询网易云歌词。歌词查询依次尝试 TTML、网易云和 LRCLib；网易云搜索不到 ID 时直接尝试 LRCLib，LRCLib 使用播放器提供的歌曲信息。

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

tooltip 显示“歌手 - 歌名”和歌词来源（`API`、`TTML`、`NCM`、`LRCLib`）。没有播放中的曲目时模块自动隐藏。

## 许可证

GPL License
