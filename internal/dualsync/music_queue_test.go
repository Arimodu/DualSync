package dualsync

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func sampleReader(sample int16) *RawReader {
	r := &RawReader{queue: make(chan []byte, 20), done: make(chan struct{}), cancel: func() {}}
	for n := 0; n < 20; n++ {
		b := make([]byte, AudioBytes)
		for i := 0; i < len(b); i += 2 {
			binary.LittleEndian.PutUint16(b[i:], uint16(sample))
		}
		r.queue <- b
	}
	return r
}
func TestMusicPreparedNextCrossfadeAndRapidSkips(t *testing.T) {
	m := &Music{reader: sampleReader(1000), gain: .25, target: .25, info: MusicInfo{Path: "old"}, settings: Settings{MusicSource: "folder"}}
	for i := 0; i < 3; i++ {
		m.queue = append(m.queue, preparedMusic{reader: sampleReader(int16(2000 + i*1000)), info: MusicInfo{Path: fmt.Sprint(i)}})
	}
	m.Next()
	start := time.Now()
	data, info := m.Tick(true, true, 25)
	if time.Since(start) > 100*time.Millisecond || len(data) != AudioBytes || info.Path != "0" || m.gain != .25 || len(m.queue) != 2 {
		t.Fatal("ready next was delayed or reset gain")
	}
	for i := 0; i < 5; i++ {
		m.Tick(true, true, 25)
	}
	if m.previous.reader != nil || m.crossSamples != 0 {
		t.Fatal("crossfade exceeded 250ms")
	}
	m.Mute()
	m.Next()
	m.Next()
	m.Tick(false, true, 25)
	if m.info.Path != "2" || m.skips != 0 || m.info.Position != 0 {
		t.Fatal("rapid skips lost or consumed held song")
	}
	m.Close()
}

func TestPlexAPIAndFallbackMusic(t *testing.T) {
	e, s, root := testEngine(t)
	name := fixture(t, root)
	media, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	const token = "private-test-token"
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != token {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/library/parts/1/file.mkv" {
			w.Write(media)
			return
		}
		var rows []map[string]any
		if r.URL.Path == "/playlists" {
			rows = []map[string]any{{"ratingKey": "1", "title": "Test playlist", "smart": true, "leafCount": 0}}
		} else {
			rows = []map[string]any{{"ratingKey": "42", "title": "Plex song", "grandparentTitle": "Album artist", "originalTitle": "Track artist", "parentTitle": "Album", "Media": []any{map[string]any{"Part": []any{map[string]any{"file": "/unmounted/song.mp3", "key": "/library/parts/1/file.mkv"}}}}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"Metadata": rows, "totalSize": len(rows)}})
	}))
	defer p.Close()
	patch := fmt.Sprintf(`{"plex_server":%q,"plex_token":%q}`, p.URL, token)
	res := call(t, s, "POST", "/api/settings", patch, 200)
	if strings.Contains(res.Body.String(), token) {
		t.Fatal("Plex token leaked")
	}
	call(t, s, "GET", "/api/plex/playlists", "", 200)
	call(t, s, "GET", "/api/plex/playlist?id=1", "", 200)
	call(t, s, "GET", "/api/plex/playlist?id=bad", "", 400)
	call(t, s, "POST", "/api/control", `{"action":"start"}`, 200)
	call(t, s, "POST", "/api/settings", `{"music_source":"plex","plex_playlist_id":"1","music_shuffle":false}`, 200)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		x := e.Snapshot()
		if x.Music.Path == "plex:42" && x.Music.ReadyCount == 3 {
			if x.Music.Title != "Plex song" || x.Music.AlbumArtist != "Album artist" {
				t.Fatal("Plex metadata lost")
			}
			entries, _ := os.ReadDir(filepath.Join(e.Config.StateDir, "plex-cache"))
			if len(entries) == 0 {
				t.Fatal("missing fallback cache")
			}
			call(t, s, "POST", "/api/control", `{"action":"music_refresh"}`, 200)
			call(t, s, "POST", "/api/settings", `{"music_source":"off"}`, 200)
			if e.music.reader != nil || e.music.previous.reader != nil || len(e.music.queue) != 0 {
				t.Fatal("off left active readers")
			}
			call(t, s, "POST", "/api/control", `{"action":"stop"}`, 200)
			if e.music.reader != nil || len(e.music.queue) != 0 {
				t.Fatal("stop left queued decoders")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Plex fallback did not prepare: %+v", e.Snapshot().Music)
}

func TestPlexPaginationAndFailureRedaction(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "secret" {
			w.WriteHeader(401)
			return
		}
		count++
		offset, _ := strconv.Atoi(r.Header.Get("X-Plex-Container-Start"))
		rows := []map[string]any{}
		for i := offset; i < min(offset+200, 201); i++ {
			rows = append(rows, map[string]any{"ratingKey": fmt.Sprint(i), "title": "Track"})
		}
		json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"Metadata": rows, "totalSize": 201}})
	}))
	defer server.Close()
	c := newPlexClient(Settings{PlexServer: server.URL, PlexToken: "secret"})
	rows, err := c.tracks(context.Background(), "1")
	if err != nil || len(rows) != 201 || count != 2 {
		t.Fatal("pagination failed", len(rows), count, err)
	}
	c.settings.PlexToken = "wrong"
	_, err = c.playlists(context.Background())
	if err == nil || strings.Contains(err.Error(), "wrong") {
		t.Fatal("authentication error leaked token", err)
	}
	if _, err = c.get(context.Background(), "http://outside.invalid/file"); err == nil {
		t.Fatal("accepted external part URL")
	}
}
