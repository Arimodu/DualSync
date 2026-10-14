package dualsync

import (
	"fmt"
	"image"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

type MusicInfo struct {
	Source           string          `json:"source"`
	PlaylistID       string          `json:"playlist_id"`
	PlaylistTitle    string          `json:"playlist_title"`
	ReadyCount       int             `json:"ready_count"`
	PreparationState string          `json:"preparation_state"`
	Upcoming         []MusicUpcoming `json:"upcoming"`
	Error            string          `json:"error"`
	Active           bool            `json:"active"`
	Paused           bool            `json:"paused"`
	Title            string          `json:"title"`
	Artist           string          `json:"artist"`
	AlbumArtist      string          `json:"album_artist"`
	Album            string          `json:"album"`
	Position         float64         `json:"position"`
	Duration         float64         `json:"duration"`
	Gain             float64         `json:"gain"`
	CoverURL         string          `json:"cover_url"`
	Path             string          `json:"path"`
	Cover            *image.RGBA     `json:"-"`
}

// Scene is everything one output frame depends on. Side is "sub" or "dub".
type Scene struct {
	Phase, Title, Side        string
	Index, Total              int
	Position, Duration, Clock float64
	Frame                     []byte
	Blur                      []byte
	PauseAge                  float64
	Resuming                  bool    // Play was pressed; the pause screen is animating away
	ResumeAge                 float64 // seconds since Play, while Resuming
	Music                     MusicInfo
	HUD                       bool
}

// Broadcast layout, in output pixels. Mirrors the approved overlay preview.
const (
	cardX, cardY, cardW, cardH = 64, 64, 804, 208
	cardArt                    = 168
	centerW                    = 1400
	iconSize                   = 168
	ringSize, ringMargin       = 64, 40
	bgFPS                      = FPS // the idle gradient advances every frame; both outputs share each frame
	spinSteps                  = 48
)

var (
	white  = rgba{1, 1, 1, 1}
	plate  = rgba{10 / 255., 14 / 255., 28 / 255., .46}
	nezumi = rgba{0xa3 / 255., 0xab / 255., 0xc0 / 255., 1}
)

type Renderer struct {
	medium, bold, black *opentype.Font
	fallback            []*opentype.Font
	faces               map[faceKey]font.Face
	sbuf                sfnt.Buffer

	overlays map[string]*overlay // text, card, tag and icon layers; bounded
	spinner  [spinSteps]*overlay
	rings    map[string]ringCache // one per output, so Sub and Dub do not evict each other
	bar      ringCache            // music progress bar, shared by both outputs

	bgKey            int
	bg               []byte
	art, prevArt     *image.RGBA
	artID            string
	artAt            float64
	last             map[string][]byte // last frame each output played; never mutated
	pauses           map[string]pauseState
	dimSrc, dimFrame map[string][]byte

	mu    sync.Mutex
	times []float64
}

type faceKey struct {
	f    *opentype.Font
	size int
}

func NewRenderer(assets string) *Renderer {
	load := func(name string) *opentype.Font {
		if assets == "" {
			return nil
		}
		b, e := os.ReadFile(filepath.Join(assets, "fonts", name))
		if e != nil {
			return nil
		}
		f, e := opentype.Parse(b)
		if e != nil {
			return nil
		}
		return f
	}
	goFont, _ := opentype.Parse(goregular.TTF)
	r := &Renderer{faces: map[faceKey]font.Face{}, overlays: map[string]*overlay{}, last: map[string][]byte{}, dimSrc: map[string][]byte{}, dimFrame: map[string][]byte{}, bgKey: -1, rings: map[string]ringCache{}, pauses: map[string]pauseState{}}
	r.medium, r.bold, r.black = load("ZenKakuGothicNew-Medium.ttf"), load("ZenKakuGothicNew-Bold.ttf"), load("ZenKakuGothicNew-Black.ttf")
	for _, f := range []*opentype.Font{load("NotoSansJP.ttf"), goFont} {
		if f != nil {
			r.fallback = append(r.fallback, f)
		}
	}
	base := r.fallback[0]
	for _, f := range []**opentype.Font{&r.medium, &r.bold, &r.black} {
		if *f == nil {
			*f = base
		}
	}
	return r
}

// ---------- text ----------

func (r *Renderer) face(f *opentype.Font, size int) font.Face {
	k := faceKey{f, size}
	if fc := r.faces[k]; fc != nil {
		return fc
	}
	fc, _ := opentype.NewFace(f, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingNone})
	r.faces[k] = fc
	return fc
}

// glyphFont returns the first font that has a glyph for ch, so Japanese titles fall back cleanly.
func (r *Renderer) glyphFont(primary *opentype.Font, ch rune) *opentype.Font {
	if i, e := primary.GlyphIndex(&r.sbuf, ch); e == nil && i != 0 {
		return primary
	}
	for _, f := range r.fallback {
		if i, e := f.GlyphIndex(&r.sbuf, ch); e == nil && i != 0 {
			return f
		}
	}
	return primary
}

func (r *Renderer) measure(f *opentype.Font, size int, s string) float64 {
	w := fixed.Int26_6(0)
	for _, ch := range s {
		adv, _ := r.face(r.glyphFont(f, ch), size).GlyphAdvance(ch)
		w += adv
	}
	return float64(w) / 64
}

// fit shortens s with an ellipsis until it is at most maxW wide.
func (r *Renderer) fit(f *opentype.Font, size int, s string, maxW float64) string {
	if r.measure(f, size, s) <= maxW {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 {
		runes = runes[:len(runes)-1]
		t := strings.TrimRight(string(runes), " ") + "…"
		if r.measure(f, size, t) <= maxW {
			return t
		}
	}
	return "…"
}

// wrap breaks s into at most maxLines lines no wider than maxW, at spaces when possible.
func (r *Renderer) wrap(f *opentype.Font, size int, s string, maxW float64, maxLines int) []string {
	var lines []string
	rest := strings.TrimSpace(s)
	for rest != "" && len(lines) < maxLines {
		if r.measure(f, size, rest) <= maxW || len(lines) == maxLines-1 {
			lines = append(lines, r.fit(f, size, rest, maxW))
			break
		}
		runes := []rune(rest)
		cut := 0
		for i := 1; i <= len(runes); i++ {
			if r.measure(f, size, string(runes[:i])) > maxW {
				break
			}
			cut = i
		}
		if sp := strings.LastIndex(string(runes[:cut]), " "); sp > 0 {
			cut = len([]rune(string(runes[:cut])[:sp]))
		}
		cut = max(cut, 1)
		lines = append(lines, strings.TrimSpace(string(runes[:cut])))
		rest = strings.TrimSpace(string(runes[cut:]))
	}
	return lines
}

// drawText paints s with its baseline at (x, y).
func (r *Renderer) drawText(c *canvas, f *opentype.Font, size int, s string, x, y float64, col rgba) {
	mask := image.NewAlpha(image.Rect(0, 0, c.w, c.h))
	d := font.Drawer{Dst: mask, Src: image.Opaque, Dot: fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}}
	for _, ch := range s {
		d.Face = r.face(r.glyphFont(f, ch), size)
		d.DrawString(string(ch))
	}
	c.paintMask(mask, col)
}

// baseline returns the baseline for a line box of height lineH starting at top.
func (r *Renderer) baseline(f *opentype.Font, size int, top, lineH float64) float64 {
	m := r.face(f, size).Metrics()
	asc, desc := float64(m.Ascent)/64, float64(m.Descent)/64
	return top + (lineH-(asc+desc))/2 + asc
}

var tagPattern = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|\{[^}]*\}`)
var spacePattern = regexp.MustCompile(`\s+`)
var extPattern = regexp.MustCompile(`\.[A-Za-z0-9]{2,4}$`)

// displayTitle turns "[Group] Show - S04E01 [1080p].mkv" into "Show - S04E01".
func displayTitle(s string) string {
	base := path.Base(strings.ReplaceAll(s, `\`, "/"))
	if base == "." || base == "/" {
		return ""
	}
	t := extPattern.ReplaceAllString(base, "")
	t = tagPattern.ReplaceAllString(t, " ")
	t = spacePattern.ReplaceAllString(strings.ReplaceAll(t, "_", " "), " ")
	t = strings.Trim(t, " -–—.")
	if t == "" {
		return base
	}
	return t
}

// ---------- RGBA canvas (premultiplied, 0..1) ----------

type rgba struct{ r, g, b, a float32 }

func (c rgba) alpha(a float32) rgba { c.a *= a; return c }

type canvas struct {
	w, h int
	p    []float32
}

func newCanvas(w, h int) *canvas { return &canvas{w, h, make([]float32, w*h*4)} }

func (c *canvas) over(i int, col rgba, cov float32) {
	a := col.a * cov
	if a <= 0 {
		return
	}
	ia := 1 - a
	p := c.p[i*4 : i*4+4]
	p[0] = col.r*a + p[0]*ia
	p[1] = col.g*a + p[1]*ia
	p[2] = col.b*a + p[2]*ia
	p[3] = a + p[3]*ia
}

// shape fills the region where sdf < 0 with one pixel of antialiasing.
func (c *canvas) shape(x0, y0, x1, y1 float64, col rgba, sdf func(x, y float64) float64) {
	for y := max(0, int(y0)-2); y < min(c.h, int(y1)+3); y++ {
		for x := max(0, int(x0)-2); x < min(c.w, int(x1)+3); x++ {
			d := sdf(float64(x)+.5, float64(y)+.5)
			if cov := clamp(.5-d, 0, 1); cov > 0 {
				c.over(y*c.w+x, col, float32(cov))
			}
		}
	}
}

func (c *canvas) paintMask(m *image.Alpha, col rgba) {
	for i, a := range m.Pix {
		if a != 0 {
			c.over(i, col, float32(a)/255)
		}
	}
}

// withShadow returns a canvas with a soft dark copy of c's alpha drawn beneath it.
func (c *canvas) withShadow(dy int, blur int, alpha float32) *canvas {
	a := make([]float32, c.w*c.h)
	for y := 0; y < c.h; y++ {
		sy := y - dy
		if sy < 0 || sy >= c.h {
			continue
		}
		for x := 0; x < c.w; x++ {
			a[y*c.w+x] = c.p[(sy*c.w+x)*4+3]
		}
	}
	boxBlur(a, c.w, c.h, max(1, blur/2), 3)
	out := newCanvas(c.w, c.h)
	for i, v := range a {
		out.p[i*4+3] = v * alpha
	}
	for i := 0; i < c.w*c.h; i++ {
		sa := c.p[i*4+3]
		ia := 1 - sa
		for k := 0; k < 4; k++ {
			out.p[i*4+k] = c.p[i*4+k] + out.p[i*4+k]*ia
		}
	}
	return out
}

// boxBlur applies `passes` horizontal and vertical box blurs of radius rad, in place.
func boxBlur(v []float32, w, h, rad, passes int) {
	tmp := make([]float32, len(v))
	line := func(src, dst []float32, n, stride, start int) {
		var sum float32
		for i := -rad; i <= rad; i++ {
			sum += src[start+min(n-1, max(0, i))*stride]
		}
		norm := 1 / float32(2*rad+1)
		for i := 0; i < n; i++ {
			dst[start+i*stride] = sum * norm
			sum += src[start+min(n-1, i+rad+1)*stride] - src[start+max(0, i-rad)*stride]
		}
	}
	for p := 0; p < passes; p++ {
		for y := 0; y < h; y++ {
			line(v, tmp, w, 1, y*w)
		}
		for x := 0; x < w; x++ {
			line(tmp, v, h, w, x)
		}
	}
}

// ---------- signed distance helpers ----------

func sdCircle(cx, cy, rad float64) func(x, y float64) float64 {
	return func(x, y float64) float64 { return math.Hypot(x-cx, y-cy) - rad }
}
func sdRing(cx, cy, rad, width float64) func(x, y float64) float64 {
	return func(x, y float64) float64 { return math.Abs(math.Hypot(x-cx, y-cy)-rad) - width/2 }
}
func sdSegment(ax, ay, bx, by, width float64) func(x, y float64) float64 {
	return func(x, y float64) float64 {
		px, py, dx, dy := x-ax, y-ay, bx-ax, by-ay
		t := clamp((px*dx+py*dy)/max(1e-9, dx*dx+dy*dy), 0, 1)
		return math.Hypot(px-dx*t, py-dy*t) - width/2
	}
}
func sdRoundRect(x0, y0, x1, y1, rad float64) func(x, y float64) float64 {
	cx, cy, hx, hy := (x0+x1)/2, (y0+y1)/2, (x1-x0)/2-rad, (y1-y0)/2-rad
	return func(x, y float64) float64 {
		qx, qy := math.Abs(x-cx)-hx, math.Abs(y-cy)-hy
		return math.Hypot(max(qx, 0), max(qy, 0)) + min(max(qx, qy), 0) - rad
	}
}

// sdArc is a stroked arc from angle a0 sweeping `sweep` radians clockwise (0 = 12 o'clock), with round caps.
func sdArc(cx, cy, rad, width, a0, sweep float64) func(x, y float64) float64 {
	ex := func(a float64) (float64, float64) { return cx + rad*math.Sin(a), cy - rad*math.Cos(a) }
	sx, sy := ex(a0)
	tx, ty := ex(a0 + sweep)
	return func(x, y float64) float64 {
		a := math.Mod(math.Atan2(x-cx, cy-y)-a0+4*math.Pi, 2*math.Pi)
		if a <= sweep {
			return math.Abs(math.Hypot(x-cx, y-cy)-rad) - width/2
		}
		return min(math.Hypot(x-sx, y-sy), math.Hypot(x-tx, y-ty)) - width/2
	}
}

// sdPolygon is a convex polygon given clockwise, rounded by rad.
func sdPolygon(pts [][2]float64, rad float64) func(x, y float64) float64 {
	return func(x, y float64) float64 {
		d := -math.MaxFloat64
		inside := true
		best := math.MaxFloat64
		for i := range pts {
			a, b := pts[i], pts[(i+1)%len(pts)]
			ex, ey := b[0]-a[0], b[1]-a[1]
			l := math.Hypot(ex, ey)
			side := ((x-a[0])*ey - (y-a[1])*ex) / l
			d = max(d, side)
			if side > 0 {
				inside = false
			}
			best = min(best, sdSegment(a[0], a[1], b[0], b[1], 0)(x, y))
		}
		if inside {
			return d - rad
		}
		return best - rad
	}
}

// ---------- YUV overlays ----------

// overlay is a premultiplied RGBA layer converted once to limited-range BT.709 YUV with alpha.
// Its origin and size are even so chroma samples line up with the frame.
type overlay struct {
	x0, y0, w, h int
	y, a         []uint8
	u, v, ca     []uint8
}

func (c *canvas) overlay() *overlay {
	minX, minY, maxX, maxY := c.w, c.h, -1, -1
	for y := 0; y < c.h; y++ {
		for x := 0; x < c.w; x++ {
			if c.p[(y*c.w+x)*4+3] > 1./512 {
				minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
			}
		}
	}
	if maxX < 0 {
		return &overlay{}
	}
	minX, minY = minX&^1, minY&^1
	w, h := (maxX-minX+2)&^1, (maxY-minY+2)&^1
	o := &overlay{x0: minX, y0: minY, w: w, h: h, y: make([]uint8, w*h), a: make([]uint8, w*h), u: make([]uint8, w*h/4), v: make([]uint8, w*h/4), ca: make([]uint8, w*h/4)}
	px := func(x, y int) (float64, float64, float64, float64) {
		x, y = x+minX, y+minY
		if x >= c.w || y >= c.h {
			return 0, 0, 0, 0
		}
		p := c.p[(y*c.w+x)*4:]
		return float64(p[0]), float64(p[1]), float64(p[2]), float64(p[3])
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, a := px(x, y)
			if a <= 0 {
				continue
			}
			lum, _, _ := rgbYUV(r/a*255, g/a*255, b/a*255)
			o.y[y*w+x], o.a[y*w+x] = lum, uint8(clamp(a*255+.5, 0, 255))
		}
	}
	for y := 0; y < h/2; y++ {
		for x := 0; x < w/2; x++ {
			var sr, sg, sb, sa float64
			for k := 0; k < 4; k++ {
				r, g, b, a := px(2*x+k%2, 2*y+k/2)
				sr, sg, sb, sa = sr+r, sg+g, sb+b, sa+a
			}
			if sa <= 0 {
				continue
			}
			_, u, v := rgbYUV(sr/sa*255, sg/sa*255, sb/sa*255)
			i := y*(w/2) + x
			o.u[i], o.v[i], o.ca[i] = u, v, uint8(clamp(sa/4*255+.5, 0, 255))
		}
	}
	return o
}

// blend draws o onto a YUV420p frame with its canvas origin at (X, Y), scaled by opacity (0..256).
func (o *overlay) blend(dst []byte, X, Y, opacity int) {
	if o == nil || o.w == 0 || opacity <= 0 {
		return
	}
	X, Y = (X+o.x0)&^1, (Y+o.y0)&^1
	n := Width * Height
	for y := 0; y < o.h; y++ {
		yy := Y + y
		if yy < 0 || yy >= Height {
			continue
		}
		row := dst[yy*Width:]
		for x := 0; x < o.w; x++ {
			xx := X + x
			a := int(o.a[y*o.w+x])
			if a == 0 || xx < 0 || xx >= Width {
				continue
			}
			a = a * opacity >> 8
			row[xx] = byte((int(o.y[y*o.w+x])*a + int(row[xx])*(255-a)) / 255)
		}
	}
	cw := o.w / 2
	for y := 0; y < o.h/2; y++ {
		yy := Y/2 + y
		if yy < 0 || yy >= Height/2 {
			continue
		}
		for x := 0; x < cw; x++ {
			xx := X/2 + x
			a := int(o.ca[y*cw+x])
			if a == 0 || xx < 0 || xx >= Width/2 {
				continue
			}
			a = a * opacity >> 8
			i := yy*(Width/2) + xx
			dst[n+i] = byte((int(o.u[y*cw+x])*a + int(dst[n+i])*(255-a)) / 255)
			dst[n+n/4+i] = byte((int(o.v[y*cw+x])*a + int(dst[n+n/4+i])*(255-a)) / 255)
		}
	}
}

func (r *Renderer) cached(key string, build func() *overlay) *overlay {
	if o := r.overlays[key]; o != nil {
		return o
	}
	if len(r.overlays) > 64 {
		r.overlays = map[string]*overlay{}
	}
	o := build()
	r.overlays[key] = o
	return o
}

// ---------- color ----------

func rgbYUV(red, green, blue float64) (byte, byte, byte) {
	return byte(clamp(16+(.2126*red+.7152*green+.0722*blue)*219/255, 16, 235)), byte(clamp(128+(-.114572*red-.385428*green+.5*blue)*224/255, 16, 240)), byte(clamp(128+(.5*red-.454153*green-.045847*blue)*224/255, 16, 240))
}
func clamp(x, lo, hi float64) float64 { return min(hi, max(lo, x)) }

func scalePlane(dst, src []byte, w, h, sw, sh int) {
	xs, weights := make([]int, w), make([]int, w)
	for x := 0; x < w; x++ {
		sx := x * (sw - 1) * 256 / (w - 1)
		xs[x] = sx >> 8
		weights[x] = sx & 255
	}
	for y := 0; y < h; y++ {
		sy := y * (sh - 1) * 256 / (h - 1)
		y0 := sy >> 8
		y1 := min(y0+1, sh-1)
		fy := sy & 255
		aRow, bRow := src[y0*sw:(y0+1)*sw], src[y1*sw:(y1+1)*sw]
		row := dst[y*w : (y+1)*w]
		for x := 0; x < w; x++ {
			x0 := xs[x]
			x1 := min(x0+1, sw-1)
			fx := weights[x]
			a := int(aRow[x0])*(256-fx) + int(aRow[x1])*fx
			b := int(bRow[x0])*(256-fx) + int(bRow[x1])*fx
			row[x] = byte((a*(256-fy) + b*fy) >> 16)
		}
	}
}

// ---------- idle gradient ----------

const gw, gh = 240, 135

var fallbackBlobs = [3][3]float32{{0x1d, 0x3b, 0x6b}, {0x55, 0xc0, 0xc2}, {0xe9, 0xa6, 0x3c}}

// artField paints the album art three times, large, rotating and drifting, then blurs and saturates it.
// Without artwork it paints soft blobs in the console's colors instead.
func artField(art *image.RGBA, t float64) []float32 {
	px := make([]float32, gw*gh*3)
	for i := 0; i < gw*gh; i++ {
		px[i*3], px[i*3+1], px[i*3+2] = 0x10, 0x16, 0x2a
	}
	layers := [3][4]float64{{.62, .40, 2.6, 1. / 90}, {.30, .70, 2.2, -1. / 120}, {.80, .75, 1.9, 1. / 150}}
	for li, l := range layers {
		fi := float64(li)
		ang := t*l[3]*2*math.Pi + fi*2.1
		cx, cy := gw*(l[0]+.08*math.Sin(t/23+fi)), gh*(l[1]+.08*math.Cos(t/29+fi))
		size := gh * l[2]
		alpha := float32(1)
		if li > 0 {
			alpha = .75
		}
		cs, sn := math.Cos(ang), math.Sin(ang)
		for y := 0; y < gh; y++ {
			for x := 0; x < gw; x++ {
				dx, dy := float64(x)+.5-cx, float64(y)+.5-cy
				var col [3]float32
				var a float32
				if art != nil {
					u, v := (dx*cs+dy*sn)/size+.5, (-dx*sn+dy*cs)/size+.5
					if u < 0 || u >= 1 || v < 0 || v >= 1 {
						continue
					}
					b := art.Bounds()
					fx, fy := u*float64(b.Dx()-1), v*float64(b.Dy()-1)
					x0, y0 := int(fx), int(fy)
					x1, y1 := min(x0+1, b.Dx()-1), min(y0+1, b.Dy()-1)
					wx, wy := float32(fx-float64(x0)), float32(fy-float64(y0))
					for k := 0; k < 3; k++ {
						c00 := float32(art.Pix[y0*art.Stride+x0*4+k])
						c10 := float32(art.Pix[y0*art.Stride+x1*4+k])
						c01 := float32(art.Pix[y1*art.Stride+x0*4+k])
						c11 := float32(art.Pix[y1*art.Stride+x1*4+k])
						col[k] = (c00*(1-wx)+c10*wx)*(1-wy) + (c01*(1-wx)+c11*wx)*wy
					}
					a = alpha
				} else {
					rad := size * .45
					d := math.Hypot(dx, dy) / rad
					if d >= 1 {
						continue
					}
					col = fallbackBlobs[li]
					a = alpha * float32(1-d)
				}
				i := (y*gw + x) * 3
				for k := 0; k < 3; k++ {
					px[i+k] = col[k]*a + px[i+k]*(1-a)
				}
			}
		}
	}
	// CSS blur(14px) on the 240-wide canvas, then saturate(1.6) and a 28% navy veil.
	for k := 0; k < 3; k++ {
		ch := make([]float32, gw*gh)
		for i := range ch {
			ch[i] = px[i*3+k]
		}
		boxBlur(ch, gw, gh, 8, 3)
		for i := range ch {
			px[i*3+k] = ch[i]
		}
	}
	const s = 1.6
	for i := 0; i < gw*gh; i++ {
		r, g, b := px[i*3], px[i*3+1], px[i*3+2]
		nr := (.213+.787*s)*r + (.715-.715*s)*g + (.072-.072*s)*b
		ng := (.213-.213*s)*r + (.715+.285*s)*g + (.072-.072*s)*b
		nb := (.213-.213*s)*r + (.715-.715*s)*g + (.072+.928*s)*b
		px[i*3] = nr*.72 + 8*.28
		px[i*3+1] = ng*.72 + 12*.28
		px[i*3+2] = nb*.72 + 24*.28
	}
	return px
}

// background returns the shared idle frame for this clock tick. Callers must copy it.
func (r *Renderer) background(m MusicInfo, clock float64) []byte {
	id := ""
	var cover *image.RGBA
	if m.Active && m.Cover != nil {
		id, cover = m.Path, m.Cover
	}
	if id != r.artID {
		r.prevArt, r.art, r.artID, r.artAt = r.art, cover, id, clock
		r.bgKey = -1
	}
	key := int(clock * bgFPS)
	if key == r.bgKey && r.bg != nil {
		return r.bg
	}
	t := float64(key) / bgFPS
	px := artField(r.art, t)
	if mix := clamp((clock-r.artAt)/2, 0, 1); mix < 1 {
		old := artField(r.prevArt, t)
		for i := range px {
			px[i] = old[i]*float32(1-mix) + px[i]*float32(mix)
		}
	}
	n := gw * gh
	ly, lu, lv := make([]float32, n), make([]float32, n), make([]float32, n)
	for i := 0; i < n; i++ {
		ly[i], lu[i], lv[i] = yuvFloat(px[i*3], px[i*3+1], px[i*3+2])
	}
	out := make([]byte, FrameBytes)
	upscaleDither(out[:Width*Height], ly, Width, Height, 0, 16, 235)
	upscaleDither(out[Width*Height:Width*Height*5/4], lu, Width/2, Height/2, 1, 16, 240)
	upscaleDither(out[Width*Height*5/4:], lv, Width/2, Height/2, 2, 16, 240)
	r.bgKey, r.bg = key, out
	return out
}

// yuvFloat is rgbYUV without rounding, so the gradient keeps its precision until the final dither.
func yuvFloat(red, green, blue float32) (float32, float32, float32) {
	return 16 + (.2126*red+.7152*green+.0722*blue)*219/255,
		128 + (-.114572*red-.385428*green+.5*blue)*224/255,
		128 + (.5*red-.454153*green-.045847*blue)*224/255
}

// cubicTable maps each output coordinate to four source taps with Catmull-Rom weights.
type cubicTable struct {
	idx [][4]int
	w   [][4]float32
}

var (
	cubicMu     sync.Mutex
	cubicTables = map[[2]int]*cubicTable{}
)

func cubicWeights(dst, src int) *cubicTable {
	cubicMu.Lock()
	defer cubicMu.Unlock()
	if t := cubicTables[[2]int{dst, src}]; t != nil {
		return t
	}
	t := &cubicTable{make([][4]int, dst), make([][4]float32, dst)}
	for x := 0; x < dst; x++ {
		sx := (float64(x)+.5)*float64(src)/float64(dst) - .5
		i := int(math.Floor(sx))
		f := sx - float64(i)
		f2, f3 := f*f, f*f*f
		w := [4]float64{-.5*f3 + f2 - .5*f, 1.5*f3 - 2.5*f2 + 1, -1.5*f3 + 2*f2 + .5*f, .5*f3 - .5*f2}
		for k := 0; k < 4; k++ {
			t.idx[x][k] = min(src-1, max(0, i-1+k))
			t.w[x][k] = float32(w[k])
		}
	}
	cubicTables[[2]int{dst, src}] = t
	return t
}

// upscaleDither scales a gw x gh float plane to w x h with Catmull-Rom interpolation, then adds a fixed
// triangular dither of about one code value before rounding. The dither stops smooth gradients from
// collapsing into 8-bit bands that the encoder then turns into blocks; being fixed in screen space,
// it costs almost nothing to encode between frames.
func upscaleDither(dst []byte, src []float32, w, h, plane int, lo, hi float32) {
	tx, ty := cubicWeights(w, gw), cubicWeights(h, gh)
	rows := make([]float32, gh*w)
	for y := 0; y < gh; y++ {
		s, d := src[y*gw:(y+1)*gw], rows[y*w:(y+1)*w]
		for x := 0; x < w; x++ {
			i, k := tx.idx[x], tx.w[x]
			d[x] = s[i[0]]*k[0] + s[i[1]]*k[1] + s[i[2]]*k[2] + s[i[3]]*k[3]
		}
	}
	ditherOnce.Do(buildDitherTiles)
	tile := &ditherTiles[plane]
	const workers = 4
	var wg sync.WaitGroup
	for part := 0; part < workers; part++ {
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			for y := y0; y < y1; y++ {
				i, k := ty.idx[y], ty.w[y]
				r0, r1, r2, r3 := rows[i[0]*w:], rows[i[1]*w:], rows[i[2]*w:], rows[i[3]*w:]
				out := dst[y*w : (y+1)*w]
				noise := tile[(y&63)*64 : (y&63)*64+64]
				for x := 0; x < w; x++ {
					v := r0[x]*k[0] + r1[x]*k[1] + r2[x]*k[2] + r3[x]*k[3] + noise[x&63]
					out[x] = byte(min(hi, max(lo, v)))
				}
			}
		}(h*part/workers, h*(part+1)/workers)
	}
	wg.Wait()
}

// ditherTiles hold one 64x64 tile of dither noise per plane, with the rounding offset of 0.5 included,
// so the per-pixel work is a table lookup instead of two noise evaluations.
var (
	ditherOnce  sync.Once
	ditherTiles [3][64 * 64]float32
)

func buildDitherTiles() {
	for plane := range ditherTiles {
		for y := 0; y < 64; y++ {
			for x := 0; x < 64; x++ {
				ditherTiles[plane][y*64+x] = ditherNoise(x, y, plane) + .5
			}
		}
	}
}

// ditherNoise is triangular noise in (-1, 1) from two interleaved-gradient-noise samples.
func ditherNoise(x, y, plane int) float32 {
	ign := func(x, y float32) float32 {
		f := .06711056*x + .00583715*y
		f -= float32(int(f))
		f *= 52.9829189
		return f - float32(int(f))
	}
	fx, fy := float32(x+plane*37), float32(y+plane*91)
	return ign(fx, fy) + ign(fx+113, fy+71) - 1
}

// dimmed is the frozen frame shown under Seeking, Buffering and Waiting, computed once per frame.
func (r *Renderer) dimmed(side string) []byte {
	src := r.last[side]
	if src == nil {
		return nil
	}
	if prev := r.dimSrc[side]; prev != nil && &prev[0] == &src[0] {
		return r.dimFrame[side]
	}
	out := make([]byte, FrameBytes)
	n := Width * Height
	dimOnce.Do(buildDimTable)
	for i := 0; i < n; i++ {
		out[i] = byte(16 + (int(src[i])-16)*int(dimTable[i])>>8)
	}
	for i := n; i < FrameBytes; i++ {
		out[i] = byte(128 + (int(src[i])-128)*200>>8)
	}
	r.dimSrc[side], r.dimFrame[side] = src, out
	return out
}

// dimTable scales luma for the frozen-frame scenes: 40% in the centre where the text sits, 75% at the edges.
var (
	dimOnce  sync.Once
	dimTable []uint8
)

func buildDimTable() {
	dimTable = make([]uint8, Width*Height)
	for y := 0; y < Height; y++ {
		ny := (float64(y)/Height - .5) * 2
		for x := 0; x < Width; x++ {
			nx := (float64(x)/Width - .5) * 2
			f := .75
			if d := math.Hypot(nx/1.1, ny/1.2); d < 1 {
				f -= .35 * (1 - d)
			}
			dimTable[y*Width+x] = uint8(f * 256)
		}
	}
}

// ---------- icons ----------

func iconDisc(c *canvas) {
	c.shape(0, 0, iconSize, iconSize, plate.alpha(.35/.46), sdCircle(84, 84, 78))
	c.shape(0, 0, iconSize, iconSize, white, sdRing(84, 84, 78, 7))
}

func (r *Renderer) icon(kind string) *overlay {
	return r.cached("icon:"+kind, func() *overlay {
		c := newCanvas(iconSize+48, iconSize+56) // room for the drop shadow
		ox, oy := 24.0, 18.0
		sh := func(f func(x, y float64) float64) func(x, y float64) float64 {
			return func(x, y float64) float64 { return f(x-ox, y-oy) }
		}
		fill := func(col rgba, f func(x, y float64) float64) { c.shape(0, 0, float64(c.w), float64(c.h), col, sh(f)) }
		switch kind {
		case "pause", "ready", "done", "wait":
			fill(plate.alpha(.35/.46), sdCircle(84, 84, 78))
			fill(white, sdRing(84, 84, 78, 7))
		}
		switch kind {
		case "pause":
			fill(white, sdRoundRect(58, 50, 75, 118, 6))
			fill(white, sdRoundRect(93, 50, 110, 118, 6))
		case "ready":
			fill(white, sdPolygon([][2]float64{{72, 56}, {120, 84}, {72, 112}}, 4))
		case "done":
			fill(white, sdSegment(52, 86, 74, 108, 10))
			fill(white, sdSegment(74, 108, 118, 60, 10))
		case "wait":
			for i, a := range []float32{1, .7, .4} {
				fill(white.alpha(a), sdCircle(54+30*float64(i), 84, 9))
			}
		case "empty":
			fill(white, func(x, y float64) float64 { return math.Abs(sdRoundRect(20, 38, 148, 130, 18)(x, y)) - 3.5 })
			fill(white, sdSegment(58, 146, 110, 146, 7))
			fill(white, sdCircle(84, 84, 6))
		}
		return c.withShadow(6, 24, .35).overlay()
	})
}

func (r *Renderer) spinnerIcon(clock float64) *overlay {
	step := int(math.Mod(clock/1.1, 1)*spinSteps) % spinSteps
	if o := r.spinner[step]; o != nil {
		return o
	}
	c := newCanvas(iconSize+48, iconSize+56)
	ox, oy := 24.0, 18.0
	a0 := float64(step) / spinSteps * 2 * math.Pi
	ring, arc := sdRing(84+ox, 84+oy, 66, 12), sdArc(84+ox, 84+oy, 66, 12, a0, math.Pi/2)
	c.shape(0, 0, float64(c.w), float64(c.h), white.alpha(.2), ring)
	c.shape(0, 0, float64(c.w), float64(c.h), white, arc)
	o := c.withShadow(6, 24, .35).overlay()
	r.spinner[step] = o
	return o
}

// ---------- scene pieces ----------

type textLine struct {
	f      *opentype.Font
	size   int
	lineH  float64
	before float64
	col    rgba
	text   string
	shadow bool
}

// centerBlock lays out the icon and text lines as one centered column and returns the text overlay,
// its canvas top in the frame, and the icon's top-left.
func (r *Renderer) centerBlock(key string, hasIcon bool, lines []textLine) (*overlay, int, int) {
	total := 0.0
	if hasIcon {
		total = iconSize + 34
	}
	for _, l := range lines {
		total += l.before + l.lineH
	}
	top := (float64(Height) - total) / 2
	iconTop := int(top)
	textTop := int(top) &^ 1
	o := r.cached("center:"+key, func() *overlay {
		pad := 40
		c := newCanvas(centerW, int(total)+2*pad)
		y := float64(pad)
		if hasIcon {
			y += iconSize + 34
		}
		shadowed, plain := newCanvas(c.w, c.h), newCanvas(c.w, c.h)
		for _, l := range lines {
			y += l.before
			if l.text != "" {
				w := r.measure(l.f, l.size, l.text)
				dst := plain
				if l.shadow {
					dst = shadowed
				}
				r.drawText(dst, l.f, l.size, l.text, (float64(c.w)-w)/2, r.baseline(l.f, l.size, y, l.lineH), l.col)
			}
			y += l.lineH
		}
		s := shadowed.withShadow(4, 30, .35)
		for i := 0; i < c.w*c.h; i++ {
			pa := plain.p[i*4+3]
			for k := 0; k < 4; k++ {
				c.p[i*4+k] = plain.p[i*4+k] + s.p[i*4+k]*(1-pa)
			}
		}
		return c.overlay()
	})
	return o, iconTop, textTop - 40
}

func (r *Renderer) card(m MusicInfo) *overlay {
	return r.cached(fmt.Sprintf("card:%s:%s:%s:%t", m.Path, m.Title, m.AlbumArtist+m.Artist, m.Cover != nil), func() *overlay {
		c := newCanvas(cardW, cardH)
		c.shape(0, 0, cardW, cardH, plate, sdRoundRect(0, 0, cardW, cardH, 32))
		c.shape(0, 0, cardW, cardH, white.alpha(.08), func(x, y float64) float64 {
			return math.Abs(sdRoundRect(.75, .75, cardW-.75, cardH-.75, 31.25)(x, y)) - .75
		})
		artBox := sdRoundRect(20, 20, 20+cardArt, 20+cardArt, 22)
		if m.Cover != nil {
			b := m.Cover.Bounds()
			for y := 20; y < 20+cardArt; y++ {
				for x := 20; x < 20+cardArt; x++ {
					cov := clamp(.5-artBox(float64(x)+.5, float64(y)+.5), 0, 1)
					if cov <= 0 {
						continue
					}
					sx, sy := (x-20)*b.Dx()/cardArt, (y-20)*b.Dy()/cardArt
					p := m.Cover.Pix[sy*m.Cover.Stride+sx*4:]
					c.over(y*c.w+x, rgba{float32(p[0]) / 255, float32(p[1]) / 255, float32(p[2]) / 255, 1}, float32(cov))
				}
			}
		} else {
			for y := 20; y < 20+cardArt; y++ {
				for x := 20; x < 20+cardArt; x++ {
					cov := clamp(.5-artBox(float64(x)+.5, float64(y)+.5), 0, 1)
					t := float32(x-20+y-20) / (2 * cardArt)
					c.over(y*c.w+x, rgba{(0x2a*(1-t) + 0x1a*t) / 255, (0x35*(1-t) + 0x22*t) / 255, (0x56*(1-t) + 0x38*t) / 255, 1}, float32(cov))
				}
			}
			// music note
			c.shape(60, 60, 150, 150, nezumi, sdSegment(102, 76, 102, 122, 6))
			c.shape(60, 60, 150, 150, nezumi, sdSegment(130, 70, 130, 114, 6))
			c.shape(60, 60, 150, 150, nezumi, sdSegment(102, 76, 130, 70, 6))
			c.shape(60, 60, 150, 150, nezumi, sdCircle(94, 124, 9))
			c.shape(60, 60, 150, 150, nezumi, sdCircle(122, 116, 9))
		}
		title := m.Title
		if title == "" {
			title = displayTitle(m.Path)
		}
		if title == "" {
			title = "Unknown song"
		}
		artist := m.AlbumArtist
		if artist == "" {
			artist = m.Artist
		}
		const mx, mw = 216.0, 560.0
		r.drawText(c, r.black, 44, r.fit(r.black, 44, title, mw), mx, r.baseline(r.black, 44, 26, 51), white)
		if artist != "" {
			r.drawText(c, r.bold, 28, r.fit(r.bold, 28, artist, mw), mx, r.baseline(r.bold, 28, 77, 40), white.alpha(.72))
		}
		return c.overlay()
	})
}

// cardBar is the music progress bar. It is drawn on its own, keyed by filled width in pixels, so it
// advances smoothly every frame while the time labels below it only change once a second.
func (r *Renderer) cardBar(m MusicInfo) *overlay {
	const w = 560
	fillPx := 0
	if m.Duration > 0 {
		fillPx = int(clamp(m.Position/m.Duration, 0, 1) * w)
	}
	if r.bar.o != nil && r.bar.key == fillPx {
		return r.bar.o
	}
	c := newCanvas(w, 8)
	c.shape(0, 0, w, 8, white.alpha(.18), sdRoundRect(0, 0, w, 8, 4))
	if fillPx > 0 {
		c.shape(0, 0, w, 8, white, sdRoundRect(0, 0, max(8, float64(fillPx)), 8, 4))
	}
	r.bar = ringCache{fillPx, c.overlay()}
	return r.bar.o
}

func (r *Renderer) cardProgress(m MusicInfo) *overlay {
	sec := int(m.Position)
	return r.cached(fmt.Sprintf("cardp:%s:%d:%d", m.Path, sec, int(m.Duration)), func() *overlay {
		const w = 560.0
		c := newCanvas(int(w), 50)
		col := white.alpha(.62)
		pos := clockText(m.Position)
		r.drawText(c, r.bold, 22, pos, 0, r.baseline(r.bold, 22, 14, 32), col)
		dur := "--:--"
		if m.Duration > 0 {
			dur = clockText(m.Duration)
		}
		r.drawText(c, r.bold, 22, dur, w-r.measure(r.bold, 22, dur), r.baseline(r.bold, 22, 14, 32), col)
		return c.overlay()
	})
}

func clockText(sec float64) string {
	s := int(max(0, sec))
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

type ringCache struct {
	key int
	o   *overlay
}

// ring identifies the output by its audio language, JP for Sub and EN for Dub, around the episode's
// progress at 50% opacity. It is on every non-playing screen (at 0% with nothing loaded) and, when the
// HUD setting is on, while playing.
func (r *Renderer) ring(frame []byte, progress float64, side string) {
	p := clamp(progress, 0, 1)
	key := int(p * 720)
	cache := r.rings[side]
	if cache.o == nil || cache.key != key {
		const c0, size = ringSize / 2.0, ringSize
		c := newCanvas(size, size)
		c.shape(0, 0, size, size, plate.alpha(.55/.46), sdCircle(c0, c0, 31))
		c.shape(0, 0, size, size, white.alpha(.28), sdRing(c0, c0, 26.5, 5))
		if key > 0 {
			c.shape(0, 0, size, size, white, sdArc(c0, c0, 26.5, 5, 0, float64(key)/720*2*math.Pi))
		}
		label := "JP"
		if side == "dub" {
			label = "EN"
		}
		// Centre the ink of the label, not its advance box, so it sits in the middle of the ring.
		const fs = 20
		b, _ := font.BoundString(r.face(r.black, fs), label)
		x := c0 - float64(b.Min.X+b.Max.X)/128
		y := c0 - float64(b.Min.Y+b.Max.Y)/128
		r.drawText(c, r.black, fs, label, x, y, white)
		cache = ringCache{key, c.overlay()}
		r.rings[side] = cache
	}
	cache.o.blend(frame, Width-ringMargin-ringSize, Height-ringMargin-ringSize, 128)
}

// ---------- render ----------

func (r *Renderer) Render(s Scene) []byte {
	start := time.Now()
	defer func() {
		r.mu.Lock()
		r.times = append(r.times, time.Since(start).Seconds()*1000)
		if len(r.times) > 1000 {
			r.times = r.times[len(r.times)-1000:]
		}
		r.mu.Unlock()
	}()
	if s.Phase == "playing" && s.Frame != nil {
		r.last[s.Side] = s.Frame
		if !s.HUD {
			return s.Frame
		}
		out := append([]byte(nil), s.Frame...)
		r.ring(out, s.Position/max(1, s.Duration), s.Side)
		return out
	}

	var frame []byte
	pt := math.Inf(1) // seconds into the pause transition; other phases show everything at once
	out := -1.0       // seconds into the resume transition, or negative when not resuming
	switch {
	case s.Phase == "paused" && len(s.Frame) == FrameBytes:
		r.last[s.Side] = s.Frame
		frame = append([]byte(nil), s.Frame...)
		pt = r.pauseElapsed(s)
		if s.Resuming {
			out = s.ResumeAge
		}
		if len(s.Blur) == FrameBytes {
			// The blur clears in the second half of the resume, ending on the sharp frame the video
			// continues from.
			if a := int(stage(pt, 0, .45) * (1 - stage(out, .2, .5)) * 256); a >= 256 {
				copy(frame, s.Blur)
			} else {
				for i, v := range frame {
					frame[i] = byte((int(v)*(256-a) + int(s.Blur[i])*a) >> 8)
				}
			}
		}
	case s.Phase == "seeking" || s.Phase == "buffering" || s.Phase == "waiting":
		if d := r.dimmed(s.Side); d != nil {
			frame = append([]byte(nil), d...)
		}
	}
	if frame == nil {
		frame = append([]byte(nil), r.background(s.Music, s.Clock)...)
	}

	title := displayTitle(s.Title)
	episode := ""
	if s.Total > 0 {
		episode = fmt.Sprintf("Episode %d of %d", s.Index+1, s.Total)
	}
	other := "Dub"
	if s.Side == "dub" {
		other = "Sub"
	}
	state := func(t string) textLine {
		return textLine{f: r.black, size: 92, lineH: 96.6, col: white, text: t, shadow: true}
	}
	what := func() []textLine {
		var out []textLine
		// Plex labels arrive as "Show · S01E01 · Episode title": show on one line, episode on the next.
		lines := r.wrap(r.bold, 40, title, 1300, 2)
		if parts := strings.Split(title, " · "); len(parts) > 1 {
			lines = []string{r.fit(r.bold, 40, parts[0], 1300), r.fit(r.bold, 40, strings.Join(parts[1:], " "), 1300)}
		}
		for i, l := range lines {
			before := 0.0
			if i == 0 {
				before = 22
			}
			out = append(out, textLine{f: r.bold, size: 40, lineH: 50, before: before, col: white, text: l, shadow: true})
		}
		return out
	}
	ep := func(t string) textLine {
		return textLine{f: r.bold, size: 32, lineH: 46, before: 12, col: white.alpha(.75), text: t}
	}
	hint := func(t string) textLine {
		return textLine{f: r.medium, size: 28, lineH: 40.6, before: 18, col: white.alpha(.62), text: t}
	}
	var icon *overlay
	var lines []textLine
	switch s.Phase {
	case "none":
		icon, lines = r.icon("empty"), []textLine{state("No media selected"), hint("Waiting for the host to choose an episode")}
	case "loading":
		icon, lines = r.spinnerIcon(s.Clock), append(append([]textLine{state("Loading")}, what()...), ep(episode))
	case "ready":
		icon, lines = r.icon("ready"), append(append([]textLine{state("Ready")}, what()...), ep(episode))
	case "paused":
		icon, lines = r.icon("pause"), append(append([]textLine{state("Media Paused")}, what()...), ep(episode))
	case "seeking":
		icon, lines = r.spinnerIcon(s.Clock), []textLine{state("Seeking")}
	case "buffering":
		icon, lines = r.spinnerIcon(s.Clock), []textLine{state("Buffering"), hint("Waiting for both streams to catch up")}
	case "waiting":
		icon, lines = r.icon("wait"), []textLine{state("Episode finished"), hint(fmt.Sprintf("The next episode starts when the %s stream finishes too", other))}
	case "ended":
		icon, lines = r.icon("done"), append(append([]textLine{state("Playlist finished")}, what()...), ep(fmt.Sprintf("All %d episodes played", s.Total)))
	default:
		lines = []textLine{state(PhaseLabel(s.Phase))}
	}
	key := fmt.Sprintf("%s|%s|%s|%d|%d|%s", s.Phase, s.Side, title, s.Index, s.Total, other)
	text, iconTop, textTop := r.centerBlock(key, icon != nil, lines)
	// Pause choreography, about 700 ms from the moment the blur is ready: the background blurs, then the
	// icon, music card and text fade in, the icon and text rising 16 px as they arrive.
	// Resuming plays it in reverse over half a second: text, music card, then icon fade and sink.
	iconIn := stage(pt, .25, .6) * (1 - stage(out, .1, .35))
	cardIn := stage(pt, .3, .7) * (1 - stage(out, .05, .3))
	textIn := stage(pt, .35, .7) * (1 - stage(out, 0, .2))
	rise := func(p float64) int { return int((1 - p) * 16) }
	text.blend(frame, (Width-centerW)/2, textTop+rise(textIn), int(textIn*256))
	if icon != nil {
		icon.blend(frame, (Width-iconSize)/2-24, iconTop-18+rise(iconIn), int(iconIn*256))
	}
	if s.Music.Active {
		r.card(s.Music).blend(frame, cardX, cardY, int(cardIn*256))
		r.cardBar(s.Music).blend(frame, cardX+216, cardY+136, int(cardIn*256))
		r.cardProgress(s.Music).blend(frame, cardX+216, cardY+136, int(cardIn*256))
	}
	if s.Side != "" {
		progress := 0.0
		if s.Duration > 0 {
			progress = s.Position / s.Duration
		}
		r.ring(frame, progress, s.Side)
	}
	return frame
}

type pauseState struct {
	age, blurAt float64
	started     bool
}

// pauseElapsed times the pause transition from the moment that output's blur is ready, not from the
// pause itself: preparing the blur takes a noticeable part of the transition, which used to be skipped.
// If no blur arrives within a second the transition runs anyway, so the pause card still appears.
// It returns a negative value until the transition starts.
func (r *Renderer) pauseElapsed(s Scene) float64 {
	st := r.pauses[s.Side]
	if s.PauseAge < st.age {
		st = pauseState{} // a new pause
	}
	st.age = s.PauseAge
	if !st.started && (len(s.Blur) == FrameBytes || s.PauseAge >= 1) {
		st.started, st.blurAt = true, s.PauseAge
	}
	r.pauses[s.Side] = st
	if !st.started {
		return -1
	}
	return s.PauseAge - st.blurAt
}

// stage maps transition time t onto one element's progress between from and to seconds, eased in and out.
func stage(t, from, to float64) float64 {
	x := clamp((t-from)/(to-from), 0, 1)
	return x * x * (3 - 2*x)
}

// PhaseLabel is the viewer-facing name of an engine phase.
func PhaseLabel(phase string) string {
	if phase == "" {
		return ""
	}
	return strings.ToUpper(phase[:1]) + phase[1:]
}

// pauseMasks hold, per pixel and scaled to 0..256, how much of the blur shows and how much the centre is
// dimmed. Index 0 is the luma plane, 1 the half-size chroma planes. Built once, they turn the per-pixel
// float maths of the pause blur into integer blends.
type pauseMask struct{ weight, dim []uint16 }

var (
	pauseMaskOnce sync.Once
	pauseMasks    [2]pauseMask
)

func buildPauseMasks() {
	for k, size := range [2][2]int{{Width, Height}, {Width / 2, Height / 2}} {
		w, h := size[0], size[1]
		m := pauseMask{make([]uint16, w*h), make([]uint16, w*h)}
		for y := 0; y < h; y++ {
			ny := (float64(y)/float64(h) - .5) * 2
			for x := 0; x < w; x++ {
				nx := (float64(x)/float64(w) - .5) * 2
				// Blur: an ellipse 58% x 62% of the frame, solid to 35%, 60% at 60%, gone by 92%.
				d := math.Hypot(nx/1.16, ny/1.24)
				var weight float64
				switch {
				case d < .35:
					weight = 1
				case d < .6:
					weight = 1 - .4*(d-.35)/.25
				case d < .92:
					weight = .6 * (1 - (d-.6)/.32)
				}
				dim := 0.0
				if dc := math.Hypot(nx/1.1, ny/1.2); dc < 1 {
					dim = .38 * (1 - dc)
				}
				m.weight[y*w+x], m.dim[y*w+x] = uint16(weight*256+.5), uint16(dim*256+.5)
			}
		}
		pauseMasks[k] = m
	}
}

// PauseBlur prepares the paused background: a heavy blur and dim in the center that fades
// to the untouched frame at the edges.
func PauseBlur(frame []byte) []byte {
	if len(frame) != FrameBytes {
		return nil
	}
	out := append([]byte(nil), frame...)
	n := Width * Height
	for plane := 0; plane < 3; plane++ {
		w, h, offset := Width, Height, 0
		if plane > 0 {
			w, h = Width/2, Height/2
			offset = n + (plane-1)*n/4
		}
		const down = 8
		sw, sh := w/down, h/down
		low := make([]float32, sw*sh)
		for y := 0; y < sh; y++ {
			for x := 0; x < sw; x++ {
				sum := 0
				for yy := 0; yy < down; yy++ {
					row := frame[offset+(y*down+yy)*w+x*down:]
					for xx := 0; xx < down; xx++ {
						sum += int(row[xx])
					}
				}
				low[y*sw+x] = float32(sum) / (down * down)
			}
		}
		rad := 7
		if plane > 0 {
			rad = 4
		}
		boxBlur(low, sw, sh, rad, 3)
		blur := make([]byte, sw*sh)
		for i, v := range low {
			blur[i] = byte(clamp(float64(v), 0, 255))
		}
		scaled := make([]byte, w*h)
		scalePlane(scaled, blur, w, h, sw, sh)
		pauseMaskOnce.Do(buildPauseMasks)
		mask := pauseMasks[min(plane, 1)]
		src, dst := frame[offset:offset+w*h], out[offset:offset+w*h]
		for i := range dst {
			wgt, bl := int(mask.weight[i]), int(scaled[i])
			if plane == 0 {
				bl = 16 + (bl-16)*154>>8 // 60% brightness
				v := (int(src[i])*(256-wgt) + bl*wgt) >> 8
				dst[i] = byte(16 + (v-16)*(256-int(mask.dim[i]))>>8)
			} else {
				bl = 128 + (bl-128)*184>>8 // 72% saturation
				dst[i] = byte((int(src[i])*(256-wgt) + bl*wgt) >> 8)
			}
		}
	}
	return out
}
