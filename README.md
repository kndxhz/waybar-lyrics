# waybar-lyrics

为 Waybar 显示当前播放器同步歌词的自定义模块。

歌词依次取自播放器 API、AMLL TTML 歌词库、网易云音乐和 LRCLib。

## 安装

### Arch Linux（AUR）

```bash
yay -S waybar-lyrics-git
```

### 手动安装

需要 Go；`playerctl` 可选，用于 Waybar 点击控制。

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
  latency_ms: 100    # SPlayer-Next 歌词提前量（毫秒），0 关闭
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

三种模式都可用 `song_id_mode: search` 强制按歌名和歌手搜索。

## Waybar 配置

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

把 `custom/lyrics` 加入 `modules-left`、`modules-center` 或 `modules-right`。左键播放/暂停，右键下一首，中键上一首。

tooltip 显示“歌手 - 歌名”和歌词来源（`API`、`TTML`、`NCM`、`LRCLib`）。没有播放中的曲目时模块自动隐藏。

## 许可证

GPL License
