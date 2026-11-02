# waybar-lyrics

一个用于 Waybar 的自定义模块，可显示来自 MPRIS 播放器的同步歌词。歌词来源支持网易云音乐与 LRCLib。

## 功能特性

- **MPRIS 集成**：自动检测 MPRIS 兼容播放器（如 Spotify、VLC、mpv）当前播放歌曲。
- **歌词来源**：
  - 网易云音乐（主来源）
  - LRCLib（兜底来源）
- **双语支持**：支持显示原文歌词与翻译歌词（如有）。
- **Waybar 兼容**：输出符合 Waybar custom 模块要求的 JSON 格式。
- **控制能力**：支持通过 Waybar 点击事件控制播放/暂停、上一首、下一首。

## 安装

### Arch Linux（AUR）

可从 AUR 安装(还没提交,请稍等一段时间...)：

```bash
yay -S waybar-lyrics-git
```

### 手动安装

1.  **依赖项**：
    - go（用于构建）
    - playerctl（可选，用于 Waybar 点击控制命令）

2.  **构建**：

    ```bash
    git clone https://github.com/kndxhz/waybar-lyrics.git
    cd waybar-lyrics
    go build -o waybar-lyrics
    ```

3.  **安装**：

    ```bash
    sudo install -Dm755 waybar-lyrics /usr/bin/waybar-lyrics
    ```

## 配置

将以下模块添加到 waybar/config（或 modules.json）：

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

然后把 "custom/lyrics" 加入 modules-left、modules-center 或 modules-right 列表。

## 使用说明

- **左键**：播放/暂停
- **右键**：下一首
- **中键**：上一首

当没有活跃播放器或播放器停止时，模块会自动隐藏。

## 许可证

GPL License
