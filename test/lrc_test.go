package main

import "testing"

// 网易云上不少同人曲（例如「超时空辉夜姬」系列的投稿）的歌词用冒号分隔
// 毫秒，形如「[00:08:31]」，必须与「[00:08.31]」一样解析出时间轴。
func TestParseLRCColonSeparatedFraction(t *testing.T) {
	content := "[00:00.00] 作词 : ryo (supercell)\n" +
		"[00:08:31]今は昔　誰もが知る物語\n" +
		"[00:13:02]かの有名なかぐや姫はこう言った\n" +
		"[01:31:34]めちゃくちゃ笑ってるのに涙止まんない\n"

	lines := parseLRC(content)
	if len(lines) != 4 {
		t.Fatalf("行数 = %d, want 4：%+v", len(lines), lines)
	}

	wantTimes := []int64{0, 8310, 13020, 91340}
	wantTexts := []string{
		"作词 : ryo (supercell)",
		"今は昔　誰もが知る物語",
		"かの有名なかぐや姫はこう言った",
		"めちゃくちゃ笑ってるのに涙止まんない",
	}
	for i, line := range lines {
		if line.TimeMs != wantTimes[i] {
			t.Fatalf("第 %d 行时间 = %d, want %d", i, line.TimeMs, wantTimes[i])
		}
		if line.Text != wantTexts[i] {
			t.Fatalf("第 %d 行文本 = %q, want %q", i, line.Text, wantTexts[i])
		}
	}
}

func TestParseLRCFractionVariants(t *testing.T) {
	content := "[00:01]无毫秒\n" +
		"[00:02.5]一位小数\n" +
		"[00:03.25]两位小数\n" +
		"[00:04.125]三位小数\n" +
		"[00:05:75]冒号两位\n"

	lines := parseLRC(content)
	wantTimes := []int64{1000, 2500, 3250, 4125, 5750}
	if len(lines) != len(wantTimes) {
		t.Fatalf("行数 = %d, want %d：%+v", len(lines), len(wantTimes), lines)
	}
	for i, line := range lines {
		if line.TimeMs != wantTimes[i] {
			t.Fatalf("第 %d 行时间 = %d, want %d", i, line.TimeMs, wantTimes[i])
		}
	}
}

func TestParseLRCWithInlineTranslationColonSeparator(t *testing.T) {
	content := "[00:08:31]今は昔　誰もが知る物語|那是很久以前 人尽皆知的故事\n" +
		"[00:13:02]かの有名なかぐや姫はこう言った|那位赫赫有名的辉夜姬 曾这样诉说\n"

	original, translation := parseLRCWithInlineTranslation(content)
	if len(original) != 2 || len(translation) != 2 {
		t.Fatalf("原文 %d 行、翻译 %d 行，want 各 2 行", len(original), len(translation))
	}
	if original[0].TimeMs != 8310 || translation[0].TimeMs != 8310 {
		t.Fatalf("首行时间不符：原文 %d、翻译 %d", original[0].TimeMs, translation[0].TimeMs)
	}
	if original[0].Text != "今は昔　誰もが知る物語" || translation[0].Text != "那是很久以前 人尽皆知的故事" {
		t.Fatalf("首行文本不符：%q / %q", original[0].Text, translation[0].Text)
	}
}

// 纯文本歌词（没有时间戳）仍然返回空，避免把说明文字当成歌词。
func TestParseLRCWithoutTimestamp(t *testing.T) {
	lines := parseLRC("[by:Aki惊蛰]\n纯文本歌词\n")
	if len(lines) != 0 {
		t.Fatalf("无时间戳时应为空，got %+v", lines)
	}
}
