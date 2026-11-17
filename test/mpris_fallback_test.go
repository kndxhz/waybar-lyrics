package main

import (
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// fakeMPRIS 在会话总线上注册一台假的 MPRIS 播放器，用于验证
// getMPRISMetadata 与 SPlayer 模式的 MPRIS 回退。
// 没有会话总线（例如 CI 或纯终端）时跳过测试。
func fakeMPRIS(t *testing.T, name, title, artist, status string) {
	t.Helper()
	conn, err := dbus.SessionBusPrivate()
	if err != nil {
		t.Skipf("没有可用的会话总线：%v", err)
	}
	if err := conn.Auth(nil); err != nil {
		conn.Close()
		t.Skipf("会话总线认证失败：%v", err)
	}
	if err := conn.Hello(); err != nil {
		conn.Close()
		t.Skipf("会话总线握手失败：%v", err)
	}
	reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
	if err != nil {
		t.Skipf("注册 %s 失败：%v", name, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		t.Skipf("%s 已被其他进程占用", name)
	}
	t.Cleanup(func() {
		_, _ = conn.ReleaseName(name)
		conn.Close()
	})

	metadata := map[string]dbus.Variant{
		"mpris:trackid": dbus.MakeVariant(dbus.ObjectPath("/org/mpris/MediaPlayer2/Track/1")),
		"xesam:title":   dbus.MakeVariant(title),
		"xesam:artist":  dbus.MakeVariant([]string{artist}),
		"mpris:length":  dbus.MakeVariant(int64(180 * 1000 * 1000)),
	}
	properties := map[string]map[string]*prop.Prop{
		"org.mpris.MediaPlayer2.Player": {
			"PlaybackStatus": {Value: status, Writable: false, Emit: prop.EmitFalse},
			"Metadata":       {Value: metadata, Writable: false, Emit: prop.EmitFalse},
			"Position":       {Value: int64(42 * 1000 * 1000), Writable: false, Emit: prop.EmitFalse},
		},
	}
	if _, err := prop.Export(conn, "/org/mpris/MediaPlayer2", properties); err != nil {
		t.Skipf("导出 MPRIS 属性失败：%v", err)
	}
}

func TestGetMPRISMetadataReadsPlayer(t *testing.T) {
	fakeMPRIS(t, "org.mpris.MediaPlayer2.fallbacktest", "回退歌曲", "回退歌手", "Playing")

	metadata := getMPRISMetadata("")
	if metadata == nil {
		t.Fatal("应读到假播放器的元数据")
	}
	if metadata.Player != "fallbacktest" || metadata.Title != "回退歌曲" || metadata.Artist != "回退歌手" {
		t.Fatalf("元数据不符：%+v", metadata)
	}
	if metadata.Duration != 180*1000*1000 || metadata.Position != 42*1000*1000 {
		t.Fatalf("时长或进度不符：%+v", metadata)
	}
}

func TestGetPlayerMetadataFallsBackToMPRIS(t *testing.T) {
	oldConfig := appConfig
	appConfig = defaultConfig()
	t.Cleanup(func() { appConfig = oldConfig })

	fakeMPRIS(t, "org.mpris.MediaPlayer2.fallbacktest", "回退歌曲", "回退歌手", "Playing")

	// SPlayer-Next 的 API 指向未监听的端口，读不到歌曲后应回退到 MPRIS。
	appConfig.Mode = "splayer-next"
	appConfig.SPlayer.Port = 1
	appConfig.SPlayer.Address = "127.0.0.1"
	appConfig.MPRISFallback = true
	metadata := getPlayerMetadata()
	if metadata == nil || metadata.Player != "fallbacktest" || metadata.Title != "回退歌曲" {
		t.Fatalf("splayer-next 应回退到 MPRIS，got %+v", metadata)
	}

	// 关闭回退后不应再读取其他播放器。
	appConfig.MPRISFallback = false
	if metadata := getPlayerMetadata(); metadata != nil {
		t.Fatalf("关闭 mpris-fallback 后不应回退，got %+v", metadata)
	}

	// splayer 模式读不到 SPlayer 时同样回退。
	appConfig.Mode = "splayer"
	appConfig.MPRISFallback = true
	metadata = getPlayerMetadata()
	if metadata == nil || metadata.Player != "fallbacktest" {
		t.Fatalf("splayer 应回退到 MPRIS，got %+v", metadata)
	}

	// mpris 模式本身读取 MPRIS，不受该配置影响。
	appConfig.Mode = "mpris"
	appConfig.MPRISFallback = false
	if metadata := getPlayerMetadata(); metadata == nil || metadata.Player != "fallbacktest" {
		t.Fatalf("mpris 模式应忽略 mpris-fallback，got %+v", metadata)
	}
}
