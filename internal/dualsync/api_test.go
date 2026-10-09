package dualsync

import (
	"bytes"
	"context"
	"dualsync/api"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type fakeOutput struct {
	closed   atomic.Bool
	failed   atomic.Bool
	notReady atomic.Bool
	frames   atomic.Int64
}

func (p *fakeOutput) Close() { p.closed.Store(true) }
func (p *fakeOutput) Status() OutputStatus {
	return OutputStatus{Ready: !p.closed.Load() && !p.notReady.Load(), Encoder: "cpu"}
}
func (p *fakeOutput) CanAccept() bool       { return !p.closed.Load() }
func (p *fakeOutput) Enqueue(v, a []byte)   { p.frames.Add(1) }
func (p *fakeOutput) Failed(time.Time) bool { return p.failed.Load() }

type fakeSide struct {
	frame []byte
	count int
	end   int
}

func (s *fakeSide) Close()                 {}
func (s *fakeSide) Ready() bool            { return true }
func (s *fakeSide) EOF() bool              { return s.count >= s.end }
func (s *fakeSide) Error() string          { return "" }
func (s *fakeSide) Frame() []byte          { return s.frame }
func (s *fakeSide) Take() ([]byte, []byte) { s.count++; return s.frame, silence }

func testEngine(t *testing.T) (*Engine, *Server, string) {
	t.Helper()
	root := t.TempDir()
	state := t.TempDir()
	web := t.TempDir()
	os.WriteFile(filepath.Join(web, "index.html"), []byte("temporary client"), 0600)
	c := Defaults(t.TempDir())
	c.MediaRoot = root
	c.StateDir = state
	c.WebRoot = web
	c.AllowUnsupported = true
	c.Settings.SubServer = "rtmp://127.0.0.1:1935"
	c.Settings.DubServer = "rtsp://127.0.0.1:8554"
	e, err := NewEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	e.mu.Lock()
	e.factory = func(Settings, string, Encoder) (Output, error) { return &fakeOutput{}, nil }
	e.decoder = func(s Source, p float64) (MediaSide, error) {
		return &fakeSide{frame: make([]byte, FrameBytes), end: 10000}, nil
	}
	e.mu.Unlock()
	return e, NewServer(e, api.Spec), root
}
func TestPackagedFrontend(t *testing.T) {
	e, _, _ := testEngine(t)
	e.Config.WebRoot = filepath.Join("..", "..", "webroot")
	s := NewServer(e, api.Spec)
	for path, contentType := range map[string]string{
		"/": "text/html", "/app.js": "text/javascript", "/app.css": "text/css",
	} {
		res := call(t, s, "GET", path, "", 200)
		if !strings.HasPrefix(res.Header().Get("Content-Type"), contentType) {
			t.Fatalf("%s content type: %s", path, res.Header().Get("Content-Type"))
		}
		if res.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s missing cache revalidation", path)
		}
	}
}
func call(t *testing.T, s *Server, method, path, body string, code int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	s.ServeHTTP(res, req)
	if res.Code != code {
		t.Fatalf("%s %s: %d want %d: %s", method, path, res.Code, code, res.Body.String())
	}
	if strings.HasPrefix(res.Header().Get("Content-Type"), "application/json") {
		validateResponse(t, method, path, body, res)
	}
	return res
}
func validateResponse(t *testing.T, method, path, body string, res *httptest.ResponseRecorder) {
	t.Helper()
	var spec map[string]any
	if err := json.Unmarshal(api.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	urlPath := strings.Split(path, "?")[0]
	if strings.HasPrefix(urlPath, "/api/music/art/") {
		urlPath = "/api/music/art/{id}"
	}
	paths := spec["paths"].(map[string]any)
	endpoint, ok := paths[urlPath].(map[string]any)
	if !ok {
		return
	}
	op, ok := endpoint[strings.ToLower(method)].(map[string]any)
	if !ok {
		return
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://dualsync.test/spec", spec); err != nil {
		t.Fatal(err)
	}
	responses := op["responses"].(map[string]any)
	response, ok := responses[fmt.Sprint(res.Code)].(map[string]any)
	if !ok {
		response = responses["default"].(map[string]any)
	}
	schema := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"]
	if err := compiler.AddResource("https://dualsync.test/response", schema); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(schema)
	raw = bytes.ReplaceAll(raw, []byte(`"#/components/`), []byte(`"https://dualsync.test/spec#/components/`))
	var standalone any
	json.Unmarshal(raw, &standalone)
	compiler.AddResource("https://dualsync.test/checked", standalone)
	checked, err := compiler.Compile("https://dualsync.test/checked")
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err = json.Unmarshal(res.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if err = checked.Validate(value); err != nil {
		t.Fatalf("response contract %s: %v", path, err)
	}
	if res.Code < 300 && body != "" {
		if rb, ok := op["requestBody"].(map[string]any); ok {
			schema = rb["content"].(map[string]any)["application/json"].(map[string]any)["schema"]
			raw, _ = json.Marshal(schema)
			raw = bytes.ReplaceAll(raw, []byte(`"#/components/`), []byte(`"https://dualsync.test/spec#/components/`))
			json.Unmarshal(raw, &standalone)
			compiler.AddResource("https://dualsync.test/request", standalone)
			checked, err = compiler.Compile("https://dualsync.test/request")
			if err != nil {
				t.Fatal(err)
			}
			json.Unmarshal([]byte(body), &value)
			if err = checked.Validate(value); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestSettingsPersistenceAndConcurrentCommands(t *testing.T) {
	e, s, _ := testEngine(t)
	call(t, s, "POST", "/api/settings", `{"sub_password":"persist-secret","music_volume":42}`, 200)
	var saved Settings
	if err := ReadJSON(filepath.Join(e.Config.StateDir, "settings.json"), &saved); err != nil || saved.SubPassword != "persist-secret" || saved.MusicVolume != 42 {
		t.Fatal("settings not persisted")
	}
	call(t, s, "POST", "/api/settings", `{"sub_password":""}`, 200)
	if e.settings.SubPassword != "persist-secret" {
		t.Fatal("blank password erased secret")
	}
	call(t, s, "POST", "/api/control", `{"action":"start"}`, 200)
	call(t, s, "POST", "/api/diagnostics", `{}`, 409)
	jobs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			body := map[string]json.RawMessage{"hud_enabled": json.RawMessage(fmt.Sprint(i%2 == 0))}
			jobs <- e.UpdateSettings(body)
		}(i)
	}
	for i := 0; i < 20; i++ {
		if err := <-jobs; err != nil {
			t.Fatal(err)
		}
	}
	call(t, s, "POST", "/api/control", `{"action":"stop"}`, 200)
	call(t, s, "POST", "/api/control", `{"action":"rescan"}`, 200)
	info, err := os.Stat(filepath.Join(e.Config.StateDir, "settings.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private settings permissions")
	}
	req := httptest.NewRequest("POST", "/api/control", strings.NewReader(`{"action":"start"}`))
	req.RemoteAddr = "127.0.0.1:1"
	res := httptest.NewRecorder()
	s.ServeHTTP(res, req)
	if res.Code != 415 {
		t.Fatal("wrong content type accepted")
	}
}

func TestExternalFrontendSameOriginProxy(t *testing.T) {
	_, s, _ := testEngine(t)
	s.Engine.Config.NoWebUI = true
	backend := httptest.NewServer(s)
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			proxy.ServeHTTP(w, r)
			return
		}
		w.Write([]byte("separate frontend"))
	}))
	defer frontend.Close()
	request, _ := http.NewRequest("POST", frontend.URL+"/api/control", strings.NewReader(`{"action":"start"}`))
	request.Header.Set("Origin", frontend.URL)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 {
		t.Fatalf("proxy rejected same origin: %d %s", response.StatusCode, body)
	}
	response, err = http.Get(frontend.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ = io.ReadAll(response.Body)
	if string(body) != "separate frontend" {
		t.Fatal("external frontend not served")
	}
}
func waitPhase(t *testing.T, e *Engine, phase string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		s := e.Snapshot()
		if s.Media.Phase == phase {
			return
		}
		if s.Media.Operation.State == "failed" {
			t.Fatalf("load failed: %+v", s.Media.Operation)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("phase %s, status %+v", phase, e.Snapshot().Media)
}
func fixture(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "日本語 S01E01.mkv")
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=red:size=128x72:rate=24:duration=4", "-f", "lavfi", "-i", "sine=frequency=440:duration=4", "-f", "lavfi", "-i", "sine=frequency=880:duration=4", "-map", "0:v", "-map", "1:a", "-map", "2:a", "-metadata:s:a:0", "language=jpn", "-metadata:s:a:1", "language=eng", "-c:v", "ffv1", "-c:a", "pcm_s16le", "-y", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, output)
	}
	return filepath.Base(path)
}
func TestHTTPContractAndConfinement(t *testing.T) {
	e, s, root := testEngine(t)
	path := fixture(t, root)
	call(t, s, "GET", "/api/status", "", 200)
	call(t, s, "GET", "/api/library?limit=1", "", 200)
	call(t, s, "GET", "/api/library?path=..", "", 403)
	call(t, s, "GET", "/api/library?path=%2Fetc", "", 400)
	call(t, s, "GET", "/api/library?cursor=no", "", 400)
	call(t, s, "GET", "/api/library?limit=0", "", 400)
	call(t, s, "GET", "/api/library?path=missing", "", 404)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	call(t, s, "GET", "/api/library?path=escape", "", 403)
	call(t, s, "GET", "/api/library/probe?path="+strings.ReplaceAll(path, " ", "%20"), "", 200)
	call(t, s, "GET", "/api/diagnostics", "", 200)
	call(t, s, "POST", "/api/control", `{"action":"play"}`, 409)
	call(t, s, "POST", "/api/control", `{"action":"bogus"}`, 400)
	call(t, s, "POST", "/api/control", `{"action":"seek","value":2}`, 400)
	call(t, s, "POST", "/api/control", `{"action":"start","extra":true}`, 400)
	call(t, s, "POST", "/api/settings", `{"sub_password":"secret-fixture","dub_password":"other-fixture"}`, 200)
	res := call(t, s, "GET", "/api/status", "", 200)
	if strings.Contains(res.Body.String(), "secret-fixture") {
		t.Fatal("secret leaked")
	}
	call(t, s, "POST", "/api/settings", `{"music_volume":101}`, 400)
	call(t, s, "POST", "/api/settings", `{"stream_path":"../bad"}`, 400)
	call(t, s, "POST", "/api/settings", `{"sub_server":"rtmp://host/path"}`, 400)
	call(t, s, "POST", "/api/settings", `{"bogus":true}`, 400)
	load := fmt.Sprintf(`{"mode":"single","items":[{"sub":{"path":%q,"audio":1,"subtitle":-1},"dub":{"path":%q,"audio":2,"subtitle":-1}}]}`, path, path)
	call(t, s, "POST", "/api/media/load", load, 202)
	waitPhase(t, e, "ready")
	call(t, s, "POST", "/api/control", `{"action":"start"}`, 200)
	call(t, s, "POST", "/api/settings", `{"stream_path":"different"}`, 409)
	call(t, s, "POST", "/api/settings", `{"hud_enabled":true}`, 200)
	e.mu.Lock()
	first := e.outputs["sub"].(*fakeOutput)
	second := e.outputs["dub"].(*fakeOutput)
	e.mu.Unlock()
	call(t, s, "POST", "/api/control", `{"action":"play"}`, 200)
	waitPhase(t, e, "playing")
	call(t, s, "POST", "/api/control", `{"action":"pause"}`, 200)
	waitPhase(t, e, "paused")
	for _, offset := range []int{-10, 10, 80} {
		call(t, s, "POST", "/api/control", fmt.Sprintf(`{"action":"seek","value":%d}`, offset), 200)
		waitPhase(t, e, "paused")
	}
	call(t, s, "POST", "/api/media/unload", `{}`, 200)
	waitPhase(t, e, "none")
	if first.closed.Load() || second.closed.Load() {
		t.Fatal("transport command disconnected publisher")
	}
	call(t, s, "POST", "/api/control", `{"action":"stop"}`, 200)
	if !first.closed.Load() || !second.closed.Load() {
		t.Fatal("stop retained publisher")
	}
	call(t, s, "GET", "/api/music/art/invalid", "", 404)
	call(t, s, "GET", "/api/openapi.yaml", "", 200)
	call(t, s, "GET", "/", "", 200)
	e.Config.NoWebUI = true
	call(t, s, "GET", "/", "", 404)
	call(t, s, "GET", "/api/status", "", 200)
	req := httptest.NewRequest("POST", "/api/control", strings.NewReader(`{"action":"start"}`))
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Origin", "https://elsewhere.test")
	res = httptest.NewRecorder()
	s.ServeHTTP(res, req)
	if res.Code != 403 {
		t.Fatal("cross-origin accepted")
	}
	req = httptest.NewRequest("GET", "/api/status", nil)
	req.RemoteAddr = "8.8.8.8:1"
	res = httptest.NewRecorder()
	s.ServeHTTP(res, req)
	if res.Code != 403 {
		t.Fatal("public network accepted")
	}
}
func TestInvalidTrackAndCancelledLoad(t *testing.T) {
	e, s, root := testEngine(t)
	path := fixture(t, root)
	body := fmt.Sprintf(`{"mode":"single","items":[{"sub":{"path":%q,"audio":99},"dub":{"path":%q}}]}`, path, path)
	call(t, s, "POST", "/api/media/load", body, 202)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && e.Snapshot().Media.Operation.State != "failed" {
		time.Sleep(20 * time.Millisecond)
	}
	if e.Snapshot().Media.Operation.State != "failed" {
		t.Fatal("invalid track accepted")
	}
	body = fmt.Sprintf(`{"mode":"single","items":[{"sub":{"path":%q},"dub":{"path":%q}}]}`, path, path)
	for i := 0; i < 10; i++ {
		call(t, s, "POST", "/api/media/load", body, 202)
		call(t, s, "POST", "/api/media/unload", `{}`, 200)
	}
	time.Sleep(200 * time.Millisecond)
	if e.Snapshot().Media.Phase != "none" {
		t.Fatal("stale load won cancellation")
	}
}
func TestLibraryPairingAndSearch(t *testing.T) {
	_, s, root := testEngine(t)
	for _, folder := range []string{"A", "B"} {
		os.Mkdir(filepath.Join(root, folder), 0700)
	}
	for _, path := range []string{"A/Show S01E2.mkv", "A/Show S01E10.mkv", "A/Show S01E3.mkv", "A/Other S01E3.mkv", "B/Dub S01E2.mkv", "B/Dub S01E10.mkv", "B/Dub S01E4.mkv"} {
		os.WriteFile(filepath.Join(root, path), nil, 0600)
	}
	res := call(t, s, "POST", "/api/library/playlist", `{"sub_folder":"A","dub_folder":"B"}`, 200)
	var preview PlaylistPreview
	json.Unmarshal(res.Body.Bytes(), &preview)
	if len(preview.Items) != 2 || !strings.Contains(preview.Items[0].Sub.Path, "E2") {
		t.Fatalf("pairing %+v", preview)
	}
	for i := 0; i < 30; i++ {
		res = call(t, s, "GET", "/api/library?q=Show", "", 200)
		var listing Listing
		json.Unmarshal(res.Body.Bytes(), &listing)
		if !listing.Indexing {
			if len(listing.Entries) != 3 {
				t.Fatalf("search %+v", listing)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("index did not finish")
}
func TestActualDecodersDifferentAudio(t *testing.T) {
	root := t.TempDir()
	path := fixture(t, root)
	l, err := NewLibrary(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	a := 1
	b := 2
	off := -1
	srcA, err := l.source(context.Background(), SourceSpec{Path: path, Audio: &a, Subtitle: &off}, "sub")
	if err != nil {
		t.Fatal(err)
	}
	srcB, err := l.source(context.Background(), SourceSpec{Path: path, Audio: &b, Subtitle: &off}, "dub")
	if err != nil {
		t.Fatal(err)
	}
	sideA, err := NewSide(srcA, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer sideA.Close()
	sideB, err := NewSide(srcB, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer sideB.Close()
	deadline := time.Now().Add(10 * time.Second)
	for !sideA.Ready() || !sideB.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("decoders not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	v, aa := sideA.Take()
	_, bb := sideB.Take()
	if len(v) != FrameBytes || len(aa) != AudioBytes || bytes.Equal(aa, bb) {
		t.Fatal("dual track decode failed")
	}
}

func TestSubtitleBurnAfterSeekWithEscapedFilename(t *testing.T) {
	root := t.TempDir()
	base := fixture(t, root)
	srt := filepath.Join(root, "captions.srt")
	os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:02,000\nFIRST CAPTION\n\n2\n00:00:02,000 --> 00:00:04,000\nSECOND CAPTION\n"), 0600)
	name := "quote' [a],; video.mkv"
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", filepath.Join(root, base), "-i", srt, "-map", "0", "-map", "1", "-c", "copy", "-c:s", "ass", "-metadata:s:s:0", "language=eng", "-y", filepath.Join(root, name))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("subtitle fixture %v %s", err, out)
	}
	l, err := NewLibrary(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	on := 3
	off := -1
	with, err := l.source(context.Background(), SourceSpec{Path: name, Audio: &off, Subtitle: &on}, "sub")
	if err != nil {
		t.Fatal(err)
	}
	without := with
	without.Subtitle = -1
	cache := newSubtitleCache(t.TempDir())
	cached, err := cache.prepare(context.Background(), with)
	if err != nil {
		t.Fatal(err)
	}
	cachedSource := with
	cachedSource.SubtitleFile = cached
	st, err := os.Stat(cached)
	if err != nil {
		t.Fatal(err)
	}
	again, err := cache.prepare(context.Background(), with)
	if err != nil || again != cached {
		t.Fatal("subtitle cache not reused", err)
	}
	stAgain, _ := os.Stat(again)
	if !os.SameFile(st, stAgain) {
		t.Fatal("subtitle cache regenerated")
	}
	for _, position := range []float64{.5, 3} {
		a, err := NewSide(with, position)
		if err != nil {
			t.Fatal(err)
		}
		b, err := NewSide(without, position)
		if err != nil {
			a.Close()
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for !a.Ready() || !b.Ready() {
			if time.Now().After(deadline) {
				a.Close()
				b.Close()
				t.Fatal("subtitle decoder timeout")
			}
			time.Sleep(10 * time.Millisecond)
		}
		av, _ := a.Take()
		bv, _ := b.Take()
		errText := a.Error()
		a.Close()
		b.Close()
		if errText != "" {
			t.Fatal(errText)
		}
		if len(av) != FrameBytes || bytes.Equal(av, bv) {
			t.Fatalf("subtitle absent at %f", position)
		}
		c, err := NewSide(cachedSource, position)
		if err != nil {
			t.Fatal(err)
		}
		deadline = time.Now().Add(10 * time.Second)
		for !c.Ready() {
			if time.Now().After(deadline) {
				c.Close()
				t.Fatal("cached subtitle decoder timeout")
			}
			time.Sleep(10 * time.Millisecond)
		}
		cv, _ := c.Take()
		c.Close()
		if !bytes.Equal(av, cv) {
			t.Fatalf("cached subtitles changed frame at %f", position)
		}
	}
	f, err := os.OpenFile(with.File, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte{0})
	_ = f.Close()
	next, err := cache.prepare(context.Background(), with)
	if err != nil || next == cached {
		t.Fatal("changed media did not invalidate subtitle cache", err)
	}
}
