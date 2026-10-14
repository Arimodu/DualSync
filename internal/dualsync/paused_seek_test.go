package dualsync

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

type seekAudioOutput struct {
	fakeOutput
	audio []byte
}

func (o *seekAudioOutput) Enqueue(_ []byte, audio []byte) {
	o.audio = append([]byte(nil), audio...)
}

func TestSeekKeepsPausedMusicAndSilencesPlayingMusic(t *testing.T) {
	for _, phase := range []string{"paused", "playing"} {
		t.Run(phase, func(t *testing.T) {
			e, _, _ := testEngine(t)
			e.cancel()
			time.Sleep(50 * time.Millisecond)
			a, b := &seekAudioOutput{}, &seekAudioOutput{}
			e.mu.Lock()
			e.items = []Item{{Duration: 100}}
			e.outputs = map[string]Output{"sub": a, "dub": b}
			e.phase = phase
			e.music.reader = sampleReader(1000)
			e.music.gain, e.music.target = .25, .25
			e.music.info = MusicInfo{Duration: 100}
			e.mu.Unlock()
			for i := 0; i < 2; i++ {
				if err := e.Command("seek", json.RawMessage("10")); err != nil {
					t.Fatal(err)
				}
				if e.targetPhase != phase {
					t.Fatal("repeated seek changed target", e.targetPhase)
				}
				e.tick(time.Now())
				if phase == "paused" {
					if e.music.gain != .25 || e.music.info.Position <= 0 || len(a.audio) != AudioBytes || bytes.Equal(a.audio, silence) || !bytes.Equal(a.audio, b.audio) {
						t.Fatal("paused seek interrupted shared music")
					}
				} else if e.music.gain != 0 || e.music.info.Position != 0 || len(a.audio) != 0 || len(b.audio) != 0 {
					t.Fatal("playing seek leaked idle music")
				}
			}
		})
	}
}
