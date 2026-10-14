package dualsync

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSeekToJumpsToAbsolutePosition(t *testing.T) {
	e, _, _ := testEngine(t)
	e.cancel()
	time.Sleep(50 * time.Millisecond)
	e.mu.Lock()
	e.items = []Item{{Duration: 100}}
	e.outputs = map[string]Output{"sub": &fakeOutput{}, "dub": &fakeOutput{}}
	e.phase = "paused"
	e.position = 12
	e.mu.Unlock()
	if err := e.Command("seek_to", json.RawMessage("37.5")); err != nil {
		t.Fatal(err)
	}
	if e.position != 37.5 || e.phase != "seeking" || e.targetPhase != "paused" {
		t.Fatal("seek_to did not jump to the position", e.position, e.phase, e.targetPhase)
	}
	if err := e.Command("seek_to", json.RawMessage("5000")); err != nil {
		t.Fatal(err)
	}
	if e.position != 100-1/FPS {
		t.Fatal("seek_to past the end was not clamped", e.position)
	}
	for _, bad := range []string{"-1", `"x"`, "null"} {
		if err := e.Command("seek_to", json.RawMessage(bad)); err == nil {
			t.Fatal("seek_to accepted", bad)
		}
	}
	// The relative seek keeps its fixed offsets.
	if err := e.Command("seek", json.RawMessage("37")); err == nil {
		t.Fatal("seek accepted an absolute value")
	}
}
