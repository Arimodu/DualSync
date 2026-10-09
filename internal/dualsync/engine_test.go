package dualsync

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func TestMusicFiveSecondFadeAndFreeze(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &RawReader{queue: make(chan []byte, 150), done: make(chan struct{}), cancel: cancel}
	m := &Music{reader: reader, ctx: ctx, cancel: cancel, info: MusicInfo{Duration: 100}}
	for i := 0; i < 130; i++ {
		reader.queue <- make([]byte, AudioBytes)
	}
	for i := 0; i < 24; i++ {
		m.Tick(true, true, 25)
	}
	if math.Abs(m.gain-.05) > .002 {
		t.Fatalf("1s gain %f", m.gain)
	}
	for i := 24; i < 120; i++ {
		m.Tick(true, true, 25)
	}
	if math.Abs(m.gain-.25) > 1e-6 {
		t.Fatalf("5s gain %f", m.gain)
	}
	for i := 0; i < 6; i++ {
		m.Tick(false, true, 25)
	}
	if m.gain != 0 {
		t.Fatalf("250ms fade %f", m.gain)
	}
	position := m.info.Position
	for i := 0; i < 30; i++ {
		m.Tick(false, true, 25)
	}
	if position != m.info.Position {
		t.Fatal("music advanced during playback")
	}
	m.Tick(true, true, 25)
	if m.info.Position <= position {
		t.Fatal("music failed to resume")
	}
}
func TestCPUAccounting(t *testing.T) {
	var topology strings.Builder
	for i := 0; i < 16; i++ {
		fmt.Fprintf(&topology, "%d,0,0,Y\n", i)
	}
	vm := cpuBudget(topology.String(), "0-7", "kvm", 0)
	if vm.Budget != 8 || vm.PhysicalCores != 0 || !vm.Eligible {
		t.Fatalf("VM synthetic topology %+v", vm)
	}
	metal := cpuBudget(topology.String(), "0-15", "none", 0)
	if metal.Budget != 1 || metal.Eligible {
		t.Fatalf("SMT counted physical cores %+v", metal)
	}
	limited := cpuBudget(topology.String(), "0-15", "kvm", 4.9)
	if limited.Budget != 4 || limited.Eligible {
		t.Fatalf("quota ignored %+v", limited)
	}
	var physical strings.Builder
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&physical, "%d,%d,0,Y\n", i, i)
	}
	metal = cpuBudget(physical.String(), "0-5", "none", 0)
	if !metal.Eligible || metal.Budget != 6 {
		t.Fatal(metal)
	}
}
func TestUnequalDurationAndAutoNext(t *testing.T) {
	e, _, _ := testEngine(t)
	e.cancel()
	time.Sleep(50 * time.Millisecond)
	e.mu.Lock()
	e.outputs = map[string]Output{"sub": &fakeOutput{}, "dub": &fakeOutput{}}
	a := &fakeSide{frame: make([]byte, FrameBytes), end: 2}
	b := &fakeSide{frame: make([]byte, FrameBytes), end: 5}
	e.sides = map[string]MediaSide{"sub": a, "dub": b}
	e.items = []Item{{Duration: 5}, {Duration: 5}}
	e.load.Mode = "paired"
	e.phase = "playing"
	e.mu.Unlock()
	e.tick(time.Now())
	e.tick(time.Now())
	if a.count != 2 || b.count != 2 || e.index != 0 {
		t.Fatal("early transition")
	}
	for i := 0; i < 3; i++ {
		e.tick(time.Now())
	}
	if e.index != 0 || b.count != 5 {
		t.Fatal("did not wait for longest side")
	}
	e.tick(time.Now())
	if e.index != 1 {
		t.Fatal("no auto-next")
	}
}
func TestRendererPassThroughHUDAndPause(t *testing.T) {
	r := NewRenderer("")
	frame := make([]byte, FrameBytes)
	for i := range frame {
		frame[i] = byte(i%200 + 16)
	}
	plain := r.Render(Scene{Phase: "playing", Frame: frame})
	if &plain[0] != &frame[0] {
		t.Fatal("unneeded playing copy")
	}
	hud := r.Render(Scene{Phase: "playing", Frame: frame, HUD: true, Position: 50, Duration: 100})
	if &hud[0] == &frame[0] || hud[0] != frame[0] {
		t.Fatal("HUD mutated source")
	}
	blur := PauseBlur(frame)
	if len(blur) != FrameBytes || blur[Width*Height/2] == frame[Width*Height/2] {
		t.Fatal("blur not applied")
	}
	paused := r.Render(Scene{Phase: "paused", Frame: frame, Blur: blur, PauseAge: 1, Title: "Fixture"})
	if len(paused) != FrameBytes {
		t.Fatal("pause render failed")
	}
}

func TestPublisherRecoveryKeepsHealthyOutputAndFreezesSource(t *testing.T) {
	e, _, _ := testEngine(t)
	e.cancel()
	time.Sleep(50 * time.Millisecond)
	healthy := &fakeOutput{}
	failed := &fakeOutput{}
	failed.failed.Store(true)
	replacement := &fakeOutput{}
	replacement.notReady.Store(true)
	e.mu.Lock()
	e.factory = func(Settings, string, Encoder) (Output, error) { return replacement, nil }
	e.outputs = map[string]Output{"sub": healthy, "dub": failed}
	e.items = []Item{{Duration: 100}}
	e.phase = "playing"
	e.position = 10
	e.sides = map[string]MediaSide{"sub": &fakeSide{frame: blackFrame, end: 100}, "dub": &fakeSide{frame: blackFrame, end: 100}}
	e.mu.Unlock()
	e.tick(time.Now())
	if e.position != 10 || e.phase != "buffering" {
		t.Fatal("source advanced during outage")
	}
	if e.outputs["sub"] != healthy || healthy.closed.Load() {
		t.Fatal("healthy output closed")
	}
	if !failed.closed.Load() || e.outputs["dub"] != replacement {
		t.Fatal("failed output not replaced")
	}
	replacement.notReady.Store(false)
	e.tick(time.Now())
	if e.position <= 10 || e.phase != "playing" {
		t.Fatal("source did not resume after recovery")
	}
}

func TestPauseWhileNextEpisodeLoads(t *testing.T) {
	e, _, _ := testEngine(t)
	e.cancel()
	time.Sleep(50 * time.Millisecond)
	e.mu.Lock()
	e.phase = "loading"
	e.targetPhase = "playing"
	e.items = []Item{{Duration: 100}}
	e.sides = map[string]MediaSide{"sub": &fakeSide{frame: blackFrame, end: 100}, "dub": &fakeSide{frame: blackFrame, end: 100}}
	e.mu.Unlock()
	if err := e.Command("pause", nil); err != nil {
		t.Fatal(err)
	}
	if e.targetPhase != "paused" {
		t.Fatal("pause during loading ignored")
	}
	e.tick(time.Now())
	if e.phase != "paused" {
		t.Fatal("loaded episode resumed unexpectedly")
	}
}

func TestUnsupportedHostAndEncoderOverride(t *testing.T) {
	e, _, _ := testEngine(t)
	e.cancel()
	time.Sleep(50 * time.Millisecond)
	e.Config.AllowUnsupported = false
	if err := e.Command("start", nil); err == nil {
		t.Fatal("unqualified host published")
	}
	e.Config.Encoder = "cpu"
	report := Report{Supported: true, Selected: Encoder{Kind: "nvenc"}, Tests: []EncoderTest{{Encoder: Encoder{Kind: "nvenc"}, Passed: true}, {Encoder: Encoder{Kind: "cpu"}, Passed: false}}}
	if err := e.SetDiagnostics(report); err != nil {
		t.Fatal(err)
	}
	if e.diagnostics.Supported {
		t.Fatal("failed CPU override inherited GPU qualification")
	}
	if e.encoder.Kind != "cpu" {
		t.Fatal("explicit encoder override was lost")
	}
	e.Config.AllowUnsupported = true
	if err := e.Command("start", nil); err != nil {
		t.Fatal("explicit unsupported override ignored", err)
	}
}

func TestResumeHoldsForExitTransition(t *testing.T) {
	e, _, _ := testEngine(t)
	e.cancel()
	time.Sleep(50 * time.Millisecond)
	a := &fakeSide{frame: blackFrame, end: 100}
	b := &fakeSide{frame: blackFrame, end: 100}
	start := time.Now()
	e.mu.Lock()
	e.items = []Item{{Duration: 100}}
	e.sides = map[string]MediaSide{"sub": a, "dub": b}
	e.outputs = map[string]Output{"sub": &fakeOutput{}, "dub": &fakeOutput{}}
	e.phase = "resuming"
	e.resumedAt = start
	e.mu.Unlock()
	e.tick(start.Add(resumeHold - 50*time.Millisecond))
	if e.phase != "resuming" || a.count != 0 {
		t.Fatal("video resumed before the exit transition finished")
	}
	e.tick(start.Add(resumeHold))
	if e.phase != "playing" || a.count != 1 || b.count != 1 {
		t.Fatal("video did not resume after the exit transition")
	}
}

func TestResumeSendsCompleteMusicFadeBeforeMedia(t *testing.T) {
	e, _, _ := testEngine(t)
	e.cancel()
	time.Sleep(50 * time.Millisecond)
	a := &fakeSide{frame: blackFrame, end: 100}
	b := &fakeSide{frame: blackFrame, end: 100}
	e.mu.Lock()
	e.items = []Item{{Duration: 100}}
	e.sides = map[string]MediaSide{"sub": a, "dub": b}
	e.outputs = map[string]Output{"sub": &fakeOutput{}, "dub": &fakeOutput{}}
	e.phase = "resuming"
	e.music.gain = .25
	e.music.target = .25
	e.music.reader = &RawReader{queue: make(chan []byte, 8), done: make(chan struct{}), cancel: func() {}}
	for i := 0; i < 8; i++ {
		e.music.reader.queue <- make([]byte, AudioBytes)
	}
	e.mu.Unlock()
	for i := 0; i < 6; i++ {
		e.tick(time.Now())
	}
	if a.count != 0 || b.count != 0 || e.position != 0 {
		t.Fatal("episode started before final fade samples were sent")
	}
	e.tick(time.Now())
	if a.count != 1 || b.count != 1 {
		t.Fatal("episode failed to start after fade")
	}
}
