package dualsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Track struct {
	Index       int    `json:"index"`
	Kind        string `json:"kind"`
	Codec       string `json:"codec"`
	Language    string `json:"language"`
	Title       string `json:"title"`
	Channels    int    `json:"channels"`
	Recommended string `json:"recommended"`
	Attached    bool   `json:"attached"`
}
type Probe struct {
	Path        string  `json:"path"`
	Duration    float64 `json:"duration"`
	Tracks      []Track `json:"tracks"`
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	AlbumArtist string  `json:"album_artist"`
	Album       string  `json:"album"`
}
type Entry struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Directory bool   `json:"directory"`
}
type Listing struct {
	Entries    []Entry `json:"entries"`
	NextCursor int     `json:"next_cursor"`
	Indexing   bool    `json:"indexing"`
	Truncated  bool    `json:"truncated"`
}
type cachedProbe struct {
	identity string
	size     int64
	mod      time.Time
	probe    Probe
}
type Library struct {
	Root                         string
	mu                           sync.Mutex
	probes                       map[string]cachedProbe
	index                        []Entry
	indexing, indexed, truncated bool
	slots                        chan struct{}
	cancel                       context.CancelFunc
	ctx                          context.Context
	generation                   uint64
}

var videoExt = map[string]bool{".mkv": true, ".mp4": true, ".m4v": true, ".webm": true, ".avi": true, ".mov": true, ".ts": true, ".m2ts": true, ".mpg": true, ".mpeg": true, ".ogv": true, ".wmv": true}

func NewLibrary(root string) (*Library, error) {
	root, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return nil, e
	}
	info, e := os.Stat(root)
	if e != nil || !info.IsDir() {
		return nil, fmt.Errorf("media root must be a readable directory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Library{Root: root, probes: map[string]cachedProbe{}, slots: make(chan struct{}, 2), cancel: cancel, ctx: ctx}, nil
}
func (l *Library) Close() { l.cancel() }
func (l *Library) Rescan() {
	l.mu.Lock()
	l.generation++
	l.index = nil
	l.indexing = false
	l.indexed = false
	l.truncated = false
	l.probes = map[string]cachedProbe{}
	l.mu.Unlock()
	l.startIndex()
}
func (l *Library) Resolve(rel string) (string, error) {
	if filepath.IsAbs(rel) || strings.ContainsRune(rel, 0) || strings.Contains(rel, "\\") {
		return "", fail("INVALID_PATH", 400, "use root-relative paths")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fail("OUTSIDE_ROOT", 403, "path escapes media root")
	}
	full, e := filepath.EvalSymlinks(filepath.Join(l.Root, clean))
	if e != nil {
		return "", fail("NOT_FOUND", 404, "library item not found")
	}
	back, e := filepath.Rel(l.Root, full)
	if e != nil || back == ".." || strings.HasPrefix(back, ".."+string(os.PathSeparator)) {
		return "", fail("OUTSIDE_ROOT", 403, "symlink escapes media root")
	}
	return full, nil
}
func (l *Library) startIndex() {
	l.mu.Lock()
	if l.indexing || l.indexed {
		l.mu.Unlock()
		return
	}
	l.indexing = true
	gen := l.generation
	l.mu.Unlock()
	go func() {
		count := 0
		_ = filepath.WalkDir(l.Root, func(path string, d fs.DirEntry, e error) error {
			if l.ctx.Err() != nil {
				return fs.SkipAll
			}
			if e != nil {
				return nil
			}
			if path == l.Root {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") || strings.EqualFold(d.Name(), "$RECYCLE.BIN") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if !d.IsDir() && !videoExt[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			rel, _ := filepath.Rel(l.Root, path)
			l.mu.Lock()
			if gen != l.generation {
				l.mu.Unlock()
				return fs.SkipAll
			}
			l.index = append(l.index, Entry{filepath.ToSlash(rel), d.Name(), d.IsDir()})
			l.mu.Unlock()
			count++
			if count >= 200000 {
				l.mu.Lock()
				l.truncated = true
				l.mu.Unlock()
				return fs.SkipAll
			}
			return nil
		})
		l.mu.Lock()
		if gen == l.generation {
			l.indexing = false
			l.indexed = true
		}
		l.mu.Unlock()
	}()
}
func (l *Library) Browse(path, q string, cursor, limit int) (Listing, error) {
	if cursor < 0 || limit < 1 || limit > 200 {
		return Listing{}, fail("INVALID_ARGUMENT", 400, "cursor must be nonnegative; limit 1..200")
	}
	full, e := l.Resolve(path)
	if e != nil {
		return Listing{}, e
	}
	st, e := os.Stat(full)
	if e != nil || !st.IsDir() {
		return Listing{}, fail("INVALID_ARGUMENT", 400, "browse path must be a directory")
	}
	items := []Entry{}
	indexing, truncated := false, false
	if q != "" {
		l.startIndex()
		l.mu.Lock()
		prefix := strings.Trim(path, "/")
		if prefix != "" {
			prefix += "/"
		}
		for _, item := range l.index {
			if strings.HasPrefix(item.Path, prefix) && strings.Contains(strings.ToLower(item.Path), strings.ToLower(q)) {
				items = append(items, item)
			}
		}
		indexing, truncated = l.indexing, l.truncated
		l.mu.Unlock()
	} else {
		dirs, e := os.ReadDir(full)
		if e != nil {
			return Listing{}, fail("UNREADABLE", 403, "directory unreadable")
		}
		for _, d := range dirs {
			if strings.HasPrefix(d.Name(), ".") || strings.EqualFold(d.Name(), "$RECYCLE.BIN") {
				continue
			}
			rel := filepath.ToSlash(filepath.Join(path, d.Name()))
			real, e := l.Resolve(rel)
			if e != nil {
				continue
			}
			st, e := os.Stat(real)
			if e != nil {
				continue
			}
			if st.IsDir() || videoExt[strings.ToLower(filepath.Ext(real))] {
				items = append(items, Entry{rel, d.Name(), st.IsDir()})
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Directory != items[j].Directory {
			return items[i].Directory
		}
		return naturalLess(items[i].Path, items[j].Path)
	})
	out := Listing{Entries: []Entry{}, NextCursor: -1, Indexing: indexing, Truncated: truncated}
	if cursor >= len(items) {
		return out, nil
	}
	end := min(cursor+limit, len(items))
	out.Entries = items[cursor:end]
	if end < len(items) {
		out.NextCursor = end
	}
	return out, nil
}

var numbers = regexp.MustCompile(`\d+|\D+`)

func naturalLess(a, b string) bool {
	aa, bb := numbers.FindAllString(strings.ToLower(a), -1), numbers.FindAllString(strings.ToLower(b), -1)
	for i := 0; i < min(len(aa), len(bb)); i++ {
		if aa[i] == bb[i] {
			continue
		}
		x, ex := strconv.Atoi(aa[i])
		y, ey := strconv.Atoi(bb[i])
		if ex == nil && ey == nil && x != y {
			return x < y
		}
		return aa[i] < bb[i]
	}
	return len(aa) < len(bb)
}
func (l *Library) Probe(ctx context.Context, rel string) (Probe, error) {
	full, e := l.Resolve(rel)
	if e != nil {
		return Probe{}, e
	}
	info, e := os.Stat(full)
	if e != nil || !info.Mode().IsRegular() {
		return Probe{}, fail("INVALID_ARGUMENT", 400, "media must be a regular file")
	}
	l.mu.Lock()
	cached, ok := l.probes[full]
	l.mu.Unlock()
	identity := ""
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		identity = fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	}
	if ok && cached.identity == identity && cached.size == info.Size() && cached.mod.Equal(info.ModTime()) {
		p := cached.probe
		p.Path = rel
		return p, nil
	}
	select {
	case l.slots <- struct{}{}:
		defer func() { <-l.slots }()
	case <-ctx.Done():
		return Probe{}, ctx.Err()
	}
	p, e := probeFile(ctx, full)
	if e != nil {
		return p, fail("PROBE_FAILED", 422, "%s", Redact(e.Error()))
	}
	p.Path = rel
	l.mu.Lock()
	if len(l.probes) > 4096 {
		l.probes = map[string]cachedProbe{}
	}
	l.probes[full] = cachedProbe{identity, info.Size(), info.ModTime(), p}
	l.mu.Unlock()
	return p, nil
}
func probeFile(ctx context.Context, path string) (Probe, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	b, e := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_format", "-show_streams", "-of", "json", path).Output()
	if e != nil {
		return Probe{}, fmt.Errorf("ffprobe could not read media: %w", e)
	}
	var raw struct {
		Streams []struct {
			Index       int               `json:"index"`
			Type        string            `json:"codec_type"`
			Codec       string            `json:"codec_name"`
			Channels    int               `json:"channels"`
			Tags        map[string]string `json:"tags"`
			Disposition struct {
				Attached int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
	}
	if e = json.Unmarshal(b, &raw); e != nil {
		return Probe{}, e
	}
	p := Probe{Tracks: []Track{}, Title: filepath.Base(path)}
	p.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	for k, v := range raw.Format.Tags {
		switch strings.ToLower(k) {
		case "title":
			p.Title = v
		case "artist":
			p.Artist = v
		case "album_artist", "albumartist":
			p.AlbumArtist = v
		case "album":
			p.Album = v
		}
	}
	if p.AlbumArtist == "" {
		p.AlbumArtist = p.Artist
	}
	for _, s := range raw.Streams {
		if s.Type != "audio" && s.Type != "video" && s.Type != "subtitle" {
			continue
		}
		t := Track{Index: s.Index, Kind: s.Type, Codec: s.Codec, Language: s.Tags["language"], Title: s.Tags["title"], Channels: s.Channels, Attached: s.Disposition.Attached != 0}
		if t.Kind == "audio" {
			if t.Language == "jpn" {
				t.Recommended = "sub"
			}
			if t.Language == "eng" {
				t.Recommended = "dub"
			}
		}
		if t.Kind == "subtitle" && t.Language == "eng" {
			if strings.Contains(strings.ToLower(t.Title), "sign") {
				t.Recommended = "dub"
			} else {
				t.Recommended = "sub"
			}
		}
		p.Tracks = append(p.Tracks, t)
	}
	return p, nil
}

type SourceSpec struct {
	Path      string      `json:"path,omitempty"`
	Plex      *PlexSource `json:"plex,omitempty"`
	Video     *int        `json:"video,omitempty"`
	Transport string      `json:"transport,omitempty"`
	Audio     *int        `json:"audio"`
	Subtitle  *int        `json:"subtitle"`
}
type ItemSpec struct {
	Title          string         `json:"title"`
	MetadataSource string         `json:"metadata_source,omitempty"`
	Metadata       *VideoMetadata `json:"metadata,omitempty"`
	Sub            SourceSpec     `json:"sub"`
	Dub            SourceSpec     `json:"dub"`
}
type LoadRequest struct {
	Mode       string     `json:"mode"`
	Title      string     `json:"title"`
	Items      []ItemSpec `json:"items"`
	StartIndex int        `json:"start_index"`
}
type Source struct {
	Spec            SourceSpec
	File            string
	Duration        float64
	Video           int
	Audio           int
	Subtitle        int
	SubtitleOrdinal int
	SubtitleCodec   string
	SubtitleFile    string
	CacheIdentity   string
	Metadata        *VideoMetadata
	Context         context.Context
	Probe           *Probe
}
type Item struct {
	Spec     ItemSpec
	Sub      Source
	Dub      Source
	Duration float64
}

func (l *Library) source(ctx context.Context, s SourceSpec, side string) (Source, error) {
	p, e := l.Probe(ctx, s.Path)
	if e != nil {
		return Source{}, e
	}
	full, e := l.Resolve(s.Path)
	if e != nil {
		return Source{}, e
	}
	return sourceFromProbe(s, p, full, side)
}
func sourceFromProbe(s SourceSpec, p Probe, full, side string) (Source, error) {
	var e error
	out := Source{Spec: s, File: full, Duration: p.Duration, Video: -1, Audio: -1, Subtitle: -1, SubtitleOrdinal: -1}
	out.Probe = &p
	audio, subs := []Track{}, []Track{}
	for _, t := range p.Tracks {
		if t.Kind == "video" && !t.Attached && (s.Video == nil && out.Video < 0 || s.Video != nil && t.Index == *s.Video) {
			out.Video = t.Index
		}
		if t.Kind == "audio" {
			audio = append(audio, t)
		}
		if t.Kind == "subtitle" {
			subs = append(subs, t)
		}
	}
	if out.Video < 0 && s.Video != nil {
		return out, fail("INVALID_TRACK", 400, "video track index unavailable")
	}
	if out.Video < 0 || p.Duration <= 0 {
		return out, fail("UNSUPPORTED_MEDIA", 422, "media needs video and a finite duration")
	}
	out.Spec.Video = &out.Video
	choose := func(list []Track, requested *int) (int, error) {
		if requested != nil {
			if *requested == -1 {
				return -1, nil
			}
			for _, t := range list {
				if t.Index == *requested {
					return t.Index, nil
				}
			}
			return -1, fail("INVALID_TRACK", 400, "track index not available for this kind")
		}
		selected := -1
		for _, t := range list {
			if t.Recommended == side {
				if selected >= 0 {
					return -1, fail("AMBIGUOUS_TRACK", 400, "select a track explicitly when recommendations are ambiguous")
				}
				selected = t.Index
			}
		}
		return selected, nil
	}
	out.Audio, e = choose(audio, s.Audio)
	if e != nil {
		return out, e
	}
	if out.Audio < 0 && s.Audio == nil && len(audio) > 0 {
		if len(audio) > 1 {
			return out, fail("AMBIGUOUS_TRACK", 400, "select audio explicitly when no unique recommendation exists")
		}
		out.Audio = audio[0].Index
	}
	out.Subtitle, e = choose(subs, s.Subtitle)
	if e != nil {
		return out, e
	}
	for ordinal, t := range subs {
		if t.Index == out.Subtitle {
			out.SubtitleOrdinal = ordinal
			out.SubtitleCodec = t.Codec
		}
	}
	out.Spec.Audio = &out.Audio
	out.Spec.Subtitle = &out.Subtitle
	return out, nil
}

type PlaylistRequest struct {
	SubFolder string `json:"sub_folder"`
	DubFolder string `json:"dub_folder"`
}
type PlaylistPreview struct {
	Items     []ItemSpec `json:"items"`
	Unmatched []string   `json:"unmatched"`
	Ambiguous []string   `json:"ambiguous"`
}

var episodeID = regexp.MustCompile(`(?i)S(\d+)E(\d+)|S(\d+)\s*-\s*(\d+)|(?:^|[ _-])(\d{1,3})(?:[ ._\[-]|$)`)

func pairKey(name string) string {
	m := episodeID.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	if m[1] != "" {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		return fmt.Sprintf("%d:%d", a, b)
	}
	if m[3] != "" {
		a, _ := strconv.Atoi(m[3])
		b, _ := strconv.Atoi(m[4])
		return fmt.Sprintf("%d:%d", a, b)
	}
	b, _ := strconv.Atoi(m[5])
	return fmt.Sprintf("0:%d", b)
}
func (l *Library) Playlist(r PlaylistRequest) (PlaylistPreview, error) {
	collect := func(folder string) ([]string, error) {
		full, e := l.Resolve(folder)
		if e != nil {
			return nil, e
		}
		dirs, e := os.ReadDir(full)
		if e != nil {
			return nil, fail("INVALID_ARGUMENT", 400, "playlist folder unreadable")
		}
		list := []string{}
		for _, d := range dirs {
			if !d.IsDir() && videoExt[strings.ToLower(filepath.Ext(d.Name()))] {
				rel := filepath.ToSlash(filepath.Join(folder, d.Name()))
				if _, e := l.Resolve(rel); e == nil {
					list = append(list, rel)
				}
			}
		}
		sort.Slice(list, func(i, j int) bool { return naturalLess(list[i], list[j]) })
		return list, nil
	}
	a, e := collect(r.SubFolder)
	if e != nil {
		return PlaylistPreview{}, e
	}
	p := PlaylistPreview{Items: []ItemSpec{}, Unmatched: []string{}, Ambiguous: []string{}}
	if r.DubFolder == "" {
		for _, s := range a {
			p.Items = append(p.Items, ItemSpec{Title: filepath.Base(s), Sub: SourceSpec{Path: s}, Dub: SourceSpec{Path: s}})
		}
		return p, nil
	}
	b, e := collect(r.DubFolder)
	if e != nil {
		return p, e
	}
	maps := []map[string][]string{{}, {}}
	for side, list := range [][]string{a, b} {
		for _, s := range list {
			k := pairKey(filepath.Base(s))
			if k == "" {
				p.Unmatched = append(p.Unmatched, s)
				continue
			}
			maps[side][k] = append(maps[side][k], s)
		}
	}
	keys := []string{}
	for k := range maps[0] {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return naturalLess(keys[i], keys[j]) })
	for _, k := range keys {
		aa, bb := maps[0][k], maps[1][k]
		if len(aa) > 1 || len(bb) > 1 {
			p.Ambiguous = append(p.Ambiguous, append(aa, bb...)...)
		} else if len(bb) == 0 {
			p.Unmatched = append(p.Unmatched, aa...)
		} else {
			p.Items = append(p.Items, ItemSpec{Title: filepath.Base(aa[0]), Sub: SourceSpec{Path: aa[0]}, Dub: SourceSpec{Path: bb[0]}})
		}
		delete(maps[1], k)
	}
	for _, bb := range maps[1] {
		p.Unmatched = append(p.Unmatched, bb...)
	}
	return p, nil
}
