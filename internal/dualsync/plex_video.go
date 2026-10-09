package dualsync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type PlexSource struct {
	ID       string `json:"id"`
	MediaID  string `json:"media_id,omitempty"`
	PartID   string `json:"part_id,omitempty"`
	ServerID string `json:"server_id,omitempty"`
}
type VideoMetadata struct {
	ID          string   `json:"id"`
	Matched     bool     `json:"matched"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Show        string   `json:"show"`
	Season      int      `json:"season"`
	Episode     int      `json:"episode"`
	Year        int      `json:"year"`
	Summary     string   `json:"summary"`
	Identifiers []string `json:"identifiers"`
	CoverURL    string   `json:"cover_url"`
	PosterURL   string   `json:"poster_url"`
}
type PlexPart struct {
	ID   int    `json:"id"`
	File string `json:"file"`
	Key  string `json:"key"`
	Size int64  `json:"size"`
}
type PlexVersion struct {
	ID         int        `json:"id"`
	Resolution string     `json:"videoResolution"`
	VideoCodec string     `json:"videoCodec"`
	AudioCodec string     `json:"audioCodec"`
	Parts      []PlexPart `json:"Part"`
}
type PlexVideo struct {
	ID          string `json:"ratingKey"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	Show        string `json:"grandparentTitle"`
	ParentTitle string `json:"parentTitle"`
	Season      int    `json:"parentIndex"`
	Episode     int    `json:"index"`
	Year        int    `json:"year"`
	Summary     string `json:"summary"`
	GUID        string `json:"guid"`
	GUIDs       []struct {
		ID string `json:"id"`
	} `json:"Guid"`
	Thumb    string        `json:"thumb"`
	Poster   string        `json:"grandparentThumb"`
	Updated  int64         `json:"updatedAt"`
	Versions []PlexVersion `json:"Media"`
}
type PlexVideoEntry struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Title   string `json:"title"`
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
}
type PlexVideoDetails struct {
	ServerID string         `json:"server_id"`
	Metadata VideoMetadata  `json:"metadata"`
	Versions []VideoVersion `json:"versions"`
}
type VideoVersion struct {
	ID         string      `json:"id"`
	Resolution string      `json:"resolution"`
	VideoCodec string      `json:"video_codec"`
	AudioCodec string      `json:"audio_codec"`
	Parts      []VideoPart `json:"parts"`
}
type VideoPart struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}
type PlexLibrary struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

func numericID(id string) bool {
	_, err := strconv.ParseUint(id, 10, 64)
	return err == nil && id != ""
}
func (p *plexClient) videoJSON(ctx context.Context, key string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(p.settings.PlexServer, "/")+key, nil)
	if err != nil {
		return fail("PLEX_FAILED", 502, "invalid Plex server")
	}
	req.Header.Set("X-Plex-Token", p.settings.PlexToken)
	req.Header.Set("Accept", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return fail("PLEX_FAILED", 502, "Plex unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fail("PLEX_FAILED", 502, "Plex returned HTTP %d", res.StatusCode)
	}
	if json.NewDecoder(io.LimitReader(res.Body, 20<<20)).Decode(dst) != nil {
		return fail("PLEX_FAILED", 502, "invalid Plex response")
	}
	return nil
}
func (p *plexClient) serverID(ctx context.Context) (string, error) {
	var raw struct {
		MediaContainer struct {
			ID string `json:"machineIdentifier"`
		}
	}
	err := p.videoJSON(ctx, "/identity", &raw)
	if err == nil && raw.MediaContainer.ID == "" {
		err = fail("PLEX_FAILED", 502, "Plex server identity unavailable")
	}
	return raw.MediaContainer.ID, err
}
func (p *plexClient) libraries(ctx context.Context) ([]PlexLibrary, error) {
	var raw struct {
		MediaContainer struct {
			Directory []struct{ Key, Title, Type string }
		}
	}
	if err := p.videoJSON(ctx, "/library/sections", &raw); err != nil {
		return nil, err
	}
	out := []PlexLibrary{}
	for _, r := range raw.MediaContainer.Directory {
		if r.Type == "show" || r.Type == "movie" {
			out = append(out, PlexLibrary{r.Key, r.Title, r.Type})
		}
	}
	return out, nil
}
func (p *plexClient) videoRows(ctx context.Context, key string) ([]PlexVideo, error) {
	var raw struct {
		MediaContainer struct {
			Metadata  []PlexVideo
			TotalSize int
		}
	}
	if err := p.videoJSON(ctx, key, &raw); err != nil {
		return nil, err
	}
	return raw.MediaContainer.Metadata, nil
}
func (p *plexClient) video(ctx context.Context, id string) (PlexVideo, error) {
	if !numericID(id) {
		return PlexVideo{}, fail("INVALID_ARGUMENT", 400, "invalid Plex video ID")
	}
	rows, err := p.videoRows(ctx, "/library/metadata/"+id)
	if err != nil {
		return PlexVideo{}, err
	}
	if len(rows) != 1 || rows[0].ID != id {
		return PlexVideo{}, fail("NOT_FOUND", 404, "Plex item unavailable")
	}
	return rows[0], nil
}
func (v PlexVideo) metadata() VideoMetadata {
	ids := []string{}
	if matchedGUID(v.GUID) {
		ids = append(ids, v.GUID)
	}
	for _, g := range v.GUIDs {
		if matchedGUID(g.ID) {
			ids = append(ids, g.ID)
		}
	}
	m := VideoMetadata{v.ID, len(ids) > 0, v.Type, v.Title, v.Show, v.Season, v.Episode, v.Year, v.Summary, ids, "", ""}
	if v.Type == "season" {
		m.Season = v.Episode
		m.Episode = 0
		m.Show = v.ParentTitle
	}
	if v.Thumb != "" {
		m.CoverURL = "/api/plex/artwork?id=" + v.ID
	}
	if v.Poster != "" || v.Thumb != "" {
		m.PosterURL = "/api/plex/artwork?id=" + v.ID + "&kind=poster"
	}
	return m
}
func matchedGUID(guid string) bool {
	scheme, _, ok := strings.Cut(guid, "://")
	if !ok {
		return false
	}
	switch scheme {
	case "plex", "imdb", "tmdb", "tvdb", "com.plexapp.agents.imdb", "com.plexapp.agents.themoviedb", "com.plexapp.agents.thetvdb":
		return true
	}
	return false
}
func (v PlexVideo) entry() PlexVideoEntry {
	entry := PlexVideoEntry{v.ID, v.Type, v.Title, v.Season, v.Episode}
	if v.Type == "season" {
		entry.Season = v.Episode
		entry.Episode = 0
	}
	return entry
}
func (v PlexVideo) details(server string) PlexVideoDetails {
	d := PlexVideoDetails{ServerID: server, Metadata: v.metadata(), Versions: []VideoVersion{}}
	d.Metadata.pinArtwork(server)
	for _, m := range v.Versions {
		version := VideoVersion{strconv.Itoa(m.ID), m.Resolution, m.VideoCodec, m.AudioCodec, []VideoPart{}}
		for _, part := range m.Parts {
			version.Parts = append(version.Parts, VideoPart{strconv.Itoa(part.ID), filepath.Base(part.File), part.Size})
		}
		d.Versions = append(d.Versions, version)
	}
	return d
}
func (v PlexVideo) part(ref PlexSource) (PlexVersion, PlexPart, error) {
	if v.Type != "episode" && v.Type != "movie" {
		return PlexVersion{}, PlexPart{}, fail("UNSUPPORTED_MEDIA", 422, "select a movie or episode")
	}
	if ref.MediaID == "" && len(v.Versions) != 1 {
		return PlexVersion{}, PlexPart{}, fail("AMBIGUOUS_VERSION", 400, "select a Plex media version")
	}
	for _, m := range v.Versions {
		if ref.MediaID != "" && ref.MediaID != strconv.Itoa(m.ID) {
			continue
		}
		if len(m.Parts) != 1 {
			return m, PlexPart{}, fail("UNSUPPORTED_MEDIA", 422, "multipart Plex versions are unsupported")
		}
		part := m.Parts[0]
		if ref.PartID != "" && ref.PartID != strconv.Itoa(part.ID) {
			continue
		}
		return m, part, nil
	}
	return PlexVersion{}, PlexPart{}, fail("NOT_FOUND", 404, "Plex version or part unavailable")
}

type videoResource struct {
	client *plexClient
	key    string
	ctx    context.Context
}
type videoProxy struct {
	mu        sync.Mutex
	resources map[string]videoResource
	listener  net.Listener
	server    *http.Server
}

func newVideoProxy() (*videoProxy, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &videoProxy{resources: map[string]videoResource{}, listener: listener}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second}
	go p.server.Serve(listener)
	return p, nil
}
func (p *videoProxy) Close() { p.server.Close() }
func (p *videoProxy) register(ctx context.Context, client *plexClient, key string) (string, error) {
	u, err := url.Parse(key)
	if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || path.Clean(u.Path) != u.Path || !strings.HasPrefix(u.Path, "/library/parts/") {
		return "", fail("PLEX_FAILED", 502, "invalid Plex media resource")
	}
	b := make([]byte, 24)
	if _, err = rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	p.mu.Lock()
	p.resources[id] = videoResource{client, key, ctx}
	p.mu.Unlock()
	context.AfterFunc(ctx, func() { p.mu.Lock(); delete(p.resources, id); p.mu.Unlock() })
	return "http://" + p.listener.Addr().String() + "/" + id, nil
}
func (p *videoProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(405)
		return
	}
	p.mu.Lock()
	resource, ok := p.resources[strings.TrimPrefix(r.URL.Path, "/")]
	p.mu.Unlock()
	if !ok || resource.ctx.Err() != nil {
		w.WriteHeader(404)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(resource.ctx, cancel)
	defer stop()
	req, err := http.NewRequestWithContext(ctx, r.Method, strings.TrimRight(resource.client.settings.PlexServer, "/")+resource.key, nil)
	if err != nil {
		w.WriteHeader(502)
		return
	}
	req.Header.Set("X-Plex-Token", resource.client.settings.PlexToken)
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	timer := time.AfterFunc(90*time.Second, cancel)
	defer timer.Stop()
	client := *resource.client.client
	client.Timeout = 0
	response, err := client.Do(req)
	if err != nil {
		w.WriteHeader(502)
		return
	}
	defer response.Body.Close()
	if rangeHeader != "" && response.StatusCode != 206 && response.StatusCode != 416 {
		w.WriteHeader(502)
		return
	}
	if response.StatusCode != 200 && response.StatusCode != 206 && response.StatusCode != 416 {
		w.WriteHeader(502)
		return
	}
	for _, key := range []string{"Content-Length", "Content-Range", "Content-Type", "Accept-Ranges"} {
		if value := response.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if r.Method == "HEAD" {
		return
	}
	buf := make([]byte, 64<<10)
	for {
		n, err := response.Body.Read(buf)
		timer.Reset(90 * time.Second)
		if n > 0 {
			if _, e := w.Write(buf[:n]); e != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func validateSource(s SourceSpec) error {
	if (s.Path == "") == (s.Plex == nil) {
		return fail("INVALID_ARGUMENT", 400, "provide exactly one storage path or Plex source")
	}
	if s.Video != nil && *s.Video < 0 {
		return fail("INVALID_TRACK", 400, "video index must be nonnegative")
	}
	if s.Audio != nil && *s.Audio < -1 || s.Subtitle != nil && *s.Subtitle < -1 {
		return fail("INVALID_TRACK", 400, "audio and subtitle indices must be -1 or nonnegative")
	}
	if s.Plex != nil && (!numericID(s.Plex.ID) || s.Plex.MediaID != "" && !numericID(s.Plex.MediaID) || s.Plex.PartID != "" && !numericID(s.Plex.PartID)) {
		return fail("INVALID_ARGUMENT", 400, "invalid Plex source IDs")
	}
	return nil
}
func sameSource(a, b SourceSpec) bool {
	if a.Plex == nil || b.Plex == nil {
		return a.Plex == nil && b.Plex == nil && a.Path == b.Path
	}
	return *a.Plex == *b.Plex
}
func (e *Engine) resolveVideo(ctx, life context.Context, settings Settings, s SourceSpec, side string) (Source, error) {
	if s.Plex == nil {
		source, err := e.Library.source(ctx, s, side)
		source.Spec.Transport = "local"
		source.Context = life
		return source, err
	}
	p := newPlexClient(settings)
	if settings.PlexServer == "" || settings.PlexToken == "" {
		return Source{}, fail("INVALID_ARGUMENT", 400, "configure Plex server and token first")
	}
	server, err := p.serverID(ctx)
	if err != nil {
		return Source{}, err
	}
	if s.Plex.ServerID != "" && s.Plex.ServerID != server {
		return Source{}, fail("PLEX_SERVER_CHANGED", 409, "selection belongs to another Plex server")
	}
	video, err := p.video(ctx, s.Plex.ID)
	if err != nil {
		return Source{}, err
	}
	version, part, err := video.part(*s.Plex)
	if err != nil {
		return Source{}, err
	}
	ref := *s.Plex
	ref.ServerID = server
	ref.MediaID = strconv.Itoa(version.ID)
	ref.PartID = strconv.Itoa(part.ID)
	s.Plex = &ref
	file := ""
	transport := "plex"
	if part.File != "" {
		if rel, err := filepath.Rel(e.Library.Root, part.File); err == nil {
			if full, err := e.Library.Resolve(filepath.ToSlash(rel)); err == nil {
				file = full
				transport = "local"
			}
		}
	}
	var probe Probe
	if file != "" {
		rel, _ := filepath.Rel(e.Library.Root, file)
		probe, err = e.Library.Probe(ctx, filepath.ToSlash(rel))
		if err != nil {
			file = ""
			transport = "plex"
		}
	}
	if file == "" {
		file, err = e.videoProxy.register(life, p, part.Key)
		if err != nil {
			return Source{}, err
		}
		rangeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		req, _ := http.NewRequestWithContext(rangeCtx, "GET", file, nil)
		req.Header.Set("Range", "bytes=0-0")
		response, rangeErr := http.DefaultClient.Do(req)
		if rangeErr == nil {
			response.Body.Close()
			if response.StatusCode != 206 {
				rangeErr = fmt.Errorf("range unavailable")
			}
		}
		cancel()
		if rangeErr != nil {
			return Source{}, fail("PLEX_FAILED", 502, "Plex original media is unavailable or not seekable")
		}
		identity := fmt.Sprintf("plex:%s:%s:%d:%d", server, part.Key, part.Size, video.Updated)
		e.Library.mu.Lock()
		cached, ok := e.Library.probes[identity]
		e.Library.mu.Unlock()
		if ok {
			probe = cached.probe
		} else {
			probe, err = probeFile(ctx, file)
			if err != nil {
				return Source{}, fail("PROBE_FAILED", 422, "Plex original media could not be probed")
			}
			probe.Path = ""
			probe.Title = video.Title
			e.Library.mu.Lock()
			if len(e.Library.probes) > 4096 {
				e.Library.probes = map[string]cachedProbe{}
			}
			e.Library.probes[identity] = cachedProbe{probe: probe}
			e.Library.mu.Unlock()
		}
	}
	s.Transport = transport
	source, err := sourceFromProbe(s, probe, file, side)
	source.Context = life
	if transport == "plex" {
		source.CacheIdentity = fmt.Sprintf("plex:%s:%s:%d:%d", server, part.Key, part.Size, video.Updated)
	}
	metadata := video.metadata()
	metadata.pinArtwork(server)
	source.Metadata = &metadata
	return source, err
}
func itemMetadata(a, b *VideoMetadata, choice string) (*VideoMetadata, error) {
	if a != nil && !a.Matched {
		a = nil
	}
	if b != nil && !b.Matched {
		b = nil
	}
	if choice == "sub" {
		if a == nil {
			return nil, fail("INVALID_ARGUMENT", 400, "Sub has no matched Plex metadata")
		}
		return a, nil
	}
	if choice == "dub" {
		if b == nil {
			return nil, fail("INVALID_ARGUMENT", 400, "Dub has no matched Plex metadata")
		}
		return b, nil
	}
	if choice != "" {
		return nil, fail("INVALID_ARGUMENT", 400, "metadata_source must be sub or dub")
	}
	if a == nil {
		return b, nil
	}
	if b == nil {
		return a, nil
	}
	for _, x := range a.Identifiers {
		for _, y := range b.Identifiers {
			if x == y {
				return a, nil
			}
		}
	}
	return nil, fail("METADATA_CONFLICT", 409, "Plex selections identify different media; choose metadata_source sub or dub")
}
func metadataTitle(m *VideoMetadata) string {
	if m.Type == "episode" {
		return fmt.Sprintf("%s · S%02dE%02d · %s", m.Show, m.Season, m.Episode, m.Title)
	}
	return m.Title
}

type CollectionSpec struct {
	Folder       *string `json:"folder,omitempty"`
	PlexSeasonID string  `json:"plex_season_id,omitempty"`
}
type VideoPreviewRequest struct {
	Sub CollectionSpec  `json:"sub"`
	Dub *CollectionSpec `json:"dub,omitempty"`
}
type collectionEntry struct {
	key, label string
	source     SourceSpec
}

func (e *Engine) videoCollection(ctx context.Context, p *plexClient, c CollectionSpec) ([]collectionEntry, error) {
	if (c.Folder == nil) == (c.PlexSeasonID == "") {
		return nil, fail("INVALID_ARGUMENT", 400, "provide a folder or Plex season")
	}
	out := []collectionEntry{}
	if c.PlexSeasonID == "" {
		preview, err := e.Library.Playlist(PlaylistRequest{SubFolder: *c.Folder})
		if err != nil {
			return nil, err
		}
		for _, item := range preview.Items {
			out = append(out, collectionEntry{pairKey(filepath.Base(item.Sub.Path)), item.Sub.Path, item.Sub})
		}
		return out, nil
	}
	season, err := p.video(ctx, c.PlexSeasonID)
	if err != nil {
		return nil, err
	}
	if season.Type != "season" {
		return nil, fail("INVALID_ARGUMENT", 400, "select a Plex season")
	}
	server, err := p.serverID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := p.videoRows(ctx, "/library/metadata/"+c.PlexSeasonID+"/children?X-Plex-Container-Start=0&X-Plex-Container-Size=501")
	if err != nil {
		return nil, err
	}
	if len(rows) > 500 {
		return nil, fail("INVALID_ARGUMENT", 400, "season exceeds 500 items")
	}
	for _, row := range rows {
		if row.Type != "episode" {
			continue
		}
		ref := PlexSource{ID: row.ID, ServerID: server}
		out = append(out, collectionEntry{fmt.Sprintf("%d:%d", row.Season, row.Episode), row.Title, SourceSpec{Plex: &ref}})
	}
	sort.Slice(out, func(i, j int) bool { return naturalLess(out[i].key, out[j].key) })
	return out, nil
}
func (m *VideoMetadata) pinArtwork(server string) {
	if m.CoverURL != "" {
		m.CoverURL += "&server_id=" + url.QueryEscape(server)
	}
	if m.PosterURL != "" {
		m.PosterURL += "&server_id=" + url.QueryEscape(server)
	}
}
func (e *Engine) videoPreview(ctx context.Context, p *plexClient, r VideoPreviewRequest) (PlaylistPreview, error) {
	a, err := e.videoCollection(ctx, p, r.Sub)
	if err != nil {
		return PlaylistPreview{}, err
	}
	out := PlaylistPreview{Items: []ItemSpec{}, Unmatched: []string{}, Ambiguous: []string{}}
	if r.Dub == nil {
		for _, entry := range a {
			out.Items = append(out.Items, ItemSpec{Sub: entry.source, Dub: entry.source})
		}
		return out, nil
	}
	b, err := e.videoCollection(ctx, p, *r.Dub)
	if err != nil {
		return out, err
	}
	seasons := map[string]bool{}
	for _, entry := range append(append([]collectionEntry{}, a...), b...) {
		parts := strings.Split(entry.key, ":")
		if len(parts) == 2 && parts[0] != "0" {
			seasons[parts[0]] = true
		}
	}
	maps := []map[string][]collectionEntry{{}, {}}
	for i, entries := range [][]collectionEntry{a, b} {
		for _, entry := range entries {
			if strings.HasPrefix(entry.key, "0:") && len(seasons) == 1 {
				for season := range seasons {
					entry.key = season + entry.key[1:]
				}
			}
			if entry.key == "" {
				out.Unmatched = append(out.Unmatched, entry.label)
			} else {
				maps[i][entry.key] = append(maps[i][entry.key], entry)
			}
		}
	}
	keys := []string{}
	for key := range maps[0] {
		keys = append(keys, key)
	}
	for key := range maps[1] {
		if _, ok := maps[0][key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return naturalLess(keys[i], keys[j]) })
	for _, key := range keys {
		aa, bb := maps[0][key], maps[1][key]
		if len(aa) > 1 || len(bb) > 1 {
			for _, row := range append(aa, bb...) {
				out.Ambiguous = append(out.Ambiguous, row.label)
			}
		} else if len(aa) == 0 || len(bb) == 0 {
			for _, row := range append(aa, bb...) {
				out.Unmatched = append(out.Unmatched, row.label)
			}
		} else {
			out.Items = append(out.Items, ItemSpec{Sub: aa[0].source, Dub: bb[0].source})
		}
	}
	return out, nil
}
func (e *Engine) probeVideo(ctx context.Context, settings Settings, s SourceSpec) (Probe, Source, error) {
	off := -1
	original := s
	s.Audio = &off
	s.Subtitle = &off
	source, err := e.resolveVideo(ctx, ctx, settings, s, "sub")
	if err != nil {
		return Probe{}, source, err
	}
	source.Spec.Audio = original.Audio
	source.Spec.Subtitle = original.Subtitle
	source.Spec.Video = original.Video
	return *source.Probe, source, nil
}
