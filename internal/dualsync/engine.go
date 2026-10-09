package dualsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type MediaSide interface {
	Close()
	Ready() bool
	EOF() bool
	Error() string
	Take() ([]byte, []byte)
	Frame() []byte
}
type Output interface {
	Close()
	Status() OutputStatus
	CanAccept() bool
	Enqueue([]byte, []byte)
	Failed(time.Time) bool
}
type Operation struct {
	ID    uint64 `json:"id"`
	State string `json:"state"`
	Error string `json:"error"`
}
type MediaState struct {
	Phase     string         `json:"phase"`
	Mode      string         `json:"mode"`
	Title     string         `json:"title"`
	Index     int            `json:"index"`
	Total     int            `json:"total"`
	Position  float64        `json:"position"`
	Duration  float64        `json:"duration"`
	Items     []ItemSpec     `json:"items"`
	Operation Operation      `json:"operation"`
	Metadata  *VideoMetadata `json:"metadata,omitempty"`
}
type RenderStats struct {
	P95MS   float64 `json:"p95_ms"`
	Samples int     `json:"samples"`
}
type Status struct {
	Revision     uint64                  `json:"revision"`
	Mode         string                  `json:"mode"`
	Buffering    bool                    `json:"buffering"`
	Episode      any                     `json:"episode"`
	Position     float64                 `json:"position"`
	Duration     float64                 `json:"duration"`
	Episodes     []map[string]any        `json:"episodes"`
	Settings     map[string]any          `json:"settings"`
	MusicTrack   string                  `json:"music_track"`
	Error        string                  `json:"error"`
	PauseSeconds float64                 `json:"pause_seconds"`
	Outputs      map[string]OutputStatus `json:"outputs"`
	Media        MediaState              `json:"media"`
	Music        MusicInfo               `json:"music"`
	Diagnostics  Report                  `json:"diagnostics"`
	Renderer     RenderStats             `json:"renderer"`
}
type savedPosition struct {
	Load     LoadRequest `json:"load"`
	Index    int         `json:"index"`
	Position float64     `json:"position"`
}
type Engine struct {
	mu                     sync.Mutex
	Library                *Library
	Config                 Config
	settings               Settings
	renderer               *Renderer
	music                  *Music
	musicInfo              MusicInfo
	items                  []Item
	load                   LoadRequest
	index                  int
	position               float64
	phase, targetPhase     string
	generation             uint64
	pauseGeneration        uint64
	operation              Operation
	revision               uint64
	err                    string
	pausedAt               time.Time
	resumedAt              time.Time // when Play left a pause; the video waits resumeHold for the exit transition
	clock                  float64
	sides                  map[string]MediaSide
	outputs                map[string]Output
	pauseFrames, pauseBlur map[string][]byte
	diagnostics            Report
	encoder                Encoder
	retry                  map[string]time.Time
	ctx                    context.Context
	cancel                 context.CancelFunc
	cancelLoad             context.CancelFunc
	lastSave               time.Time
	factory                func(Settings, string, Encoder) (Output, error)
	decoder                func(Source, float64) (MediaSide, error)
	subtitles              *subtitleCache
	videoProxy             *videoProxy
	cancelSources          context.CancelFunc
	remoteRetry            bool
	remoteTarget           string
}

func NewEngine(c Config) (*Engine, error) {
	l, err := NewLibrary(c.MediaRoot)
	if err != nil {
		return nil, err
	}
	if err = c.Settings.Validate(); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(c.StateDir, 0700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{Library: l, Config: c, settings: c.Settings, renderer: NewRenderer(c.Assets), ctx: ctx, cancel: cancel, phase: "none", sides: map[string]MediaSide{}, outputs: map[string]Output{}, pauseFrames: map[string][]byte{}, pauseBlur: map[string][]byte{}, retry: map[string]time.Time{}}
	e.factory = func(s Settings, side string, enc Encoder) (Output, error) { return NewPublisher(s, side, enc) }
	e.subtitles = newSubtitleCache(c.StateDir)
	e.decoder = func(s Source, p float64) (MediaSide, error) {
		if textSubtitle(s) {
			if file, err := e.subtitles.prepare(e.ctx, s); err == nil {
				s.SubtitleFile = file
			} else if s.Spec.Transport == "plex" {
				return nil, err
			}
		}
		return NewSide(s, p)
	}
	if err = ReadJSON(filepath.Join(c.StateDir, "settings.json"), &e.settings); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err = e.settings.Validate(); err != nil {
		return nil, err
	}
	if ReadJSON(filepath.Join(c.StateDir, "diagnostics.json"), &e.diagnostics) != nil {
		e.diagnostics = Inventory()
	}
	if e.diagnostics.Fingerprint != "" && e.diagnostics.Fingerprint != Inventory().Fingerprint {
		e.diagnostics.Supported = false
		e.diagnostics.CPUFallback = false
		e.diagnostics.Warning = "Hardware or FFmpeg changed; repeat encoding qualification"
	}
	if e.diagnostics.Tests == nil {
		e.diagnostics.Tests = []EncoderTest{}
	}
	if e.diagnostics.Missing == nil {
		e.diagnostics.Missing = []string{}
	}
	e.operation.State = "idle"
	e.encoder = e.diagnostics.Selected
	if c.Encoder != "auto" && c.Encoder != "" {
		e.encoder = Encoder{c.Encoder, c.Device}
		qualified := false
		for _, test := range e.diagnostics.Tests {
			if test.Passed && test.Encoder.Kind == c.Encoder && (c.Device == "" || test.Encoder.Device == c.Device) {
				qualified = true
				e.encoder.Device = test.Encoder.Device
			}
		}
		if !qualified {
			e.diagnostics.Supported = false
			e.diagnostics.Warning = "Selected encoder/device is not qualified"
		}
	}
	if e.encoder.Kind == "" {
		e.encoder = Encoder{Kind: "cpu"}
	}
	if e.settings.MusicSource == "" {
		e.settings.MusicSource = "folder"
	}
	e.videoProxy, err = newVideoProxy()
	if err != nil {
		cancel()
		l.Close()
		return nil, err
	}
	e.music = NewConfiguredMusic(l, c.StateDir, e.settings)
	var saved savedPosition
	if ReadJSON(filepath.Join(c.StateDir, "position.json"), &saved) == nil && len(saved.Load.Items) > 0 {
		saved.Load.StartIndex = saved.Index
		_ = e.Load(saved.Load, saved.Position)
	}
	go e.run()
	return e, nil
}
func (e *Engine) Close() {
	e.cancel()
	if e.videoProxy != nil {
		e.videoProxy.Close()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cancelOperation()
	e.closeSides()
	for _, p := range e.outputs {
		p.Close()
	}
	e.outputs = map[string]Output{}
	e.music.Close()
	e.Library.Close()
	e.save()
}
func (e *Engine) cancelOperation() {
	e.generation++
	if e.cancelLoad != nil {
		e.cancelLoad()
		e.cancelLoad = nil
	}
}
func (e *Engine) closeSides() {
	for _, s := range e.sides {
		s.Close()
	}
	e.sides = map[string]MediaSide{}
}
func (e *Engine) save() {
	_ = SaveJSON(filepath.Join(e.Config.StateDir, "position.json"), savedPosition{e.load, e.index, e.position})
	e.lastSave = time.Now()
}
func (e *Engine) Load(r LoadRequest, position float64) error {
	if r.Mode != "single" && r.Mode != "paired" {
		return fail("INVALID_ARGUMENT", 400, "mode must be single or paired")
	}
	if len(r.Items) == 0 || len(r.Items) > 500 || r.StartIndex < 0 || r.StartIndex >= len(r.Items) {
		return fail("INVALID_ARGUMENT", 400, "provide 1..500 items and a valid start_index")
	}
	for _, item := range r.Items {
		if r.Mode == "single" && !sameSource(item.Sub, item.Dub) {
			return fail("INVALID_ARGUMENT", 400, "single mode requires identical media sources")
		}
		if item.MetadataSource != "" && item.MetadataSource != "sub" && item.MetadataSource != "dub" {
			return fail("INVALID_ARGUMENT", 400, "metadata_source must be sub or dub")
		}
		for _, source := range []SourceSpec{item.Sub, item.Dub} {
			if err := validateSource(source); err != nil {
				return err
			}
			if source.Plex == nil {
				if _, err := e.Library.Resolve(source.Path); err != nil {
					return err
				}
			}
		}
	}
	e.mu.Lock()
	e.cancelOperation()
	e.closeSides()
	if e.cancelSources != nil {
		e.cancelSources()
	}
	life, cancelSources := context.WithCancel(e.ctx)
	e.cancelSources = cancelSources
	settings := e.settings
	e.items = nil
	e.load = r
	e.index = r.StartIndex
	e.position = position
	e.err = ""
	e.phase = "loading"
	e.remoteRetry = false
	e.targetPhase = "ready"
	e.operation = Operation{ID: e.generation, State: "running"}
	e.revision++
	gen := e.generation
	ctx, cancel := context.WithCancel(e.ctx)
	e.cancelLoad = cancel
	e.mu.Unlock()
	go func() {
		items := make([]Item, 0, len(r.Items))
		for _, spec := range r.Items {
			a, err := e.resolveVideo(ctx, life, settings, spec.Sub, "sub")
			if err != nil {
				cancelSources()
				e.loadError(gen, err)
				return
			}
			b, err := e.resolveVideo(ctx, life, settings, spec.Dub, "dub")
			if err != nil {
				cancelSources()
				e.loadError(gen, err)
				return
			}
			spec.Metadata, err = itemMetadata(a.Metadata, b.Metadata, spec.MetadataSource)
			if err != nil {
				cancelSources()
				e.loadError(gen, err)
				return
			}
			if spec.Title == "" && spec.Metadata != nil {
				spec.Title = metadataTitle(spec.Metadata)
			}
			if spec.Title == "" {
				spec.Title = filepath.Base(spec.Sub.Path)
				if spec.Sub.Plex != nil && a.Metadata != nil {
					spec.Title = a.Metadata.Title
				}
			}
			spec.Sub = a.Spec
			spec.Dub = b.Spec
			items = append(items, Item{spec, a, b, max(a.Duration, b.Duration)})
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if gen != e.generation {
			return
		}
		e.items = items
		for i := range items {
			e.load.Items[i] = items[i].Spec
		}
		e.position = min(max(0, e.position), max(0, e.current().Duration-1/FPS))
		e.startDecodeLocked("ready")
	}()
	return nil
}
func (e *Engine) loadError(gen uint64, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if gen != e.generation {
		return
	}
	e.err = Redact(err.Error())
	e.operation.State = "failed"
	e.operation.Error = e.err
	e.phase = "none"
	if e.remoteRetry {
		e.phase = "buffering"
		e.retry["media"] = time.Now().Add(2 * time.Second)
	}
	e.revision++
}
func (e *Engine) current() *Item {
	if e.index >= 0 && e.index < len(e.items) {
		return &e.items[e.index]
	}
	return nil
}
func (e *Engine) startDecodeLocked(target string) {
	if e.remoteRetry {
		e.remoteTarget = target
	}
	e.err = ""
	e.cancelOperation()
	e.closeSides()
	e.targetPhase = target
	gen := e.generation
	e.operation = Operation{ID: gen, State: "running"}
	item := *e.current()
	position := e.position
	e.subtitles.warm(e.ctx, item.Sub)
	e.subtitles.warm(e.ctx, item.Dub)
	ctx, cancel := context.WithCancel(e.ctx)
	e.cancelLoad = cancel
	go func() {
		a, err := e.decoder(item.Sub, position)
		if err != nil {
			e.loadError(gen, err)
			return
		}
		b, err := e.decoder(item.Dub, position)
		if err != nil {
			a.Close()
			e.loadError(gen, err)
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if ctx.Err() != nil || gen != e.generation {
			a.Close()
			b.Close()
			return
		}
		e.sides = map[string]MediaSide{"sub": a, "dub": b}
		if e.index+1 < len(e.items) {
			next := e.items[e.index+1]
			e.subtitles.warm(e.ctx, next.Sub)
			e.subtitles.warm(e.ctx, next.Dub)
		}
		e.revision++
	}()
}

// resumeHold is how long Play keeps the frozen frame while the pause screen animates away.
const resumeHold = 500 * time.Millisecond

func (e *Engine) pauseLocked() { e.phase = "paused"; e.pausedAt = time.Now(); e.prepareBlurLocked() }
func (e *Engine) prepareBlurLocked() {
	gen := e.generation
	e.pauseGeneration++
	pauseGen := e.pauseGeneration
	e.pauseFrames = map[string][]byte{}
	e.pauseBlur = map[string][]byte{}
	for side, s := range e.sides {
		frame := s.Frame()
		e.pauseFrames[side] = frame
		if frame != nil {
			go func(side string, frame []byte) {
				blur := PauseBlur(frame)
				e.mu.Lock()
				defer e.mu.Unlock()
				if e.generation == gen && e.pauseGeneration == pauseGen && e.pauseFrames[side] != nil {
					e.pauseBlur[side] = blur
				}
			}(side, frame)
		}
	}
}
func (e *Engine) Command(action string, value json.RawMessage) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.revision++
	switch action {
	case "start":
		if len(e.outputs) > 0 {
			return nil
		}
		if e.settings.SubServer == "" || e.settings.DubServer == "" {
			return fail("DESTINATIONS_REQUIRED", 409, "configure both publishing servers first")
		}
		if !e.diagnostics.Supported && !e.Config.AllowUnsupported {
			return fail("UNSUPPORTED_HOST", 409, "run encoding diagnostics or explicitly allow unsupported operation")
		}
		for _, side := range []string{"sub", "dub"} {
			p, err := e.factory(e.settings, side, e.encoder)
			if err != nil {
				for _, p := range e.outputs {
					p.Close()
				}
				e.outputs = map[string]Output{}
				return fail("PUBLISHER_FAILED", 502, "%s", Redact(err.Error()))
			}
			e.outputs[side] = p
		}
		e.err = ""
	case "stop":
		if e.remoteRetry {
			e.remoteRetry = false
			e.cancelOperation()
			e.closeSides()
		}
		for _, p := range e.outputs {
			p.Close()
		}
		e.outputs = map[string]Output{}
		e.music.Mute()
		e.music.Close()
		e.music = NewConfiguredMusic(e.Library, e.Config.StateDir, e.settings)
		if e.phase == "playing" || e.phase == "resuming" || e.phase == "buffering" {
			e.pauseLocked()
		}
		if (e.phase == "loading" || e.phase == "seeking") && e.targetPhase == "playing" {
			e.targetPhase = "paused"
		}
		e.save()
	case "play":
		if len(e.outputs) == 0 {
			return fail("STREAMS_STOPPED", 409, "start streams first")
		}
		if e.current() == nil {
			return fail("NO_MEDIA", 409, "load media first")
		}
		if e.phase == "loading" || e.phase == "seeking" {
			return fail("MEDIA_NOT_READY", 409, "wait for media to become ready")
		}
		if e.phase == "playing" || e.phase == "resuming" {
			return nil
		}
		if e.phase == "ended" {
			e.position = 0
			e.phase = "loading"
			e.startDecodeLocked("playing")
		} else if len(e.sides) != 2 {
			e.phase = "loading"
			e.startDecodeLocked("playing")
		} else {
			if e.remoteRetry {
				e.remoteTarget = "playing"
				e.targetPhase = "playing"
			}
			e.phase = "resuming"
			e.resumedAt = time.Now()
		}
	case "pause":
		if e.remoteRetry {
			e.remoteTarget = "paused"
			e.targetPhase = "paused"
		}
		if e.phase == "playing" || e.phase == "resuming" || e.phase == "buffering" {
			e.pauseLocked()
			e.save()
		}
		if (e.phase == "loading" || e.phase == "seeking") && e.targetPhase == "playing" {
			e.targetPhase = "paused"
			e.save()
		}
	case "seek":
		var offset float64
		if json.Unmarshal(value, &offset) != nil || (offset != -10 && offset != 10 && offset != 80) {
			return fail("INVALID_ARGUMENT", 400, "seek offset must be -10, 10 or 80")
		}
		if e.current() == nil {
			return fail("NO_MEDIA", 409, "load media first")
		}
		target := "ready"
		if e.phase == "playing" || e.phase == "buffering" || e.phase == "resuming" {
			target = "playing"
		}
		if e.phase == "paused" {
			target = "paused"
		}
		e.position = clamp(e.position+offset, 0, max(0, e.current().Duration-1/FPS))
		e.phase = "seeking"
		e.music.Mute()
		e.startDecodeLocked(target)
		e.save()
	case "episode":
		var number int
		if json.Unmarshal(value, &number) != nil || number < 1 || number > len(e.items) {
			return fail("INVALID_ARGUMENT", 400, "episode must be an available one-based playlist item")
		}
		target := "ready"
		if e.phase == "playing" || e.phase == "buffering" {
			target = "playing"
		}
		if e.phase == "paused" {
			target = "paused"
		}
		e.index = number - 1
		e.position = 0
		e.phase = "loading"
		e.startDecodeLocked(target)
		e.save()
	case "unload":
		e.cancelOperation()
		e.closeSides()
		if e.cancelSources != nil {
			e.cancelSources()
			e.cancelSources = nil
		}
		e.items = nil
		e.load = LoadRequest{}
		e.index = 0
		e.position = 0
		e.phase = "none"
		e.remoteRetry = false
		e.err = ""
		e.operation = Operation{State: "idle"}
		e.save()
	case "music_next":
		e.music.Next()
	case "music_refresh":
		e.music.Configure(e.settings)
		e.music.replace = false
	case "rescan":
		e.Library.Rescan()
	default:
		return fail("UNKNOWN_COMMAND", 400, "unknown control action")
	}
	return nil
}
func (e *Engine) UpdateSettings(patch map[string]json.RawMessage) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	base, _ := json.Marshal(e.settings)
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(base, &m)
	for k, v := range patch {
		if string(v) == "null" {
			return fail("INVALID_ARGUMENT", 400, "settings fields cannot be null")
		}
		if k != "hud_enabled" && !strings.HasPrefix(k, "music_") && !strings.HasPrefix(k, "plex_") && len(e.outputs) > 0 {
			return fail("STREAMS_RUNNING", 409, "stop streams before updating settings except hud_enabled")
		}
		if _, ok := m[k]; !ok && k != "sub_password" && k != "dub_password" && k != "plex_token" {
			return fail("INVALID_ARGUMENT", 400, "unknown settings field %s", k)
		}
		if k == "sub_password" || k == "dub_password" || k == "plex_token" {
			var s string
			if json.Unmarshal(v, &s) != nil {
				return fail("INVALID_ARGUMENT", 400, "password must be a string")
			}
			if s == "" {
				continue
			}
		}
		m[k] = v
	}
	b, _ := json.Marshal(m)
	s := e.settings
	if err := json.Unmarshal(b, &s); err != nil {
		return fail("INVALID_ARGUMENT", 400, "invalid settings types")
	}
	if err := s.Validate(); err != nil {
		return fail("INVALID_ARGUMENT", 400, "%s", err)
	}
	if s.MusicFolder != "" {
		full, err := e.Library.Resolve(s.MusicFolder)
		if err != nil {
			return err
		}
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			return fail("INVALID_ARGUMENT", 400, "music_folder must be a directory")
		}
	}
	if err := SaveJSON(filepath.Join(e.Config.StateDir, "settings.json"), s); err != nil {
		return err
	}
	if s.MusicFolder != e.settings.MusicFolder || s.MusicSource != e.settings.MusicSource || s.MusicShuffle != e.settings.MusicShuffle || s.PlexServer != e.settings.PlexServer || s.PlexToken != e.settings.PlexToken || s.PlexPlaylistID != e.settings.PlexPlaylistID {
		preserve := s.MusicFolder == e.settings.MusicFolder && s.MusicSource == e.settings.MusicSource && s.PlexServer == e.settings.PlexServer && s.PlexToken == e.settings.PlexToken && s.PlexPlaylistID == e.settings.PlexPlaylistID
		e.music.Configure(s)
		if preserve {
			e.music.replace = false
		}
	}
	e.settings = s
	e.revision++
	return nil
}
func (e *Engine) Snapshot() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	outputs := map[string]OutputStatus{}
	for s, p := range e.outputs {
		outputs[s] = p.Status()
	}
	mode := "idle"
	if len(outputs) == 0 {
		mode = "stopped"
	} else if e.phase == "playing" || e.phase == "buffering" {
		mode = "playing"
	} else if e.phase == "paused" || e.phase == "resuming" {
		mode = "paused"
	}
	items := make([]ItemSpec, 0, len(e.items))
	episodes := []map[string]any{}
	duration := 0.0
	title := ""
	var episode any = nil
	for i, item := range e.items {
		items = append(items, item.Spec)
		episodes = append(episodes, map[string]any{"episode": i + 1, "duration": item.Duration})
	}
	if item := e.current(); item != nil {
		duration = item.Duration
		title = item.Spec.Title
		episode = e.index + 1
	}
	age := 0.0
	if e.phase == "paused" {
		age = time.Since(e.pausedAt).Seconds()
	}
	err := e.err
	if err == "" && e.music.Error != "" {
		err = e.music.Error
	}
	e.renderer.mu.Lock()
	times := append([]float64(nil), e.renderer.times...)
	e.renderer.mu.Unlock()
	sort.Float64s(times)
	stats := RenderStats{Samples: len(times)}
	if len(times) > 0 {
		stats.P95MS = times[min(len(times)-1, len(times)*95/100)]
	}
	music := e.music.snapshot()
	music.Active = e.musicInfo.Active
	music.Gain = e.music.gain
	var metadata *VideoMetadata
	if current := e.current(); current != nil {
		metadata = current.Spec.Metadata
	}
	return Status{e.revision, mode, e.phase == "buffering", episode, e.position, duration, episodes, e.settings.Public(), music.Path, err, age, outputs, MediaState{e.phase, e.load.Mode, title, e.index, len(items), e.position, duration, items, e.operation, metadata}, music, e.diagnostics, stats}
}
func (e *Engine) tick(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clock += 1 / FPS
	e.revision++
	for side, p := range e.outputs {
		if p.Failed(now) && !now.Before(e.retry[side]) {
			old := p.Status()
			p.Close()
			e.retry[side] = now.Add(5 * time.Second)
			if old.Restarts > 2 && e.diagnostics.CPUFallback && containsEncoderError(old.Error) {
				e.encoder = Encoder{Kind: "cpu"}
			}
			next, err := e.factory(e.settings, side, e.encoder)
			if err != nil {
				e.err = Redact(err.Error())
				continue
			}
			if pp, ok := next.(*Publisher); ok {
				pp.Restarts = old.Restarts + 1
			}
			e.outputs[side] = next
		}
	}
	if e.remoteRetry && len(e.sides) == 0 && e.operation.State != "running" && now.After(e.retry["media"]) {
		e.startDecodeLocked(e.remoteTarget)
	}
	ready := len(e.sides) == 2
	for side, s := range e.sides {
		ready = ready && s.Ready()
		mediaError := s.Error()
		if item := e.current(); item != nil && (e.phase == "playing" || e.phase == "buffering") {
			source := item.Sub
			if side == "dub" {
				source = item.Dub
			}
			if source.Spec.Transport == "plex" && s.EOF() && e.position < source.Duration-2 {
				mediaError = "Plex source ended before its expected duration"
			}
		}
		if mediaError != "" {
			e.err = mediaError
			item := e.current()
			if item != nil && (item.Sub.Spec.Transport == "plex" || item.Dub.Spec.Transport == "plex") {
				if !e.remoteRetry {
					e.remoteTarget = e.phase
					if e.remoteTarget == "buffering" || e.remoteTarget == "resuming" {
						e.remoteTarget = "playing"
					}
					if e.remoteTarget == "loading" || e.remoteTarget == "seeking" {
						e.remoteTarget = e.targetPhase
					}
				}
				e.remoteRetry = true
				e.retry["media"] = now.Add(2 * time.Second)
				e.phase = "buffering"
				e.operation.State = "failed"
				e.operation.Error = e.err
				e.closeSides()
				ready = false
				break
			}
			e.pauseLocked()
			e.closeSides()
			ready = false
			break
		}
	}
	if (e.phase == "loading" || e.phase == "seeking" || e.remoteRetry) && ready {
		for _, s := range e.sides {
			s.Frame()
		}
		e.phase = e.targetPhase
		e.operation.State = "complete"
		e.remoteRetry = false
		if e.phase == "paused" {
			e.pauseLocked()
		}
		e.save()
	}
	outputReady := len(e.outputs) == 2
	for _, p := range e.outputs {
		outputReady = outputReady && p.Status().Ready && p.CanAccept()
	}
	musicDue := e.phase == "none" || e.phase == "ready" || e.phase == "paused" || e.phase == "ended" || (e.phase == "loading" && e.targetPhase == "ready")
	if len(e.outputs) == 0 {
		musicDue = false
	}
	previousMusicGain := e.music.gain
	musicAudio, info := e.music.Tick(musicDue, outputReady && len(e.outputs) > 0, e.settings.MusicVolume)
	e.musicInfo = info
	// The video waits for the music fade and for the pause screen's half-second exit transition.
	if e.phase == "resuming" && previousMusicGain <= 0 && e.music.gain <= 0 && ready && outputReady && now.Sub(e.resumedAt) >= resumeHold {
		e.phase = "playing"
	}
	play := (e.phase == "playing" || e.phase == "buffering") && ready && outputReady
	if (e.phase == "playing" || e.phase == "buffering") && !play {
		e.phase = "buffering"
	} else if play {
		e.phase = "playing"
	}
	if play {
		allEOF := true
		for _, s := range e.sides {
			allEOF = allEOF && s.EOF()
		}
		if allEOF {
			if e.index+1 < len(e.items) {
				e.index++
				e.position = 0
				e.phase = "loading"
				e.startDecodeLocked("playing")
			} else {
				e.position = e.current().Duration
				e.phase = "ended"
				e.closeSides()
			}
			play = false
			e.save()
		}
	}
	item := e.current()
	title := ""
	duration := 0.0
	if item != nil {
		title = item.Spec.Title
		duration = item.Duration
	}
	for _, side := range []string{"sub", "dub"} {
		p := e.outputs[side]
		if p == nil {
			continue
		}
		scene := Scene{Phase: e.phase, Title: title, Side: side, Index: e.index, Total: len(e.items), Position: e.position, Duration: duration, Clock: e.clock, Music: info, HUD: e.settings.HUD}
		audio := musicAudio
		if play {
			s := e.sides[side]
			if s.EOF() {
				scene.Phase = "waiting"
				scene.Music = MusicInfo{}
				audio = nil
			} else {
				scene.Frame, audio = s.Take()
			}
		} else if e.phase == "paused" || e.phase == "resuming" {
			scene.Phase = "paused"
			scene.Frame = e.pauseFrames[side]
			scene.Blur = e.pauseBlur[side]
			scene.PauseAge = now.Sub(e.pausedAt).Seconds()
			if e.phase == "resuming" {
				scene.Resuming, scene.ResumeAge = true, now.Sub(e.resumedAt).Seconds()
			}
		}
		if scene.Phase == "seeking" || scene.Phase == "buffering" || (scene.Phase == "loading" && e.targetPhase == "playing") {
			scene.Music = MusicInfo{}
			audio = nil
		}
		frame := e.renderer.Render(scene)
		p.Enqueue(frame, audio)
	}
	if play {
		e.position = min(e.position+1/FPS, duration)
	}
	if now.Sub(e.lastSave) >= 5*time.Second {
		e.save()
	}
}
func containsEncoderError(s string) bool {
	for _, text := range []string{"Cannot load libcuda", "No NVENC capable", "Error initializing output stream", "Error while opening encoder", "Device creation failed", "Failed to initialise VAAPI"} {
		if contains(s, text) {
			return true
		}
	}
	return false
}
func contains(a, b string) bool {
	return len(b) > 0 && len(a) >= len(b) && func() bool {
		for i := 0; i <= len(a)-len(b); i++ {
			if a[i:i+len(b)] == b {
				return true
			}
		}
		return false
	}()
}
func (e *Engine) run() {
	ticker := time.NewTicker(time.Second * 1001 / 24000)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			e.tick(now)
		case <-e.ctx.Done():
			return
		}
	}
}
func (e *Engine) SetDiagnostics(r Report) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.outputs) > 0 {
		return fail("STREAMS_RUNNING", 409, "stop streams before changing encoder qualification")
	}
	if err := SaveJSON(filepath.Join(e.Config.StateDir, "diagnostics.json"), r); err != nil {
		return err
	}
	e.diagnostics = r
	e.encoder = r.Selected
	if e.Config.Encoder != "" && e.Config.Encoder != "auto" {
		e.encoder = Encoder{e.Config.Encoder, e.Config.Device}
		qualified := false
		for _, test := range r.Tests {
			if test.Passed && test.Encoder.Kind == e.Config.Encoder && (e.Config.Device == "" || test.Encoder.Device == e.Config.Device) {
				e.encoder = test.Encoder
				qualified = true
			}
		}
		if !qualified {
			e.diagnostics.Supported = false
			e.diagnostics.Warning = "Selected encoder/device is not qualified"
		}
	}
	e.revision++
	return nil
}
