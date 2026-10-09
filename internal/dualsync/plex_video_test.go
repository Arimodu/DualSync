package dualsync

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func videoMock(t *testing.T, root, name string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	ranges := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "video-private-token" {
			w.WriteHeader(401)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/library/parts/") {
			if r.Header.Get("Range") != "" {
				ranges.Add(1)
			}
			http.ServeContent(w, r, "original.mkv", time.Unix(1, 0), bytes.NewReader(data))
			return
		}
		video := func(id string) map[string]any {
			file := filepath.Join(root, name)
			guid := "plex://episode/same"
			index := 1
			if id == "2" {
				file = "/not-mounted/video.mkv"
				index = 2
			}
			if id == "3" {
				guid = "plex://episode/different"
			}
			if id == "5" {
				guid = "local://unmatched"
			}
			part := map[string]any{"id": 20, "file": file, "key": "/library/parts/20/file.mkv", "size": len(data)}
			parts := []any{part}
			if id == "4" {
				parts = append(parts, part)
			}
			versions := []any{map[string]any{"id": 10, "videoResolution": "1080", "videoCodec": "ffv1", "Part": parts}}
			if id == "6" {
				versions = append(versions, map[string]any{"id": 11, "Part": []any{part}})
			}
			return map[string]any{"ratingKey": id, "type": "episode", "title": "Episode " + id, "grandparentTitle": "Test Show", "parentIndex": 1, "index": index, "guid": guid, "Media": versions}
		}
		if r.URL.Path == "/identity" {
			json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "video-test-server"}})
			return
		}
		if r.URL.Path == "/library/sections" {
			json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"Directory": []any{map[string]any{"key": "1", "title": "Anime", "type": "show"}, map[string]any{"key": "2", "title": "Music", "type": "artist"}}}})
			return
		}
		rows := []any{}
		switch r.URL.Path {
		case "/library/metadata/9":
			rows = append(rows, map[string]any{"ratingKey": "9", "type": "season", "title": "Season 1"})
		case "/library/metadata/9/children":
			rows = append(rows, video("1"), video("2"))
		case "/library/sections/1/all":
			rows = append(rows, map[string]any{"ratingKey": "8", "type": "show", "title": "Test Show"})
		default:
			id := strings.TrimPrefix(r.URL.Path, "/library/metadata/")
			if numericID(id) {
				rows = append(rows, video(id))
			} else {
				w.WriteHeader(404)
				return
			}
		}
		if start, err := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start")); err == nil {
			if start >= len(rows) {
				rows = []any{}
			} else {
				rows = rows[start:]
			}
		}
		if limit, err := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Size")); err == nil && limit < len(rows) {
			rows = rows[:limit]
		}
		json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"Metadata": rows}})
	}))
	t.Cleanup(server.Close)
	return server, ranges
}
func configureVideo(t *testing.T, e *Engine, p *httptest.Server) {
	t.Helper()
	e.mu.Lock()
	e.settings.PlexServer = p.URL
	e.settings.PlexToken = "video-private-token"
	e.mu.Unlock()
}
func TestPlexVideoHTTPAndMixedLoad(t *testing.T) {
	e, s, root := testEngine(t)
	name := fixture(t, root)
	p, ranges := videoMock(t, root, name)
	configureVideo(t, e, p)
	call(t, s, "GET", "/api/plex/libraries", "", 200)
	call(t, s, "GET", "/api/plex/videos?library_id=1", "", 200)
	call(t, s, "GET", "/api/plex/videos?parent_id=9&limit=1", "", 200)
	call(t, s, "GET", "/api/plex/videos?parent_id=9&cursor=1&limit=1", "", 200)
	call(t, s, "GET", "/api/plex/videos?parent_id=9&limit=0", "", 400)
	call(t, s, "GET", "/api/plex/video?id=6", "", 200)
	call(t, s, "GET", "/api/plex/video/probe?id=6", "", 400)
	call(t, s, "GET", "/api/plex/video/probe?id=6&media_id=11", "", 200)
	call(t, s, "GET", "/api/plex/video/probe?id=1&server_id=wrong", "", 409)
	call(t, s, "GET", "/api/plex/video/probe?id=4", "", 422)
	call(t, s, "GET", "/api/plex/video?id=..", "", 400)
	response := call(t, s, "GET", "/api/plex/video/probe?id=2", "", 200)
	if ranges.Load() == 0 || !strings.Contains(response.Body.String(), `"transport":"plex"`) || strings.Contains(response.Body.String(), "video-private-token") || strings.Contains(response.Body.String(), "/library/parts/") {
		t.Fatal("fallback, verified indices or redaction failed")
	}
	call(t, s, "POST", "/api/control", `{"action":"start"}`, 200)
	e.mu.Lock()
	original := e.outputs["sub"]
	e.mu.Unlock()
	body := fmt.Sprintf(`{"mode":"paired","items":[{"sub":{"plex":{"id":"1"}},"dub":{"path":%q}}]}`, name)
	call(t, s, "POST", "/api/media/load", body, 202)
	waitPhase(t, e, "ready")
	state := e.Snapshot()
	if state.Media.Metadata == nil || !state.Media.Metadata.Matched || !strings.Contains(state.Media.Title, "Test Show") {
		t.Fatal("matched Plex labels missing")
	}
	call(t, s, "GET", "/api/status", "", 200)
	call(t, s, "POST", "/api/media/load", `{"mode":"single","items":[{"sub":{"plex":{"id":"2"}},"dub":{"plex":{"id":"2"}}}]}`, 202)
	waitPhase(t, e, "ready")
	for _, action := range []string{`{"action":"play"}`, `{"action":"pause"}`, `{"action":"seek","value":10}`, `{"action":"seek","value":-10}`, `{"action":"seek","value":80}`} {
		call(t, s, "POST", "/api/control", action, 200)
		if strings.Contains(action, "seek") {
			waitPhase(t, e, "paused")
		}
	}
	e.mu.Lock()
	same := e.outputs["sub"] == original
	e.mu.Unlock()
	if !same {
		t.Fatal("publisher replaced by source controls")
	}
	call(t, s, "POST", "/api/media/load", `{"mode":"paired","items":[{"sub":{"plex":{"id":"1"}},"dub":{"plex":{"id":"3"}},"metadata_source":"dub"}]}`, 202)
	waitPhase(t, e, "ready")
	if e.Snapshot().Media.Metadata.ID != "3" {
		t.Fatal("metadata selection ignored")
	}
	call(t, s, "POST", "/api/media/load", `{"mode":"single","items":[{"sub":{"plex":{"id":"5"}},"dub":{"plex":{"id":"5"}}}]}`, 202)
	waitPhase(t, e, "ready")
	if e.Snapshot().Media.Metadata != nil {
		t.Fatal("unmatched metadata promoted")
	}
	call(t, s, "POST", "/api/media/load", `{"mode":"single","items":[{"sub":{"plex":{"id":"1"},"path":"anything"},"dub":{"plex":{"id":"1"},"path":"anything"}}]}`, 400)
	call(t, s, "POST", "/api/media/unload", `{}`, 200)
}
func TestPlexVideoPairingAndMetadataConflict(t *testing.T) {
	e, s, root := testEngine(t)
	name := fixture(t, root)
	p, _ := videoMock(t, root, name)
	configureVideo(t, e, p)
	os.Mkdir(filepath.Join(root, "Dub"), 0700)
	for _, ep := range []int{1, 3} {
		if err := os.Link(filepath.Join(root, name), filepath.Join(root, "Dub", fmt.Sprintf("S01E%02d.mkv", ep))); err != nil {
			t.Fatal(err)
		}
	}
	response := call(t, s, "POST", "/api/media/preview", `{"sub":{"plex_season_id":"9"},"dub":{"folder":"Dub"}}`, 200)
	var preview PlaylistPreview
	json.Unmarshal(response.Body.Bytes(), &preview)
	if len(preview.Items) != 1 || len(preview.Unmatched) != 2 || preview.Items[0].Sub.Plex.ID != "1" {
		t.Fatalf("pairing lost episode identity: %+v", preview)
	}
	call(t, s, "POST", "/api/media/preview", `{"sub":{"plex_season_id":"9"}}`, 200)
	call(t, s, "POST", "/api/media/preview", `{"sub":{"folder":".."}}`, 403)
	call(t, s, "POST", "/api/media/load", `{"mode":"paired","items":[{"sub":{"plex":{"id":"1"}},"dub":{"plex":{"id":"3"}}}]}`, 202)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e.Snapshot().Media.Operation.State == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(e.Snapshot().Media.Operation.Error, "metadata_source") {
		t.Fatal("conflict accepted or failed silently")
	}
	a := VideoMetadata{Matched: true, Identifiers: []string{"tmdb://1"}}
	b := VideoMetadata{Matched: true, Identifiers: []string{"tmdb://1"}}
	if _, err := itemMetadata(&a, &b, ""); err != nil {
		t.Fatal("matching external identity rejected")
	}
}
func TestPlexRangeProxyCancellationAndNoRangeServer(t *testing.T) {
	p, err := newVideoProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var gotToken atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken.Store(r.Header.Get("X-Plex-Token") == "secret")
		http.ServeContent(w, r, "test", time.Time{}, strings.NewReader("0123456789"))
	}))
	defer upstream.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	uri, err := p.register(ctx, newPlexClient(Settings{PlexServer: upstream.URL, PlexToken: "secret"}), "/library/parts/1/file.mkv")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", uri, nil)
	req.Header.Set("Range", "bytes=3-5")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 206 || string(body) != "345" || !gotToken.Load() {
		t.Fatal("range/auth proxy failed")
	}
	req, _ = http.NewRequest("HEAD", uri, nil)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Content-Length") != "10" {
		t.Fatal("HEAD size lost")
	}
	if _, err := p.register(ctx, newPlexClient(Settings{}), "//evil/anything"); err == nil {
		t.Fatal("foreign resource accepted")
	}
	cancel()
	res, err = http.Get(uri)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatal("cancelled resource retained")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("whole file")) }))
	defer bad.Close()
	life, stop := context.WithCancel(context.Background())
	defer stop()
	uri, _ = p.register(life, newPlexClient(Settings{PlexServer: bad.URL}), "/library/parts/2/file.mkv")
	req, _ = http.NewRequest("GET", uri, nil)
	req.Header.Set("Range", "bytes=0-0")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 502 {
		t.Fatal("unseekable server accepted")
	}
}

func TestPlexVideoRemoteSubtitleAndSeek(t *testing.T) {
	e, _, root := testEngine(t)
	name := fixture(t, root)
	ass := filepath.Join(root, "captions.ass")
	text := "[Script Info]\nScriptType: v4.00+\nPlayResX: 128\nPlayResY: 72\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\nStyle: Default,Arial,14,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,1,0,2,5,5,5,1\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: 0,0:00:00.00,0:00:04.00,Default,,0,0,0,,REMOTE CAPTION\n"
	if err := os.WriteFile(ass, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "with-captions.mkv")
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", filepath.Join(root, name), "-i", ass, "-map", "0", "-map", "1", "-c", "copy", "-metadata:s:s:0", "language=eng", "-y", file)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("subtitle fixture: %v %s", err, output)
	}
	p, _ := videoMock(t, root, filepath.Base(file))
	configureVideo(t, e, p)
	e.mu.Lock()
	settings := e.settings
	e.mu.Unlock()
	source, err := e.resolveVideo(context.Background(), e.ctx, settings, SourceSpec{Plex: &PlexSource{ID: "2"}}, "sub")
	if err != nil {
		t.Fatal(err)
	}
	cached, err := e.subtitles.prepare(e.ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	source.SubtitleFile = cached
	var captionFrame []byte
	for _, position := range []float64{0, 2} {
		side, err := NewSide(source, position)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !side.Ready() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		frame, audio := side.Take()
		if position == 0 {
			captionFrame = frame
		}
		side.Close()
		if len(frame) != FrameBytes || len(audio) != AudioBytes {
			t.Fatalf("remote seek %.0f failed: %s", position, side.Error())
		}
	}
	off := source
	off.Subtitle = -1
	off.Audio = 2
	side, err := NewSide(off, 0)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !side.Ready() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	plain, englishAudio := side.Take()
	side.Close()
	if len(plain) != FrameBytes || bytes.Equal(plain, captionFrame) {
		t.Fatal("remote ASS was not burned into output")
	}
	crossings := 0
	for i := 4; i < len(englishAudio); i += 4 {
		a := int16(binary.LittleEndian.Uint16(englishAudio[i-4:]))
		b := int16(binary.LittleEndian.Uint16(englishAudio[i:]))
		if a <= 0 && b > 0 {
			crossings++
		}
	}
	frequency := float64(crossings) * 48000 / float64(Samples)
	if frequency < 830 || frequency > 930 {
		t.Fatalf("remote English track mapping: %.1f Hz, expected 880", frequency)
	}
	second, err := e.subtitles.prepare(e.ctx, source)
	if err != nil || second != cached {
		t.Fatal("remote subtitle cache not reused")
	}
}

type interruptedVideoSide struct{ fakeSide }

func (*interruptedVideoSide) Error() string { return "remote test interruption" }
func TestPlexVideoInterruptionHoldsClockAndRecovers(t *testing.T) {
	e, _, _ := testEngine(t)
	e.mu.Lock()
	e.items = []Item{{Sub: Source{Spec: SourceSpec{Transport: "plex"}}, Duration: 100}}
	e.position = 30
	e.phase = "playing"
	e.outputs = map[string]Output{"sub": &fakeOutput{}, "dub": &fakeOutput{}}
	sub, dub := e.outputs["sub"], e.outputs["dub"]
	e.sides = map[string]MediaSide{"sub": &interruptedVideoSide{}, "dub": &fakeSide{end: 10000}}
	e.mu.Unlock()
	e.tick(time.Now())
	e.mu.Lock()
	if e.position != 30 || e.phase != "buffering" || !e.remoteRetry {
		e.mu.Unlock()
		t.Fatal("interruption advanced source clock")
	}
	e.retry["media"] = time.Now().Add(-time.Second)
	e.mu.Unlock()
	e.tick(time.Now())
	waitPhase(t, e, "playing")
	e.mu.Lock()
	same := e.outputs["sub"] == sub && e.outputs["dub"] == dub
	e.mu.Unlock()
	if !same {
		t.Fatal("recovery replaced publishers")
	}
}
func TestPlexVideoRetryRespectsPauseAndStop(t *testing.T) {
	e, _, _ := testEngine(t)
	e.mu.Lock()
	e.items = []Item{{Duration: 100}}
	e.phase = "buffering"
	e.position = 25
	e.remoteRetry = true
	e.remoteTarget = "playing"
	e.operation.State = "failed"
	e.retry["media"] = time.Now().Add(time.Hour)
	e.outputs = map[string]Output{"sub": &fakeOutput{}, "dub": &fakeOutput{}}
	e.mu.Unlock()
	if err := e.Command("pause", nil); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	target := e.remoteTarget
	e.retry["media"] = time.Now().Add(-time.Second)
	e.mu.Unlock()
	if target != "paused" {
		t.Fatal("pause lost during remote retry")
	}
	e.tick(time.Now())
	waitPhase(t, e, "paused")
	e.mu.Lock()
	e.remoteRetry = true
	e.remoteTarget = "playing"
	e.mu.Unlock()
	if err := e.Command("stop", nil); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	retry := e.remoteRetry
	count := len(e.sides)
	e.mu.Unlock()
	if retry || count != 0 {
		t.Fatal("stopped remote retry kept decoding")
	}
	if err := e.Command("start", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.Command("play", nil); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, e, "playing")
}

func TestLivePlexVideoOriginal(t *testing.T) {
	if os.Getenv("DUALSYNC_PLEX_VIDEO_LIVE") != "1" {
		t.Skip("requires Zeus Plex and shared media")
	}
	e, _, _ := testEngine(t)
	var settings Settings
	if err := ReadJSON("/var/lib/dualsync-dev/settings.json", &settings); err != nil {
		t.Fatal("live Plex settings unavailable")
	}
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Minute)
	defer cancel()
	for _, remote := range []bool{false, true} {
		if !remote {
			e.Library.Close()
			library, err := NewLibrary("/mnt/data-vault")
			if err != nil {
				t.Fatal(err)
			}
			e.Library = library
		} else {
			e.Library.Close()
			library, err := NewLibrary(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			e.Library = library
			e.subtitles = newSubtitleCache(t.TempDir())
		}
		for _, mapping := range []struct {
			side            string
			audio, subtitle int
		}{{"sub", 2, 4}, {"dub", 1, 3}} {
			start := time.Now()
			source, err := e.resolveVideo(ctx, ctx, settings, SourceSpec{Plex: &PlexSource{ID: "9875"}, Audio: &mapping.audio, Subtitle: &mapping.subtitle}, mapping.side)
			if err != nil {
				t.Fatal(err)
			}
			if remote && source.Spec.Transport != "plex" || !remote && source.Spec.Transport != "local" {
				t.Fatal("unexpected live transport")
			}
			cached, err := e.subtitles.prepare(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			source.SubtitleFile = cached
			cold := time.Since(start)
			for _, position := range []float64{0, 80, 70, 160} {
				start = time.Now()
				side, err := NewSide(source, position)
				if err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(15 * time.Second)
				for !side.Ready() && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				frame, audio := side.Take()
				side.Close()
				if len(frame) != FrameBytes || len(audio) != AudioBytes {
					t.Fatal("live original decode failed")
				}
				t.Logf("transport=%s side=%s position=%.0f cold_prepare=%s first_frame=%s", source.Spec.Transport, mapping.side, position, cold, time.Since(start))
			}
		}
	}
}
func TestPlexVideoMatchedIdentityRules(t *testing.T) {
	season := PlexVideo{Type: "season", Episode: 4, ParentTitle: "Example"}
	if season.entry().Season != 4 || season.entry().Episode != 0 || season.metadata().Season != 4 || season.metadata().Show != "Example" {
		t.Fatal("season index mapped as episode number")
	}
	for _, guid := range []string{"local://1", "com.plexapp.agents.none://1", "com.plexapp.agents.localmedia://1", ""} {
		if (PlexVideo{GUID: guid}).metadata().Matched {
			t.Fatalf("unmatched identity accepted: %s", guid)
		}
	}
	if !(PlexVideo{GUID: "plex://episode/1"}).metadata().Matched {
		t.Fatal("matched Plex identity lost")
	}
}
func TestVideoTrackSelectionAndSubtitleCacheBounds(t *testing.T) {
	bad := -2
	if validateSource(SourceSpec{Path: "example.mkv", Audio: &bad}) == nil {
		t.Fatal("negative audio index accepted")
	}
	index := 2
	off := -1
	probe := Probe{Duration: 10, Tracks: []Track{{Index: 0, Kind: "video", Attached: true}, {Index: 1, Kind: "video"}, {Index: 2, Kind: "video"}}}
	source, err := sourceFromProbe(SourceSpec{Video: &index, Audio: &off, Subtitle: &off}, probe, "unused", "sub")
	if err != nil || source.Video != 2 {
		t.Fatal("independent video track selection failed")
	}
	index = 99
	if _, err := sourceFromProbe(SourceSpec{Video: &index}, probe, "unused", "sub"); err == nil {
		t.Fatal("invalid video track accepted")
	}
	c := newSubtitleCache(t.TempDir())
	os.Mkdir(c.root, 0700)
	active := filepath.Join(c.root, "active.mks")
	old := filepath.Join(c.root, "old.mks")
	for _, path := range []string{active, old} {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.Truncate(600 << 20); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.hold(ctx, active)
	c.mu.Lock()
	room := c.makeRoom(100 << 20)
	c.mu.Unlock()
	if !room {
		t.Fatal("inactive cache was not evicted")
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatal("active subtitle cache evicted")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("cache exceeded bound")
	}
	c.mu.Lock()
	room = c.makeRoom(600 << 20)
	c.mu.Unlock()
	if room {
		t.Fatal("pinned cache exceeded limit")
	}
}
func TestLivePlexTransportBitmapSubtitle(t *testing.T) {
	if os.Getenv("DUALSYNC_PLEX_VIDEO_LIVE") != "1" {
		t.Skip("requires Zeus bitmap subtitle fixture")
	}
	path := "/mnt/data-vault/Media/Anime/Strike Witches/Brave Witches/OVA1 - Petersburg Grand Strategy.mkv"
	probe, err := probeFile(context.Background(), path)
	if err != nil {
		t.Fatal("bitmap fixture unavailable")
	}
	audio, subtitle := -1, -1
	for _, track := range probe.Tracks {
		if audio < 0 && track.Kind == "audio" {
			audio = track.Index
		}
		if track.Codec == "hdmv_pgs_subtitle" {
			subtitle = track.Index
			break
		}
	}
	if subtitle < 0 {
		t.Fatal("bitmap fixture has no PGS")
	}
	e, _, _ := testEngine(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "pgs-test-token" {
			w.WriteHeader(401)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/library/parts/") {
			http.ServeFile(w, r, path)
			return
		}
		if r.URL.Path == "/identity" {
			json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "pgs-test"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"Metadata": []any{map[string]any{"ratingKey": "1", "type": "episode", "title": "PGS fixture", "guid": "plex://episode/pgs-test", "Media": []any{map[string]any{"id": 10, "Part": []any{map[string]any{"id": 20, "file": "/unmounted/pgs.mkv", "key": "/library/parts/20/file.mkv"}}}}}}}})
	}))
	defer upstream.Close()
	settings := Settings{PlexServer: upstream.URL, PlexToken: "pgs-test-token"}
	source, err := e.resolveVideo(e.ctx, e.ctx, settings, SourceSpec{Plex: &PlexSource{ID: "1"}, Audio: &audio, Subtitle: &subtitle}, "sub")
	if err != nil {
		t.Fatal(err)
	}
	if source.Spec.Transport != "plex" || source.SubtitleCodec != "hdmv_pgs_subtitle" {
		t.Fatal("remote PGS mapping lost")
	}
	for _, position := range []float64{80, 70} {
		start := time.Now()
		side, err := NewSide(source, position)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for !side.Ready() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		frame, audio := side.Take()
		side.Close()
		if len(frame) != FrameBytes || len(audio) != AudioBytes {
			t.Fatal("remote bitmap decoding failed")
		}
		t.Logf("PGS original HTTP position=%.0f first_frame=%s", position, time.Since(start))
	}
}
