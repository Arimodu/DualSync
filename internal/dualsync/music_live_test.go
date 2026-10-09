package dualsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLivePlexMusicQueue(t *testing.T) {
	if os.Getenv("DUALSYNC_PLEX_LIVE") == "" {
		t.Skip("requires configured Zeus staging Plex")
	}
	var settings Settings
	if err := ReadJSON("/var/lib/dualsync-dev/settings.json", &settings); err != nil {
		t.Fatal(err)
	}
	settings.MusicSource = "plex"
	settings.MusicShuffle = false
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client := newPlexClient(settings)
	lists, err := client.playlists(ctx)
	if err != nil || len(lists) == 0 {
		t.Fatal("live Plex listing failed", err)
	}
	settings.PlexPlaylistID = lists[0].ID
	tracks, err := client.tracks(ctx, settings.PlexPlaylistID)
	if err != nil || len(tracks) == 0 {
		t.Fatal("live Plex tracks failed", err)
	}
	library, err := NewLibrary("/mnt/data-vault")
	if err != nil {
		t.Fatal(err)
	}
	defer library.Close()
	state := t.TempDir()
	m := NewConfiguredMusic(library, state, settings)
	defer m.Close()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		m.Tick(false, true, 25)
		if m.reader != nil && len(m.queue) == 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(m.queue) != 3 {
		t.Fatal("live queue did not fill", m.Error)
	}
	for n := 0; n < 3; n++ {
		start := time.Now()
		m.Next()
		m.Tick(false, true, 25)
		if m.info.Position != 0 {
			t.Fatal("held song advanced")
		}
		t.Logf("ready skip %d: %.3f ms", n+1, time.Since(start).Seconds()*1000)
	}
	track := tracks[0]
	track.File = "/unmounted/force-plex-fallback"
	prepared := m.prepareTrack(ctx, client, track, "plex")
	defer prepared.close()
	if prepared.err != "" || prepared.reader == nil {
		t.Fatal("real Plex fallback failed", prepared.err)
	}
	files, _ := os.ReadDir(filepath.Join(state, "plex-cache"))
	if len(files) == 0 {
		t.Fatal("real fallback not cached")
	}
	raw, _ := json.Marshal(prepared.info)
	if string(raw) == "" {
		t.Fatal("missing fallback metadata")
	}
	t.Logf("real Plex original media fallback prepared; cached objects: %d", len(files))
}
