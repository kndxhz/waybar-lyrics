# waybar-lyrics

为 Waybar 显示当前播放器同步歌词的自定义模块。

歌词依次取自播放器 API、AMLL TTML 歌词库、网易云音乐和 LRCLib。

## 安装

Arch Linux（AUR）：

```bash
yay -S waybar-lyrics
```

预编译二进制：从 [Releases](https://github.com/kndxhz/waybar-lyrics/releases) 下载对应架构，安装到 PATH 中的目录：

```bash
chmod +x waybar-lyrics-linux-amd64
sudo install -Dm755 waybar-lyrics-linux-amd64 /usr/local/bin/waybar-lyrics
```

手动编译需要 Go：`go build -o waybar-lyrics` 后同样安装到 PATH。`mpris` / `splayer` 模式的播放控制需要 `playerctl`，仅显示歌词不需要。

安装后可用 `waybar-lyrics --version`（或 `-V`）确认版本。

## 配置

配置文件为 `~/.config/waybar-lyrics/waybar-lyrics.yaml`，首次运行自动生成；升级后缺失的新配置项会自动补写，已有取值不会被覆盖。

```yaml
mode: splayer-next   # mpris、splayer 或 splayer-next
mpris-fallback: true # SPlayer 读不到歌曲时回退到 MPRIS；mpris 模式下忽略
mpris-ms: 500        # MPRIS 来源歌词的时间补偿（毫秒）：正数提前，负数延后，0 关闭
splayer:
  address: 127.0.0.1 # SPlayer-Next API 地址
  port: 14558        # SPlayer-Next API 端口
  song_id_mode: auto # auto：优先使用播放器歌曲 ID；search：按歌名歌手搜索
  latency_ms: 100    # SPlayer-Next 歌词时间补偿（毫秒），含义同上
log:
  path: /tmp/waybar-lyrics.log
  level: INFO        # DEBUG、INFO、WARN 或 ERROR
llm:
  enable: false      # 是否用 LLM 从 MPRIS 元数据提取歌名歌手
  base_url: https://api.deepseek.com/v1 # OpenAI 兼容接口地址
  model_name: deepseek-flash
  api_key: xxx       # 接口密钥，按需替换
  prompt: |-         # {metadata} 会替换成播放器元数据
    基于以下内容，如果这个媒体是歌曲，提取这个媒体的歌名，输出{"title":"歌名","singer":"歌手"}，歌名不要包含歌手，歌手如果数据内没有，不要瞎猜，只需要输出为null。如果不是歌曲，输出{"is_song":false}

    {metadata}
```

`--mode` / `-m` 可临时覆盖配置文件中的模式。`mpris-ms` 作用于 `mpris` 模式以及 SPlayer 回退到 MPRIS 的情况；SPlayer-Next 通过 HTTP API 提供的进度仍用 `splayer.latency_ms`。日志每次启动时清空，仅 `DEBUG` 级别记录完整歌词。

### 播放器模式

- `mpris`：普通 MPRIS 播放器（Spotify、VLC、mpv 等），按歌名和歌手搜索。
- `splayer`：旧版 SPlayer，歌曲 ID 取自 MPRIS。
- `splayer-next`：通过 SPlayer-Next 外部 API 读取曲目和歌词，优先显示播放器提供的歌词。需在 SPlayer-Next 设置中开启外部 API。

同一时刻可能有多台播放器注册 MPRIS（浏览器即使没有播放也会常驻），程序会遍历所有播放器，跳过读不到歌名的服务，并按 `PlaybackStatus` 优先选择正在播放的那台。标题里的扩展名、音轨号、站点后缀以及 `歌手 - 歌名` 格式会被清洗后再用于搜索。

`splayer` / `splayer-next` 读不到歌曲时，`mpris-fallback` 会让程序改用 MPRIS 读取其他播放器（排除 SPlayer 自身）；回退得到的歌词不做 `latency_ms` 补偿，也不会等待播放器 API 歌词。

### 基于 LLM 的歌曲信息提取

`llm.enable` 开启后，凡是通过 MPRIS 取到的歌曲都会先把播放器元数据交给 OpenAI 兼容接口，由模型判断是否为歌曲并提取歌名、歌手，再按「歌手 歌名」搜索。

- `llm.prompt` 中的 `{metadata}` 会替换成播放器元数据（形如 `xesam:title 歌名` 的多行文本）。
- 判定非歌曲时按没识别到歌曲处理，不再用原始标题搜索；接口失败或输出无法解析时仍沿用原始标题，歌词显示不中断。
- `api_key` 默认 `xxx`，需替换为可用密钥。同一首歌只请求一次。

## 播放控制

```bash
waybar-lyrics play | pause | play-pause | next | previous | stop
waybar-lyrics --mode splayer-next play-pause   # 临时切换模式
```

`splayer-next` 走外部 API，失败时按 `mpris-fallback` 的设置回退给 `playerctl`；`splayer` / `mpris` 直接调用系统中的 `playerctl`。命令执行一次后退出，失败时向标准错误输出原因并以非零状态退出。

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

把 `custom/lyrics` 加入 `modules-left`、`modules-center` 或 `modules-right`：左键播放/暂停，右键下一首，中键上一首。tooltip 显示「歌手 - 歌名」和歌词来源，没有播放中的曲目时模块自动隐藏。

`exec` 与点击命令都直接写 `waybar-lyrics`，不要写绝对路径或 `~/.local/bin` 下的旧副本，否则副本会遮蔽包管理器的更新。若用 `--mode` 覆盖模式，点击命令也要带上相同选项，或者直接在 YAML 中统一设置模式。

## 许可证

GPL License
