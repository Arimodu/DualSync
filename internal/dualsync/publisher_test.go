package dualsync

import (
	"encoding/binary"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPublisherProducesAudioPackets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.mkv")
	frames := 96
	p, err := startPublisher(path, Encoder{Kind: "cpu"}, &frames)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	audio := make([]byte, AudioBytes)
	for i := 0; i < len(audio); i += 2 {
		binary.LittleEndian.PutUint16(audio[i:], uint16(i%12000))
	}
	deadline := time.Now().Add(20 * time.Second)
	for i := 0; i < frames; {
		if time.Now().After(deadline) {
			t.Fatal("publisher timed out")
		}
		if p.CanAccept() {
			p.Enqueue(blackFrame, audio)
			i++
		} else {
			time.Sleep(time.Millisecond)
		}
	}
	p.Finish()
	select {
	case <-p.done:
	case <-time.After(20 * time.Second):
		t.Fatal("publisher did not finish")
	}
	samples, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-map", "0:a:0", "-f", "s16le", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) < AudioBytes*80 {
		t.Fatalf("missing encoded audio: %d bytes, logs %s", len(samples), p.log.String())
	}
}
