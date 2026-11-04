package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type splayerNextSnapshot struct {
	Track    json.RawMessage `json:"track"`
	Playing  json.RawMessage `json:"nowPlaying"`
	State    string          `json:"state"`
	Position int64           `json:"position"`
	Duration int64           `json:"duration"`
}

type splayerNextTrack struct {
	ID      string `json:"id"`
	ExtID   string `json:"extId"`
	Source  string `json:"source"`
	Title   string `json:"title"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Album struct {
		Name string `json:"name"`
	} `json:"album"`
	Duration int64  `json:"duration"`
	Cover    string `json:"cover"`
	Plugin   struct {
		MusicInfo struct {
			ID string `json:"id"`
		} `json:"musicInfo"`
	} `json:"plugin"`
}

func getPlayerMetadata() *PlayerMetadata {
	switch appConfig.Mode {
	case "splayer-next":
		return getSPlayerNextMetadata()
	case "splayer":
		return getMPRISMetadata("splayer")
	default:
		return getMPRISMetadata("")
	}
}

func splayerBaseURL() string {
	address := strings.TrimSpace(appConfig.SPlayer.Address)
	if address == "" {
		address = "127.0.0.1"
	}
	if !strings.Contains(address, "://") {
		if net.ParseIP(address) != nil && strings.Contains(address, ":") {
			address = "[" + address + "]"
		}
		address = "http://" + address
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	if parsed.Port() == "" && appConfig.SPlayer.Port > 0 {
		parsed.Host = net.JoinHostPort(parsed.Hostname(), strconv.Itoa(appConfig.SPlayer.Port))
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func fetchJSON(path string, target any) bool {
	client := &http.Client{Timeout: 1200 * time.Millisecond}
	res, err := client.Get(splayerBaseURL() + path)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(res.Body)
	return err == nil && json.Unmarshal(body, target) == nil
}

func getSPlayerNextMetadata() *PlayerMetadata {
	var raw struct {
		Track      json.RawMessage `json:"track"`
		NowPlaying json.RawMessage `json:"nowPlaying"`
		Position   int64           `json:"position"`
		Duration   int64           `json:"duration"`
	}
	if !fetchJSON("/now-playing", &raw) {
		return nil
	}
	trackData := raw.Track
	if len(trackData) == 0 {
		var nested struct {
			Track json.RawMessage `json:"track"`
		}
		if json.Unmarshal(raw.NowPlaying, &nested) == nil && len(nested.Track) > 0 {
			trackData = nested.Track
		} else {
			trackData = raw.NowPlaying
		}
	}
	var track splayerNextTrack
	if len(trackData) == 0 || json.Unmarshal(trackData, &track) != nil {
		return nil
	}
	if track.Title == "" {
		return nil
	}
	artist := ""
	if len(track.Artists) > 0 {
		artist = track.Artists[0].Name
	}
	if artist == "" {
		return nil
	}
	metadata := &PlayerMetadata{Player: "splayer-next", TrackID: track.ID, Source: track.Source, Title: track.Title, Artist: artist, Album: track.Album.Name, Duration: track.Duration * 1000, ArtUrl: track.Cover}
	if raw.Position > 0 {
		metadata.Position = raw.Position * 1000
	}
	if raw.Duration > 0 {
		metadata.Duration = raw.Duration * 1000
	}
	if track.ExtID != "" {
		metadata.TrackID = track.ExtID
	}
	if track.Plugin.MusicInfo.ID != "" {
		metadata.TrackID = track.Plugin.MusicInfo.ID
	}
	metadata.Lyrics = getSPlayerNextLyrics()
	return metadata
}

func getSPlayerNextLyrics() *ParsedLyrics {
	var response json.RawMessage
	if !fetchJSON("/lyrics", &response) {
		return nil
	}
	return parseSPlayerNextLyrics(response)
}

func parseSPlayerNextLyrics(data []byte) *ParsedLyrics {
	var envelope struct {
		Lyric  json.RawMessage `json:"lyric"`
		Lyrics json.RawMessage `json:"lyrics"`
	}
	if json.Unmarshal(data, &envelope) == nil {
		if len(envelope.Lyric) > 0 && string(envelope.Lyric) != "null" {
			data = envelope.Lyric
		} else if len(envelope.Lyrics) > 0 {
			data = envelope.Lyrics
		}
	}
	var lines []struct {
		StartTime  int64  `json:"startTime"`
		EndTime    int64  `json:"endTime"`
		Translated string `json:"translatedLyric"`
		Words      []struct {
			Word string `json:"word"`
		} `json:"words"`
	}
	if json.Unmarshal(data, &lines) != nil || len(lines) == 0 {
		return nil
	}
	result := &ParsedLyrics{}
	for _, line := range lines {
		var text strings.Builder
		for _, word := range line.Words {
			text.WriteString(word.Word)
		}
		if text.Len() == 0 {
			continue
		}
		result.Original = append(result.Original, TimedLyricLine{TimeMs: line.StartTime, Text: text.String()})
		if strings.TrimSpace(line.Translated) != "" {
			result.Translation = append(result.Translation, TimedLyricLine{TimeMs: line.StartTime, Text: line.Translated})
		}
	}
	if len(result.Original) == 0 {
		return nil
	}
	return result
}

func extractTrackID(value string) string {
	if match := regexp.MustCompile(`(?:/|^)([0-9]+)$`).FindStringSubmatch(strings.TrimSpace(value)); len(match) > 1 {
		return match[1]
	}
	return ""
}
