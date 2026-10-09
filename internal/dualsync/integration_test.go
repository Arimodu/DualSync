package dualsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRealLibraryFixtures(t *testing.T) {
	root := os.Getenv("DUALSYNC_TEST_MEDIA_ROOT")
	if root == "" {
		t.Skip("set DUALSYNC_TEST_MEDIA_ROOT for mounted-media integration tests")
	}
	l, err := NewLibrary(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	paths := []string{
		"Media/Anime/[Judas] Enen no Shouboutai (Fire Force) (Season 03) [1080p][HEVC x265 10bit][Dual-Audio][Multi-Subs]/[Judas] Fire Force - S03E01v2.mkv",
		"Media/Anime/[Anime Time] Amagi Brilliant Park (Season 01+OVA+Specials) [BD] [Dual Audio][1080p][HEVC 10bit x265][AAC][Eng Sub]/[Anime Time] Amagi Brilliant Park - 01.mkv",
		"Media/Anime/Strike Witches/Brave Witches/OVA1 - Petersburg Grand Strategy.mkv",
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			p, err := l.Probe(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"sub", "dub"} {
				audio, subtitle := -1, -1
				for _, track := range p.Tracks {
					if track.Recommended == name {
						if track.Kind == "audio" && audio < 0 {
							audio = track.Index
						}
						if track.Kind == "subtitle" && subtitle < 0 {
							subtitle = track.Index
						}
					}
				}
				if subtitle < 0 && name == "dub" {
					for _, track := range p.Tracks {
						if track.Kind == "subtitle" {
							subtitle = track.Index
						}
					}
				}
				src, err := l.source(context.Background(), SourceSpec{Path: path, Audio: &audio, Subtitle: &subtitle}, name)
				if err != nil {
					t.Fatal(err)
				}
				side, err := NewSide(src, 80)
				if err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(50 * time.Second)
				for !side.Ready() {
					if time.Now().After(deadline) {
						side.Close()
						t.Fatal("fixture decoder timeout")
					}
					time.Sleep(50 * time.Millisecond)
				}
				frame, samples := side.Take()
				errText := side.Error()
				side.Close()
				if errText != "" {
					t.Fatal(errText)
				}
				if len(frame) != FrameBytes || len(samples) != AudioBytes {
					t.Fatalf("missing decoded frame/audio %s: %d %d", name, len(frame), len(samples))
				}
				t.Logf("%s audio=%d subtitle=%d codec=%s at 80 seconds", name, audio, subtitle, src.SubtitleCodec)
			}
		})
	}
}

func TestActualHTTPReadyPersistenceRestart(t *testing.T) {
	e, s, root := testEngine(t)
	path := fixture(t, root)
	body := `{"mode":"single","items":[{"sub":{"path":` + quoteJSON(path) + `},"dub":{"path":` + quoteJSON(path) + `}}]}`
	call(t, s, "POST", "/api/media/load", body, 202)
	waitPhase(t, e, "ready")
	c := e.Config
	other, err := NewEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	waitPhase(t, other, "ready")
	status := other.Snapshot()
	if status.Mode != "stopped" || len(status.Outputs) != 0 {
		t.Fatal("restart auto-published")
	}
}
func quoteJSON(value string) string { b, _ := json.Marshal(value); return string(b) }
