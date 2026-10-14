package dualsync

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStartupRequalifiesChangedHardware(t *testing.T) {
	base, _, _ := testEngine(t)
	c := base.Config
	c.StateDir = t.TempDir()
	c.AllowUnsupported = true
	saved := Report{Supported: true, Fingerprint: "old-kernel", Selected: Encoder{Kind: "nvenc"}}
	if err := SaveJSON(filepath.Join(c.StateDir, "diagnostics.json"), saved); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	e, err := newEngine(c, func() Report { return Report{Fingerprint: "new-kernel"} }, func(ctx context.Context, _ string) Report {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return Report{Supported: true, Fingerprint: "new-kernel", Selected: Encoder{Kind: "nvenc"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if d := e.Snapshot().Diagnostics; !d.Running || d.Supported {
		t.Fatal("stale qualification must run again before publishing", d)
	}
	if err := e.Command("start", nil); err == nil {
		t.Fatal("publishing accepted during encoding benchmark")
	}
	if err := e.startDiagnostics(func(context.Context, string) Report { panic("duplicate benchmark") }); err == nil {
		t.Fatal("duplicate benchmark accepted")
	}
	release <- struct{}{}
	deadline := time.Now().Add(time.Second)
	for e.Snapshot().Diagnostics.Running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	d := e.Snapshot().Diagnostics
	if d.Running || !d.Supported || d.Fingerprint != "new-kernel" {
		t.Fatal("background qualification did not complete", d)
	}
	var persisted Report
	if err := ReadJSON(filepath.Join(c.StateDir, "diagnostics.json"), &persisted); err != nil || persisted.Fingerprint != "new-kernel" {
		t.Fatal("qualification not persisted", err)
	}
}

func TestStartupReusesUnchangedQualification(t *testing.T) {
	base, _, _ := testEngine(t)
	c := base.Config
	c.StateDir = t.TempDir()
	r := Report{Supported: true, Fingerprint: "unchanged", Selected: Encoder{Kind: "cpu"}}
	if err := SaveJSON(filepath.Join(c.StateDir, "diagnostics.json"), r); err != nil {
		t.Fatal(err)
	}
	e, err := newEngine(c, func() Report { return r }, func(context.Context, string) Report { panic("unchanged hardware retested") })
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if d := e.Snapshot().Diagnostics; d.Running || !d.Supported {
		t.Fatal("valid qualification was discarded", d)
	}
}

func TestMusicAutomaticRefreshKeepsPreparedQueue(t *testing.T) {
	m := &Music{reader: sampleReader(1000), info: MusicInfo{Path: "plex:current"}, settings: Settings{MusicSource: "plex"}, results: make(chan preparedMusic)}
	defer m.Close()
	for _, id := range []string{"first", "second", "third"} {
		m.queue = append(m.queue, preparedMusic{reader: sampleReader(2000), info: MusicInfo{Path: id}})
	}
	for i := 0; i < 5; i++ {
		_, info := m.Tick(false, true, 25)
		if info.Path != "plex:current" || len(info.Upcoming) != 3 || info.Upcoming[0].Path != "first" || info.Upcoming[2].Path != "third" {
			t.Fatal("automatic refresh discarded prepared songs", info)
		}
	}
	close(m.results)
}
