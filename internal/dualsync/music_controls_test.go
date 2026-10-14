package dualsync

import (
	"testing"
	"time"
)

func TestMusicControlsAndSilentMediaPause(t *testing.T) {
	e, server, _ := testEngine(t)
	e.cancel()
	time.Sleep(50 * time.Millisecond)
	a, b := &seekAudioOutput{}, &seekAudioOutput{}
	e.mu.Lock()
	e.items = []Item{{Duration: 100, Spec: ItemSpec{Sub: SourceSpec{Path: "sub.mkv"}, Dub: SourceSpec{Path: "dub.mkv"}}}}
	e.outputs = map[string]Output{"sub": a, "dub": b}
	e.phase = "playing"
	e.music.reader = sampleReader(1000)
	reader := e.music.reader
	e.music.gain, e.music.target = .25, .25
	e.music.info = MusicInfo{Position: 17, Duration: 100, Title: "held song"}
	e.mu.Unlock()

	call(t, server, "POST", "/api/control", `{"action":"pause_silent"}`, 200)
	if s := e.Snapshot(); s.Media.Phase != "paused" || !s.Music.Paused || s.Music.Active || s.Music.Gain != 0 {
		t.Fatal("silent pause not visible immediately", s.Media, s.Music)
	}
	for i := 0; i < 3; i++ {
		e.tick(time.Now())
	}
	call(t, server, "POST", "/api/control", `{"action":"seek","value":10}`, 200)
	e.tick(time.Now())
	if e.music.reader != reader || e.music.info.Position != 17 || len(a.audio) != 0 || len(b.audio) != 0 {
		t.Fatal("silent pause or seek consumed music")
	}

	call(t, server, "POST", "/api/control", `{"action":"music_play"}`, 200)
	e.tick(time.Now())
	if e.Snapshot().Music.Paused || e.music.reader != reader || e.music.info.Position <= 17 || len(a.audio) != AudioBytes {
		t.Fatal("music failed to resume held song")
	}
	position := e.music.info.Position
	call(t, server, "POST", "/api/control", `{"action":"music_pause"}`, 200)
	e.tick(time.Now())
	if !e.Snapshot().Music.Paused || e.music.info.Position != position || len(a.audio) != 0 {
		t.Fatal("music pause failed to hold position")
	}

	call(t, server, "POST", "/api/control", `{"action":"pause"}`, 200)
	e.tick(time.Now())
	if e.Snapshot().Music.Paused || e.music.info.Position <= position || len(a.audio) != AudioBytes {
		t.Fatal("normal pause did not restore music")
	}
	if e.outputs["sub"] != a || e.outputs["dub"] != b {
		t.Fatal("music controls replaced publishers")
	}
	call(t, server, "POST", "/api/control", `{"action":"music_pause"}`, 200)
	e.mu.Lock()
	e.music.queue = []preparedMusic{{reader: sampleReader(2000), info: MusicInfo{Path: "next", Duration: 100}}}
	e.mu.Unlock()
	call(t, server, "POST", "/api/control", `{"action":"music_next"}`, 200)
	e.tick(time.Now())
	if s := e.Snapshot(); !s.Music.Paused || s.Music.Path != "next" || s.Music.Position != 0 || len(a.audio) != 0 {
		t.Fatal("next song did not remain held", s.Music)
	}
	e.mu.Lock()
	e.phase = "playing"
	e.sides = map[string]MediaSide{"sub": &fakeSide{frame: blackFrame, end: 100}, "dub": &fakeSide{frame: blackFrame, end: 100}}
	e.mu.Unlock()
	call(t, server, "POST", "/api/control", `{"action":"music_play"}`, 200)
	e.tick(time.Now())
	if s := e.Snapshot(); s.Music.Paused || s.Music.Position != 0 || s.Music.Gain != 0 {
		t.Fatal("music play overlapped episode audio", s.Music)
	}
}
