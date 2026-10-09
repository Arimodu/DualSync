package dualsync

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type PlexPlaylist struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Smart bool   `json:"smart"`
	Count int    `json:"count"`
}
type PlexTrack struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	AlbumArtist string  `json:"album_artist"`
	Album       string  `json:"album"`
	Duration    float64 `json:"duration"`
	File        string  `json:"file"`
	Key         string  `json:"-"`
	Art         string  `json:"-"`
}
type plexRecord struct {
	RatingKey        string  `json:"ratingKey"`
	Title            string  `json:"title"`
	OriginalTitle    string  `json:"originalTitle"`
	GrandparentTitle string  `json:"grandparentTitle"`
	ParentTitle      string  `json:"parentTitle"`
	Thumb            string  `json:"thumb"`
	ParentThumb      string  `json:"parentThumb"`
	Duration         float64 `json:"duration"`
	Smart            bool    `json:"smart"`
	LeafCount        int     `json:"leafCount"`
	Media            []struct {
		Part []struct {
			File string `json:"file"`
			Key  string `json:"key"`
		} `json:"Part"`
	} `json:"Media"`
}
type plexContainer struct {
	MediaContainer struct {
		TotalSize int          `json:"totalSize"`
		Metadata  []plexRecord `json:"Metadata"`
	} `json:"MediaContainer"`
}
type plexClient struct {
	settings Settings
	client   *http.Client
}

func newPlexClient(s Settings) *plexClient {
	return &plexClient{s, &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (p *plexClient) get(ctx context.Context, key string) (*http.Response, error) {
	u, err := url.Parse(key)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(key, "//") || u.RawQuery != "" {
		return nil, fmt.Errorf("invalid Plex resource")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(p.settings.PlexServer, "/")+key, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid Plex server")
	}
	req.Header.Set("X-Plex-Token", p.settings.PlexToken)
	req.Header.Set("Accept", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Plex unavailable")
	}
	if res.StatusCode != 200 && res.StatusCode != 206 {
		res.Body.Close()
		return nil, fmt.Errorf("Plex returned HTTP %d", res.StatusCode)
	}
	return res, nil
}
func (p *plexClient) page(ctx context.Context, key string, start int) ([]plexRecord, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(p.settings.PlexServer, "/")+key, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid Plex server")
	}
	req.Header.Set("X-Plex-Token", p.settings.PlexToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Container-Start", strconv.Itoa(start))
	req.Header.Set("X-Plex-Container-Size", "200")
	res, err := p.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("Plex unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, 0, fmt.Errorf("Plex returned HTTP %d", res.StatusCode)
	}
	var data plexContainer
	if err = json.NewDecoder(io.LimitReader(res.Body, 20<<20)).Decode(&data); err != nil {
		return nil, 0, fmt.Errorf("invalid Plex response")
	}
	return data.MediaContainer.Metadata, data.MediaContainer.TotalSize, nil
}
func (p *plexClient) records(ctx context.Context, key string) ([]plexRecord, error) {
	all := []plexRecord{}
	for {
		rows, total, err := p.page(ctx, key, len(all))
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if len(all) > 10000 {
			return nil, fmt.Errorf("Plex collection exceeds 10000 items")
		}
		if len(rows) == 0 || (total > 0 && len(all) >= total) || (total == 0 && len(rows) < 200) {
			return all, nil
		}
	}
}
func (p *plexClient) playlists(ctx context.Context) ([]PlexPlaylist, error) {
	rows, err := p.records(ctx, "/playlists?playlistType=audio")
	if err != nil {
		return nil, err
	}
	out := []PlexPlaylist{}
	for _, r := range rows {
		out = append(out, PlexPlaylist{r.RatingKey, r.Title, r.Smart, r.LeafCount})
	}
	return out, nil
}
func (p *plexClient) tracks(ctx context.Context, id string) ([]PlexTrack, error) {
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return nil, fmt.Errorf("invalid Plex playlist ID")
	}
	rows, err := p.records(ctx, "/playlists/"+id+"/items")
	if err != nil {
		return nil, err
	}
	out := []PlexTrack{}
	for _, r := range rows {
		t := PlexTrack{ID: r.RatingKey, Title: r.Title, Artist: r.OriginalTitle, AlbumArtist: r.GrandparentTitle, Album: r.ParentTitle, Duration: r.Duration / 1000, Art: r.Thumb}
		if t.Art == "" {
			t.Art = r.ParentThumb
		}
		if t.Art != "" {
			if u, e := url.Parse(t.Art); e == nil {
				u.RawQuery = ""
				t.Art = u.String()
			}
		}
		if len(r.Media) > 0 && len(r.Media[0].Part) > 0 {
			t.File = r.Media[0].Part[0].File
			t.Key = r.Media[0].Part[0].Key
		}
		if t.Artist == "" {
			t.Artist = t.AlbumArtist
		}
		out = append(out, t)
	}
	return out, nil
}

var plexCachePins = struct {
	sync.Mutex
	paths map[string]int
}{paths: map[string]int{}}

func (p *plexClient) download(ctx context.Context, state, key string, limit int64) (string, func(), error) {
	root := filepath.Join(state, "plex-cache")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", nil, err
	}
	path := filepath.Join(root, fmt.Sprintf("%x", sha256.Sum256([]byte(p.settings.PlexServer+":"+key))))
	plexCachePins.Lock()
	plexCachePins.paths[path]++
	plexCachePins.Unlock()
	release := func() {
		plexCachePins.Lock()
		plexCachePins.paths[path]--
		if plexCachePins.paths[path] <= 0 {
			delete(plexCachePins.paths, path)
		}
		plexCachePins.Unlock()
	}
	if st, e := os.Stat(path); e == nil && st.Size() > 0 {
		_ = os.Chtimes(path, time.Now(), time.Now())
		return path, release, nil
	}
	res, err := p.get(ctx, key)
	if err != nil {
		release()
		return "", nil, err
	}
	defer res.Body.Close()
	f, err := os.CreateTemp(root, "download-*.tmp")
	if err != nil {
		release()
		return "", nil, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	n, err := io.Copy(f, io.LimitReader(res.Body, limit+1))
	f.Close()
	if err != nil || n > limit || n == 0 {
		release()
		return "", nil, fmt.Errorf("Plex download failed or exceeds size limit")
	}
	plexCachePins.Lock()
	defer plexCachePins.Unlock()
	files, _ := os.ReadDir(root)
	type old struct {
		path string
		size int64
		mod  time.Time
	}
	var olds []old
	var total int64
	for _, entry := range files {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		st, e := entry.Info()
		if e != nil {
			continue
		}
		q := filepath.Join(root, entry.Name())
		total += st.Size()
		if plexCachePins.paths[q] == 0 {
			olds = append(olds, old{q, st.Size(), st.ModTime()})
		}
	}
	sort.Slice(olds, func(i, j int) bool { return olds[i].mod.Before(olds[j].mod) })
	for _, x := range olds {
		if total+n <= 1<<30 {
			break
		}
		if os.Remove(x.path) == nil {
			total -= x.size
		}
	}
	if total+n > 1<<30 {
		plexCachePins.paths[path]--
		return "", nil, fmt.Errorf("Plex cache full with active tracks")
	}
	if err = os.Rename(tmp, path); err != nil {
		plexCachePins.paths[path]--
		return "", nil, err
	}
	return path, release, nil
}
