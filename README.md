# waybar-lyrics

一个用于 Waybar 的自定义模块，可显示播放器同步歌词。歌词来源按 AMLL TTML 歌词库、网易云音乐、LRCLib 的顺序检索。

## 功能特性

- **三种播放器模式**：普通 MPRIS、旧版 SPlayer（MPRIS）和 SPlayer-Next（HTTP API）彼此独立。
- **SPlayer-Next 歌词优先**：API 已返回当前歌词时直接使用，不再搜索歌曲；API 没有歌词时才读取网易云歌曲 ID，必要时按标题和歌手搜索。
- **歌词来源**：AMLL TTML DB 优先，逐词 TTML 会转换为逐行显示；未命中后依次使用网易云音乐和 LRCLib。
- **双语支持**：支持显示原文歌词与翻译歌词（如有）。
- **Waybar 兼容**：输出符合 Waybar custom 模块要求的 JSON 格式。
- **控制能力**：支持通过 Waybar 点击事件控制播放/暂停、上一首、下一首。

## 安装

### Arch Linux（AUR）

可从 AUR 安装（还没提交，请稍等一段时间）：

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

## 播放器配置

配置文件路径为 `$XDG_CONFIG_HOME/waybar-lyrics/waybar-lyrics.yaml`；未设置 `XDG_CONFIG_HOME` 时使用 `$HOME/.config/waybar-lyrics/waybar-lyrics.yaml`。不创建配置文件时默认使用普通 MPRIS 模式。

```yaml
# 可选值：mpris、splayer、splayer-next
mode: splayer-next

splayer:
  # SPlayer-Next 外部 API 地址和端口
  address: 127.0.0.1
  port: 14558
  # auto：优先读取播放器提供的网易云 ID；search：始终按标题和歌手搜索
  song_id_mode: auto
```

也可以把地址和端口写在 `splayer.api` 下，适用于已有这类配置结构的用户：

```yaml
mode: splayer-next
splayer:
  song_id_mode: auto
  api:
    address: 127.0.0.1
    port: 14558
```

SPlayer-Next 的外部 API 默认关闭，需要在 SPlayer-Next 的“设置 → 外部 API”中开启。该接口默认只监听本机，默认端口为 `14558`。

三种模式的歌词匹配流程如下：

1. SPlayer-Next API 已提供歌词时直接显示 API 歌词。
2. 否则尝试读取播放器元数据中的网易云歌曲 ID。
3. 没有可用歌曲 ID 时按标题和歌手搜索网易云歌曲。
4. 按 AMLL TTML DB → 网易云音乐 → LRCLib 的顺序获取歌词。

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
