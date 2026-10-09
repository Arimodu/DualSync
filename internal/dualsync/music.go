package dualsync

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"image"
	"image/png"
	"io/fs"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var musicExt = map[string]bool{".flac": true, ".mp3": true, ".m4a": true, ".aac": true, ".ogg": true, ".opus": true, ".wav": true, ".aiff": true, ".alac": true}

type MusicUpcoming struct {
	Path     string  `json:"path"`
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Duration float64 `json:"duration"`
	CoverURL string  `json:"cover_url"`
}
type preparedMusic struct {
	reader  *RawReader
	info    MusicInfo
	err     string
	warning string
	release func()
}

func (p preparedMusic) close() {
	p.reader.Close()
	if p.release != nil {
		p.release()
	}
}

type Music struct {
	library         *Library
	state, folder   string
	settings        Settings
	reader          *RawReader
	info            MusicInfo
	release         func()
	queue           []preparedMusic
	results         chan preparedMusic
	requests        chan struct{}
	pending         int
	failed, replace bool
	skips           int
	nextRefresh     time.Time
	ctx             context.Context
	cancel          context.CancelFunc
	workerCancel    context.CancelFunc
	previous        preparedMusic
	crossSamples    int
	gain, target    float64
	rampSamples     int
	Error           string
}

func NewMusic(l *Library, state, folder string) *Music {
	return NewConfiguredMusic(l, state, Settings{MusicFolder: folder, MusicSource: "folder", MusicShuffle: true})
}
func NewConfiguredMusic(l *Library, state string, s Settings) *Music {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Music{library: l, state: state, ctx: ctx, cancel: cancel}
	m.Configure(s)
	return m
}
func (m *Music) discardWorker() {
	if m.workerCancel != nil {
		m.workerCancel()
	}
	for _, p := range m.queue {
		p.close()
	}
	m.queue = nil
	if m.results != nil {
		ch := m.results
		go func() {
			for p := range ch {
				p.close()
			}
		}()
	}
	m.results = nil
	m.requests = nil
	m.pending = 0
}
func (m *Music) Close() {
	if m.cancel != nil {
		m.cancel()
	}
	m.discardWorker()
	preparedMusic{reader: m.reader, release: m.release}.close()
	m.reader = nil
	m.release = nil
	m.previous.close()
	m.previous = preparedMusic{}
}
func (m *Music) Configure(s Settings) {
	m.discardWorker()
	if s.MusicSource == "" {
		s.MusicSource = "folder"
	}
	m.settings = s
	m.folder = s.MusicFolder
	m.failed = false
	m.Error = ""
	m.replace = true
	m.skips = 0
	m.nextRefresh = time.Now().Add(5 * time.Minute)
	if s.MusicSource == "off" || (s.MusicSource == "folder" && s.MusicFolder == "") {
		m.previous.close()
		m.previous = preparedMusic{}
		m.crossSamples = 0
		m.gain = 0
		m.target = 0
		m.rampSamples = 0
		preparedMusic{reader: m.reader, release: m.release}.close()
		m.reader = nil
		m.release = nil
		m.info = MusicInfo{}
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.workerCancel = cancel
	m.results = make(chan preparedMusic, 3)
	m.requests = make(chan struct{}, 3)
	lastID := m.info.Path
	if s.MusicSource == "plex" {
		lastID = strings.TrimPrefix(lastID, "plex:")
	}
	go m.worker(ctx, s, m.requests, m.results, lastID)
}
func (m *Music) Next() { m.skips++ }
func (m *Music) Mute() {
	m.gain = 0
	m.target = 0
	m.previous.close()
	m.previous = preparedMusic{}
	m.crossSamples = 0
}
func (m *Music) snapshot() MusicInfo {
	i := m.info
	i.Source = m.settings.MusicSource
	if i.Source == "" {
		i.Source = "folder"
	}
	i.PlaylistID = m.settings.PlexPlaylistID
	i.ReadyCount = len(m.queue)
	i.Upcoming = []MusicUpcoming{}
	i.Error = m.Error
	i.PreparationState = "ready"
	if m.failed {
		i.PreparationState = "failed"
	} else if m.pending > 0 {
		i.PreparationState = "preparing"
	} else if m.requests == nil || (m.reader == nil && len(m.queue) == 0 && m.pending == 0) {
		i.PreparationState = "idle"
	}
	for _, p := range m.queue {
		i.Upcoming = append(i.Upcoming, MusicUpcoming{p.info.Path, p.info.Title, p.info.Artist, p.info.Duration, p.info.CoverURL})
	}
	return i
}
func (m *Music) promote(cross bool) {
	if len(m.queue) == 0 {
		return
	}
	p := m.queue[0]
	m.queue = m.queue[1:]
	m.previous.close()
	m.previous = preparedMusic{}
	if cross && m.reader != nil && m.gain > 0 {
		m.previous = preparedMusic{reader: m.reader, release: m.release}
		m.crossSamples = 12000
	} else {
		preparedMusic{reader: m.reader, release: m.release}.close()
		m.crossSamples = 0
	}
	m.reader = p.reader
	m.info = p.info
	m.release = p.release
	m.replace = false
}
func (m *Music) worker(ctx context.Context, s Settings, requests <-chan struct{}, results chan<- preparedMusic, lastID string) {
	defer close(results)
	var entries []PlexTrack
	index := 0
	refreshed := time.Time{}
	plex := newPlexClient(s)
	playlistTitle := ""
	load := func() error {
		if s.MusicSource == "plex" {
			rows, err := plex.tracks(ctx, s.PlexPlaylistID)
			if err != nil {
				return err
			}
			entries = rows
			lists, e := plex.playlists(ctx)
			if e == nil {
				for _, x := range lists {
					if x.ID == s.PlexPlaylistID {
						playlistTitle = x.Title
					}
				}
			}
		}
		if s.MusicSource == "folder" {
			entries = nil
			full, err := m.library.Resolve(s.MusicFolder)
			if err != nil {
				return err
			}
			err = filepath.WalkDir(full, func(path string, d fs.DirEntry, e error) error {
				if ctx.Err() != nil {
					return fs.SkipAll
				}
				if e != nil {
					return nil
				}
				if d.IsDir() && (strings.HasPrefix(d.Name(), ".") || strings.EqualFold(d.Name(), "$RECYCLE.BIN")) {
					return filepath.SkipDir
				}
				if !d.IsDir() && d.Type()&os.ModeSymlink == 0 && musicExt[strings.ToLower(filepath.Ext(path))] {
					rel, _ := filepath.Rel(m.library.Root, path)
					entries = append(entries, PlexTrack{ID: filepath.ToSlash(rel), File: path})
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		if len(entries) == 0 {
			return fail("UNREADABLE", 422, "No readable music files found")
		}
		index = 0
		refreshed = time.Now()
		if s.MusicShuffle {
			rand.Shuffle(len(entries), func(i, j int) { entries[i], entries[j] = entries[j], entries[i] })
			if len(entries) > 1 && entries[0].ID == lastID {
				entries[0], entries[1] = entries[1], entries[0]
			}
		} else if lastID != "" {
			for i, track := range entries {
				if track.ID == lastID {
					index = (i + 1) % len(entries)
					break
				}
			}
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-requests:
		}
		if entries == nil || (s.MusicSource == "plex" && time.Since(refreshed) >= 5*time.Minute) {
			if err := load(); err != nil {
				select {
				case results <- preparedMusic{err: Redact(err.Error())}:
				case <-ctx.Done():
				}
				return
			}
		}
		var out preparedMusic
		skipped := false
		for attempt := 0; attempt < len(entries); attempt++ {
			if index >= len(entries) {
				index = 0
				if s.MusicShuffle {
					rand.Shuffle(len(entries), func(i, j int) { entries[i], entries[j] = entries[j], entries[i] })
					if len(entries) > 1 && entries[0].ID == lastID {
						entries[0], entries[1] = entries[1], entries[0]
					}
				}
			}
			track := entries[index]
			index++
			out = m.prepareTrack(ctx, plex, track, s.MusicSource)
			out.info.PlaylistTitle = playlistTitle
			if out.err == "" {
				lastID = track.ID
				if skipped {
					out.warning = "Unreadable music tracks skipped"
				}
				break
			}
			skipped = true
			if ctx.Err() != nil {
				out.close()
				return
			}
		}
		select {
		case results <- out:
		case <-ctx.Done():
			out.close()
			return
		}
		if out.err != "" {
			return
		}
	}
}
func (m *Music) prepareTrack(ctx context.Context, plex *plexClient, t PlexTrack, source string) preparedMusic {
	work, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	path := ""
	rel, err := filepath.Rel(m.library.Root, t.File)
	if err == nil {
		full, e := m.library.Resolve(filepath.ToSlash(rel))
		if e == nil {
			if f, e := os.Open(full); e == nil {
				f.Close()
				path = full
			}
		}
	}
	var release func()
	if path == "" && source == "plex" {
		path, release, err = plex.download(work, m.state, t.Key, 256<<20)
		if err != nil {
			return preparedMusic{err: err.Error()}
		}
	}
	failed := func(msg string) preparedMusic {
		if release != nil {
			release()
		}
		return preparedMusic{err: msg}
	}
	if path == "" {
		return failed("Music file unavailable")
	}
	probe, err := probeFile(work, path)
	if err != nil {
		return failed("Unreadable music file skipped")
	}
	audio := -1
	for _, x := range probe.Tracks {
		if x.Kind == "audio" {
			audio = x.Index
			break
		}
	}
	if audio < 0 {
		return failed("Music file has no audio")
	}
	reader, err := audioReader(path, 0, audio)
	if err != nil {
		return failed("Music decoder unavailable")
	}
	out := preparedMusic{reader: reader, release: release, info: MusicInfo{Path: filepath.ToSlash(rel), Title: probe.Title, Artist: probe.Artist, AlbumArtist: probe.AlbumArtist, Album: probe.Album, Duration: probe.Duration}}
	if source == "plex" {
		out.info.Path = "plex:" + t.ID
		out.info.Title = t.Title
		out.info.Artist = t.Artist
		out.info.AlbumArtist = t.AlbumArtist
		out.info.Album = t.Album
	}
	art := ""
	for _, x := range probe.Tracks {
		if x.Attached {
			art = path
			break
		}
	}
	if art == "" {
		for _, name := range []string{"cover.jpg", "folder.jpg", "cover.png", "folder.png"} {
			candidate := filepath.Join(filepath.Dir(path), name)
			r, _ := filepath.Rel(m.library.Root, candidate)
			if full, e := m.library.Resolve(filepath.ToSlash(r)); e == nil {
				if st, e := os.Stat(full); e == nil && st.Mode().IsRegular() {
					art = full
					break
				}
			}
		}
	}
	artCtx, artCancel := context.WithTimeout(work, 2*time.Second)
	defer artCancel()
	var artRelease func()
	if art == "" && source == "plex" && t.Art != "" {
		art, artRelease, _ = plex.download(artCtx, m.state, t.Art, 10<<20)
		if artRelease != nil {
			defer artRelease()
		}
	}
	if art != "" {
		if b, e := decodeImage(artCtx, art, 168, 168); e == nil {
			im := image.NewRGBA(image.Rect(0, 0, 168, 168))
			copy(im.Pix, b)
			out.info.Cover = im
			sum := sha256.Sum256([]byte(out.info.Path))
			id := hex.EncodeToString(sum[:16])
			dir := filepath.Join(m.state, "art")
			_ = os.MkdirAll(dir, 0700)
			if f, e := os.Create(filepath.Join(dir, id+".png")); e == nil {
				_ = png.Encode(f, im)
				f.Close()
				out.info.CoverURL = "/api/music/art/" + id
			}
		}
	}
	if out.info.Title == "" {
		out.info.Title = filepath.Base(t.File)
	}
	if out.info.AlbumArtist == "" {
		out.info.AlbumArtist = out.info.Artist
	}
	for !reader.Ready() {
		select {
		case <-work.Done():
			out.close()
			return preparedMusic{err: "Music preparation timed out"}
		case <-time.After(10 * time.Millisecond):
		}
	}
	if reader.EOF() {
		out.close()
		return preparedMusic{err: "Unreadable music file skipped"}
	}
	return out
}
func (m *Music) Tick(due, consume bool, volume float64) ([]byte, MusicInfo) {
	if m.settings.MusicSource == "plex" && consume && time.Now().After(m.nextRefresh) {
		m.Configure(m.settings)
		m.replace = false
	}
	if m.results != nil {
		for {
			select {
			case p, ok := <-m.results:
				if !ok {
					m.results = nil
					goto drained
				}
				m.pending--
				if p.err != "" {
					m.failed = true
					m.Error = p.err
				} else {
					m.queue = append(m.queue, p)
					if p.warning != "" {
						m.Error = p.warning
					}
				}
			default:
				goto drained
			}
		}
	}
drained:
	if m.reader != nil && m.reader.EOF() {
		preparedMusic{reader: m.reader, release: m.release}.close()
		m.reader = nil
		m.release = nil
	}
	if (m.reader == nil || m.replace) && len(m.queue) > 0 {
		m.promote(due && consume)
	}
	for m.skips > 0 && len(m.queue) > 0 {
		m.promote(due && consume)
		m.skips--
	}
	if m.requests != nil && !m.failed && consume {
		for len(m.queue)+m.pending < 3 {
			select {
			case m.requests <- struct{}{}:
				m.pending++
			default:
				goto filled
			}
		}
	}
filled:
	target := 0.0
	if due {
		target = volume / 100
	}
	if target != m.target {
		m.target = target
		m.rampSamples = 5 * 48000
		if target < m.gain {
			m.rampSamples = 48000 / 4
		}
	}
	info := m.snapshot()
	info.Active = due && m.reader != nil
	info.Gain = m.gain
	if !consume || m.reader == nil || (!due && m.gain <= 0) {
		return nil, info
	}
	data := m.reader.Take()
	if data == nil {
		return nil, info
	}
	if m.previous.reader != nil && m.crossSamples > 0 {
		previous := m.previous.reader.Take()
		mixed := append([]byte(nil), data...)
		for i := 0; i < len(mixed); i += 4 {
			alpha := 1 - float64(m.crossSamples)/12000
			for c := 0; c < 2; c++ {
				j := i + c*2
				a := int16(0)
				if j+2 <= len(previous) {
					a = int16(binary.LittleEndian.Uint16(previous[j:]))
				}
				b := int16(binary.LittleEndian.Uint16(data[j:]))
				binary.LittleEndian.PutUint16(mixed[j:], uint16(int16(float64(a)*(1-alpha)+float64(b)*alpha)))
			}
			if m.crossSamples > 0 {
				m.crossSamples--
			}
		}
		data = mixed
		if m.crossSamples == 0 {
			m.previous.close()
			m.previous = preparedMusic{}
		}
	}
	out := make([]byte, len(data))
	gain := m.gain
	for i := 0; i < len(data); i += 4 {
		if m.rampSamples > 0 {
			gain += (m.target - gain) / float64(m.rampSamples)
			m.rampSamples--
		} else {
			gain = m.target
		}
		for c := 0; c < 2; c++ {
			index := i + c*2
			sample := int16(binary.LittleEndian.Uint16(data[index:]))
			v := int16(math.Round(float64(sample) * gain))
			binary.LittleEndian.PutUint16(out[index:], uint16(v))
		}
	}
	m.gain = gain
	m.info.Position += float64(Samples) / 48000
	info = m.snapshot()
	info.Active = due || gain > 0
	info.Gain = gain
	return out, info
}
