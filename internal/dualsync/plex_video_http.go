package dualsync

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

func (s *Server) plexVideoHTTP(w http.ResponseWriter, r *http.Request) error {
	if r.URL.Path == "/api/media/preview" {
		if r.Method != "POST" {
			return methodError()
		}
	} else if r.Method != "GET" {
		return methodError()
	}
	s.Engine.mu.Lock()
	settings := s.Engine.settings
	s.Engine.mu.Unlock()
	if r.URL.Path != "/api/media/preview" && (settings.PlexServer == "" || settings.PlexToken == "") {
		return fail("INVALID_ARGUMENT", 400, "configure Plex server and token first")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	p := newPlexClient(settings)
	switch r.URL.Path {
	case "/api/plex/libraries":
		rows, err := p.libraries(ctx)
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"libraries": rows})
	case "/api/plex/videos":
		q := r.URL.Query()
		cursor, limit := 0, 100
		var err error
		if q.Get("cursor") != "" {
			cursor, err = strconv.Atoi(q.Get("cursor"))
			if err != nil {
				return fail("INVALID_ARGUMENT", 400, "invalid cursor")
			}
		}
		if q.Get("limit") != "" {
			limit, err = strconv.Atoi(q.Get("limit"))
			if err != nil {
				return fail("INVALID_ARGUMENT", 400, "invalid limit")
			}
		}
		if cursor < 0 || limit < 1 || limit > 200 {
			return fail("INVALID_ARGUMENT", 400, "cursor must be nonnegative; limit must be 1..200")
		}
		key := ""
		if id := q.Get("parent_id"); id != "" {
			if !numericID(id) {
				return fail("INVALID_ARGUMENT", 400, "invalid parent ID")
			}
			key = "/library/metadata/" + id + "/children"
		} else {
			id := q.Get("library_id")
			if !numericID(id) {
				return fail("INVALID_ARGUMENT", 400, "provide library_id or parent_id")
			}
			key = "/library/sections/" + id + "/all"
		}
		params := r.URL.Query()
		params.Del("parent_id")
		params.Del("library_id")
		params.Del("cursor")
		params.Del("limit")
		params.Del("q")
		if len(params) > 0 {
			return fail("INVALID_ARGUMENT", 400, "unknown video search parameter")
		}
		key += fmt.Sprintf("?X-Plex-Container-Start=%d&X-Plex-Container-Size=%d", cursor, limit+1)
		if q.Get("q") != "" {
			key += "&title=" + url.QueryEscape(q.Get("q"))
		}
		rows, err := p.videoRows(ctx, key)
		if err != nil {
			return err
		}
		next := -1
		if len(rows) > limit {
			next = cursor + limit
			rows = rows[:limit]
		}
		entries := []PlexVideoEntry{}
		for _, v := range rows {
			entries = append(entries, v.entry())
		}
		writeJSON(w, 200, map[string]any{"entries": entries, "next_cursor": next})
	case "/api/plex/video":
		video, err := p.video(ctx, r.URL.Query().Get("id"))
		if err != nil {
			return err
		}
		server, err := p.serverID(ctx)
		if err != nil {
			return err
		}
		writeJSON(w, 200, video.details(server))
	case "/api/plex/video/probe":
		ref := PlexSource{ID: r.URL.Query().Get("id"), MediaID: r.URL.Query().Get("media_id"), PartID: r.URL.Query().Get("part_id"), ServerID: r.URL.Query().Get("server_id")}
		spec := SourceSpec{Plex: &ref}
		if err := validateSource(spec); err != nil {
			return err
		}
		probe, source, err := s.Engine.probeVideo(ctx, settings, spec)
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"probe": probe, "source": source.Spec, "metadata": source.Metadata})
	case "/api/media/preview":
		var req VideoPreviewRequest
		if err := decodeJSON(w, r, &req); err != nil {
			return err
		}
		preview, err := s.Engine.videoPreview(ctx, p, req)
		if err != nil {
			return err
		}
		writeJSON(w, 200, preview)
	case "/api/plex/artwork":
		if expected := r.URL.Query().Get("server_id"); expected != "" {
			actual, err := p.serverID(ctx)
			if err != nil {
				return err
			}
			if actual != expected {
				return fail("PLEX_SERVER_CHANGED", 409, "artwork belongs to another Plex server")
			}
		}
		video, err := p.video(ctx, r.URL.Query().Get("id"))
		if err != nil {
			return err
		}
		key := video.Thumb
		if kind := r.URL.Query().Get("kind"); kind == "poster" {
			if video.Poster != "" {
				key = video.Poster
			}
		} else if kind != "" {
			return fail("INVALID_ARGUMENT", 400, "kind must be poster or omitted")
		}
		if key == "" {
			return fail("NOT_FOUND", 404, "Plex artwork unavailable")
		}
		path, release, err := p.download(ctx, s.Engine.Config.StateDir, key, 10<<20)
		if err != nil {
			return fail("PLEX_FAILED", 502, "Plex artwork unavailable")
		}
		defer release()
		data, err := os.ReadFile(path)
		if err != nil {
			return fail("PLEX_FAILED", 502, "Plex artwork unavailable")
		}
		mime := http.DetectContentType(data)
		if !strings.HasPrefix(mime, "image/") {
			return fail("PLEX_FAILED", 502, "invalid Plex artwork")
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "private, max-age=300")
		w.Write(data)
	}
	return nil
}
