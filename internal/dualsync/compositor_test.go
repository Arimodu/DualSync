package dualsync

import (
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/image/draw"
)

// fixtureFrame is a colourful limited-range YUV frame, or the image at DUALSYNC_PREVIEW_FRAME.
func fixtureFrame(t *testing.T) []byte {
	if p := os.Getenv("DUALSYNC_PREVIEW_FRAME"); p != "" {
		if im := loadImage(t, p, Width, Height); im != nil {
			return rgbaToYUV(im)
		}
	}
	f := make([]byte, FrameBytes)
	n := Width * Height
	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			f[y*Width+x] = byte(40 + (x+y)%160)
		}
	}
	for i := n; i < FrameBytes; i++ {
		f[i] = byte(100 + i%56)
	}
	return f
}

func fixtureCover(t *testing.T) *image.RGBA {
	if p := os.Getenv("DUALSYNC_PREVIEW_ART"); p != "" {
		if im := loadImage(t, p, 168, 168); im != nil {
			return im
		}
	}
	im := image.NewRGBA(image.Rect(0, 0, 168, 168))
	for y := 0; y < 168; y++ {
		for x := 0; x < 168; x++ {
			im.SetRGBA(x, y, color.RGBA{uint8(80 + x), uint8(40 + y/2), uint8(200 - x/2), 255})
		}
	}
	return im
}

func loadImage(t *testing.T, p string, w, h int) *image.RGBA {
	f, e := os.Open(p)
	if e != nil {
		t.Logf("preview image %s: %v", p, e)
		return nil
	}
	defer f.Close()
	src, _, e := image.Decode(f)
	if e != nil {
		t.Logf("preview image %s: %v", p, e)
		return nil
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	return dst
}

func rgbaToYUV(im *image.RGBA) []byte {
	f := make([]byte, FrameBytes)
	n := Width * Height
	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			c := im.RGBAAt(x, y)
			lum, u, v := rgbYUV(float64(c.R), float64(c.G), float64(c.B))
			f[y*Width+x] = lum
			if x%2 == 0 && y%2 == 0 {
				i := (y/2)*(Width/2) + x/2
				f[n+i], f[n+n/4+i] = u, v
			}
		}
	}
	return f
}

// yuvPNG converts a limited-range BT.709 frame back to RGB for inspection.
func yuvPNG(t *testing.T, frame []byte, name string) {
	dir := os.Getenv("DUALSYNC_PREVIEW_DIR")
	if dir == "" {
		return
	}
	im := image.NewRGBA(image.Rect(0, 0, Width, Height))
	n := Width * Height
	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			ci := (y/2)*(Width/2) + x/2
			Y := (float64(frame[y*Width+x]) - 16) * 255 / 219
			U := (float64(frame[n+ci]) - 128) * 255 / 224
			V := (float64(frame[n+n/4+ci]) - 128) * 255 / 224
			im.SetRGBA(x, y, color.RGBA{uint8(clamp(Y+1.5748*V, 0, 255)), uint8(clamp(Y-.1873*U-.4681*V, 0, 255)), uint8(clamp(Y+1.8556*U, 0, 255)), 255})
		}
	}
	_ = os.MkdirAll(dir, 0755)
	f, e := os.Create(filepath.Join(dir, name+".png"))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e := png.Encode(f, im); e != nil {
		t.Fatal(e)
	}
}

func assetsDir() string {
	if d, e := filepath.Abs("../../assets"); e == nil {
		if _, e := os.Stat(filepath.Join(d, "fonts")); e == nil {
			return d
		}
	}
	return ""
}

func TestBroadcastScenes(t *testing.T) {
	r := NewRenderer(assetsDir())
	frame := fixtureFrame(t)
	blur := PauseBlur(frame)
	music := MusicInfo{Active: true, Title: "ラビットホール", Artist: "DECO*27", AlbumArtist: "DECO*27", Position: 54, Duration: 159, Gain: .25, Path: "Music/rabbit.flac", Cover: fixtureCover(t)}
	noArt := MusicInfo{Active: true, Title: "", Path: "Music/Unknown Artist - Track 07.mp3", Position: 12}
	title := "[SubsPlease] Tensei Shitara Slime Datta Ken S4 - 03 (1080p) [ABCDEF12].mkv"
	long := "[Group] Tensei Shitara Slime Datta Ken - The Demon Lord's Banquet and the Envoy from the Western Holy Church and Everything After It - S04E03 [1080p].mkv"
	jp := "転生したらスライムだった件 第4期 第3話「魔王たちの宴」.mkv"
	cases := []struct {
		name  string
		scene Scene
	}{
		{"none-song-change", Scene{Phase: "none", Side: "sub", Clock: 10, Music: music}},
		{"none", Scene{Phase: "none", Side: "sub", Clock: 13, Music: music}},
		{"none-no-art", Scene{Phase: "none", Side: "dub", Clock: 10, Music: noArt}},
		{"none-silent", Scene{Phase: "none", Side: "sub", Clock: 10}},
		{"loading", Scene{Phase: "loading", Side: "sub", Title: title, Index: 2, Total: 23, Clock: 3}},
		{"ready", Scene{Phase: "ready", Side: "dub", Title: title, Index: 2, Total: 23, Clock: 20, Music: music}},
		{"ready-long", Scene{Phase: "ready", Side: "sub", Title: long, Index: 2, Total: 23, Clock: 20, Music: music}},
		{"ready-plex", Scene{Phase: "ready", Side: "sub", Title: "I've Been Killing Slimes for 300 Years and Maxed Out My Level · S01E01 · I Maxed Out My Level", Index: 0, Total: 12, Clock: 20, Music: music}},
		{"ready-japanese", Scene{Phase: "ready", Side: "sub", Title: jp, Index: 2, Total: 23, Clock: 20, Music: music}},
		{"paused-0.00", Scene{Phase: "paused", Side: "sub", Title: title, Index: 2, Total: 23, Frame: frame, Blur: blur, PauseAge: 0, Music: music}},
		{"paused-0.25", Scene{Phase: "paused", Side: "sub", Title: title, Index: 2, Total: 23, Frame: frame, Blur: blur, PauseAge: .25, Music: music}},
		{"paused", Scene{Phase: "paused", Side: "sub", Title: title, Index: 2, Total: 23, Frame: frame, Blur: blur, PauseAge: 2, Music: music}},
		{"paused-hud", Scene{Phase: "paused", Side: "dub", Title: title, Index: 2, Total: 23, Frame: frame, Blur: blur, PauseAge: 2, Music: music, HUD: true, Position: 316, Duration: 1440}},
		{"ended", Scene{Phase: "ended", Side: "sub", Title: title, Index: 22, Total: 23, Clock: 40, Music: music}},
	}
	for _, c := range cases {
		out := r.Render(c.scene)
		if len(out) != FrameBytes {
			t.Fatalf("%s: frame is %d bytes", c.name, len(out))
		}
		yuvPNG(t, out, c.name)
	}
	// Seeking, buffering and waiting show the last frame that output played.
	r.Render(Scene{Phase: "playing", Side: "sub", Frame: frame})
	for _, p := range []string{"seeking", "buffering", "waiting"} {
		out := r.Render(Scene{Phase: p, Side: "sub", Clock: 1})
		if len(out) != FrameBytes {
			t.Fatalf("%s: bad length", p)
		}
		yuvPNG(t, out, p)
	}
	for _, p := range []float64{0, .5, 1} {
		for _, side := range []string{"sub", "dub"} {
			out := r.Render(Scene{Phase: "playing", Side: side, Frame: frame, HUD: true, Position: p * 100, Duration: 100})
			yuvPNG(t, out, fmt.Sprintf("hud-%s-%03.0f", side, p*100))
		}
	}
}

func TestBroadcastInvariants(t *testing.T) {
	r := NewRenderer(assetsDir())
	frame := fixtureFrame(t)
	orig := append([]byte(nil), frame...)
	hud := r.Render(Scene{Phase: "playing", Side: "dub", Frame: frame, HUD: true, Position: 30, Duration: 60})
	if string(frame) != string(orig) {
		t.Fatal("HUD mutated the decoder frame")
	}
	// The ring sits bottom right at half opacity and leaves the rest of the picture alone.
	if hud[0] != frame[0] || hud[(Height/2)*Width+Width/2] != frame[(Height/2)*Width+Width/2] {
		t.Fatal("HUD changed pixels outside the ring")
	}
	// A point on the ring's disc, clear of the label so the language letters cannot mask the check.
	cy, cx := Height-ringMargin-ringSize/2, Width-ringMargin-ringSize/2-20
	if hud[cy*Width+cx] == frame[cy*Width+cx] {
		t.Fatal("HUD ring not drawn")
	}
	// Each output gets its own JP or EN ring, so idle frames differ between Sub and Dub.
	a := r.Render(Scene{Phase: "none", Side: "sub", Clock: 5})
	b := r.Render(Scene{Phase: "none", Side: "dub", Clock: 5})
	if string(a) == string(b) {
		t.Fatal("sub and dub idle frames are identical")
	}
	// Non-playing screens always show the ring, whatever the HUD setting, and nothing in the top-right
	// corner where the old channel tag sat.
	blurFrame := PauseBlur(frame)
	for _, hud := range []bool{false, true} {
		paused := r.Render(Scene{Phase: "paused", Side: "sub", Frame: frame, Blur: blurFrame, PauseAge: 1, Position: 30, Duration: 60, HUD: hud})
		if paused[cy*Width+cx] == blurFrame[cy*Width+cx] {
			t.Fatalf("paused frame (HUD %v) has no ring", hud)
		}
		if paused[90*Width+Width-120] != blurFrame[90*Width+Width-120] {
			t.Fatalf("paused frame (HUD %v) draws in the top-right corner", hud)
		}
	}
	idle := r.Render(Scene{Phase: "none", Side: "dub", Clock: 5})
	bg := r.background(MusicInfo{}, 5)
	if idle[cy*Width+cx] == bg[cy*Width+cx] {
		t.Fatal("idle frame has no ring")
	}
	// The pause transition starts when the blur arrives, not when pause began: a blur that is late by
	// 0.3 s still fades in fully, and the transition is complete 0.7 s after it arrives.
	rp := NewRenderer(assetsDir())
	rp.Render(Scene{Phase: "paused", Side: "sub", Frame: frame, PauseAge: 0})
	rp.Render(Scene{Phase: "paused", Side: "sub", Frame: frame, PauseAge: .3})
	late := rp.Render(Scene{Phase: "paused", Side: "sub", Frame: frame, Blur: blurFrame, PauseAge: .35})
	// A blurred point below the pause text, clear of every overlay.
	mid2 := 740*Width + 460
	if late[mid2] != frame[mid2] && late[mid2] == blurFrame[mid2] {
		t.Fatal("late blur jumped straight to full strength")
	}
	done := rp.Render(Scene{Phase: "paused", Side: "sub", Frame: frame, Blur: blurFrame, PauseAge: 1.05})
	if done[mid2] != blurFrame[mid2] {
		t.Fatal("pause fade did not finish half a second after the blur arrived")
	}
	// Resuming reverses it: half a second after Play the frame is sharp again, with no pause card.
	rr := NewRenderer(assetsDir())
	rr.Render(Scene{Phase: "paused", Side: "sub", Frame: frame, Blur: blurFrame, PauseAge: 0})
	held := rr.Render(Scene{Phase: "paused", Side: "sub", Frame: frame, Blur: blurFrame, PauseAge: 2, Total: 1, Title: "x"})
	early := rr.Render(Scene{Phase: "paused", Side: "sub", Frame: frame, Blur: blurFrame, PauseAge: 2.1, Total: 1, Title: "x", Resuming: true, ResumeAge: .1})
	gone := rr.Render(Scene{Phase: "paused", Side: "sub", Frame: frame, Blur: blurFrame, PauseAge: 2.5, Total: 1, Title: "x", Resuming: true, ResumeAge: .5})
	if early[mid2] != held[mid2] {
		t.Fatal("blur started clearing before the text had faded")
	}
	centre := (Height/2)*Width + Width/2
	if gone[mid2] != frame[mid2] || gone[centre] != frame[centre] {
		t.Fatal("resume did not end on the sharp frozen frame")
	}
	// Pause fades to the prepared blur over half a second.
	blur := PauseBlur(frame)
	mid := (Height/2)*Width + Width/2
	start := r.Render(Scene{Phase: "paused", Side: "sub", Frame: frame, Blur: blur, PauseAge: 0})
	end := r.Render(Scene{Phase: "paused", Side: "sub", Frame: frame, Blur: blur, PauseAge: 1})
	if start[10*Width+Width/2] != frame[10*Width+Width/2] {
		t.Fatal("pause did not start from the frozen frame")
	}
	_ = end[mid]
	// Edges stay sharp, the centre is blurred.
	if blur[5*Width+5] != frame[5*Width+5] {
		t.Fatal("pause blur touched the frame corner")
	}
	if blur[mid] == frame[mid] {
		t.Fatal("pause blur left the centre untouched")
	}
}

func TestDisplayTitle(t *testing.T) {
	for in, want := range map[string]string{
		"[SubsPlease] Tensei Shitara Slime Datta Ken S4 - 01 (1080p) [182FF0C9].mkv": "Tensei Shitara Slime Datta Ken S4 - 01",
		"Anime/Show/Show_Name_-_05.mp4":                                              "Show Name - 05",
		"Vol. 2":                                                                     "Vol. 2",
		"転生したらスライムだった件 第3話.mkv":                                                      "転生したらスライムだった件 第3話",
		"": "",
	} {
		if got := displayTitle(in); got != want {
			t.Errorf("displayTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// BenchmarkIdleTick renders one idle tick for both outputs, the way the engine does.
func BenchmarkIdleTick(b *testing.B) {
	r := NewRenderer(assetsDir())
	music := MusicInfo{Active: true, Title: "ラビットホール", AlbumArtist: "DECO*27", Position: 54, Duration: 159, Path: "m", Cover: fixtureCover(&testing.T{})}
	clock := 0.0
	for i := 0; i < b.N; i++ {
		clock += 1 / FPS
		r.Render(Scene{Phase: "none", Side: "sub", Clock: clock, Music: music})
		r.Render(Scene{Phase: "none", Side: "dub", Clock: clock, Music: music})
	}
}

func BenchmarkPauseBlur(b *testing.B) {
	frame := fixtureFrame(&testing.T{})
	PauseBlur(frame)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PauseBlur(frame)
	}
}

func BenchmarkPausedTick(b *testing.B) {
	r := NewRenderer(assetsDir())
	frame := fixtureFrame(&testing.T{})
	blur := PauseBlur(frame)
	start := time.Now()
	for i := 0; i < b.N; i++ {
		age := time.Since(start).Seconds() + 1
		r.Render(Scene{Phase: "paused", Side: "sub", Frame: frame, Blur: blur, PauseAge: age, Title: "x", Total: 3})
		r.Render(Scene{Phase: "paused", Side: "dub", Frame: frame, Blur: blur, PauseAge: age, Title: "x", Total: 3})
	}
}
