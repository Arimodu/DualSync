package dualsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	Engine   *Engine
	Spec     []byte
	frontend http.Handler
}

func NewServer(e *Engine, spec []byte) *Server {
	return &Server{e, spec, http.FileServer(http.Dir(e.Config.WebRoot))}
}
func writeJSON(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(data)
}
func writeError(w http.ResponseWriter, err error) {
	var e *APIError
	if errors.As(err, &e) {
		writeJSON(w, e.HTTP, map[string]string{"error": e.Message, "code": e.Code})
	} else {
		writeJSON(w, 500, map[string]string{"error": "Internal operation failed", "code": "INTERNAL_ERROR"})
	}
}
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		return fail("INVALID_CONTENT_TYPE", 415, "use application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fail("INVALID_JSON", 400, "invalid JSON request")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fail("INVALID_JSON", 400, "request must contain one JSON value")
	}
	return nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
		writeError(w, fail("PRIVATE_NETWORK_REQUIRED", 403, "private network access required"))
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				writeError(w, fail("CROSS_ORIGIN", 403, "use a same-origin API proxy"))
				return
			}
		}
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if s.Engine.Config.NoWebUI {
			writeError(w, fail("NOT_FOUND", 404, "frontend serving disabled"))
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		s.frontend.ServeHTTP(w, r)
		return
	}
	var handlerErr error
	switch r.URL.Path {
	case "/api/plex/libraries", "/api/plex/videos", "/api/plex/video", "/api/plex/video/probe", "/api/plex/artwork", "/api/media/preview":
		handlerErr = s.plexVideoHTTP(w, r)
		if handlerErr == nil {
			return
		}
	case "/api/plex/playlists", "/api/plex/playlist":
		if r.Method != "GET" {
			handlerErr = methodError()
			break
		}
		s.Engine.mu.Lock()
		settings := s.Engine.settings
		s.Engine.mu.Unlock()
		if settings.PlexServer == "" || settings.PlexToken == "" {
			handlerErr = fail("INVALID_ARGUMENT", 400, "configure Plex server and token first")
			break
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		p := newPlexClient(settings)
		if r.URL.Path == "/api/plex/playlists" {
			rows, err := p.playlists(ctx)
			if err != nil {
				handlerErr = fail("PLEX_FAILED", 502, "%s", err)
				break
			}
			writeJSON(w, 200, map[string]any{"playlists": rows})
			return
		}
		if _, err := strconv.ParseUint(r.URL.Query().Get("id"), 10, 64); err != nil {
			handlerErr = fail("INVALID_ARGUMENT", 400, "invalid Plex playlist ID")
			break
		}
		rows, err := p.tracks(ctx, r.URL.Query().Get("id"))
		if err != nil {
			handlerErr = fail("PLEX_FAILED", 502, "%s", err)
			break
		}
		writeJSON(w, 200, map[string]any{"tracks": rows})
		return
	case "/api/status":
		if r.Method != "GET" {
			handlerErr = methodError()
			break
		}
		writeJSON(w, 200, s.Engine.Snapshot())
		return
	case "/api/control":
		if r.Method != "POST" {
			handlerErr = methodError()
			break
		}
		var cmd struct {
			Action string          `json:"action"`
			Value  json.RawMessage `json:"value"`
		}
		if handlerErr = decodeJSON(w, r, &cmd); handlerErr == nil {
			handlerErr = s.Engine.Command(cmd.Action, cmd.Value)
		}
		if handlerErr == nil {
			writeJSON(w, 200, s.Engine.Snapshot())
			return
		}
	case "/api/settings":
		if r.Method != "POST" {
			handlerErr = methodError()
			break
		}
		var patch map[string]json.RawMessage
		if handlerErr = decodeJSON(w, r, &patch); handlerErr == nil {
			handlerErr = s.Engine.UpdateSettings(patch)
		}
		if handlerErr == nil {
			writeJSON(w, 200, s.Engine.Snapshot())
			return
		}
	case "/api/library":
		if r.Method != "GET" {
			handlerErr = methodError()
			break
		}
		cursor, limit := 0, 100
		if v := r.URL.Query().Get("cursor"); v != "" {
			var err error
			cursor, err = strconv.Atoi(v)
			if err != nil {
				handlerErr = fail("INVALID_ARGUMENT", 400, "invalid cursor")
				break
			}
		}
		if v := r.URL.Query().Get("limit"); v != "" {
			var err error
			limit, err = strconv.Atoi(v)
			if err != nil {
				handlerErr = fail("INVALID_ARGUMENT", 400, "invalid limit")
				break
			}
		}
		var listing Listing
		listing, handlerErr = s.Engine.Library.Browse(r.URL.Query().Get("path"), r.URL.Query().Get("q"), cursor, limit)
		if handlerErr == nil {
			writeJSON(w, 200, listing)
			return
		}
	case "/api/library/probe":
		if r.Method != "GET" {
			handlerErr = methodError()
			break
		}
		var probe Probe
		probe, handlerErr = s.Engine.Library.Probe(r.Context(), r.URL.Query().Get("path"))
		if handlerErr == nil {
			writeJSON(w, 200, probe)
			return
		}
	case "/api/library/playlist":
		if r.Method != "POST" {
			handlerErr = methodError()
			break
		}
		var req PlaylistRequest
		if handlerErr = decodeJSON(w, r, &req); handlerErr == nil {
			var preview PlaylistPreview
			preview, handlerErr = s.Engine.Library.Playlist(req)
			if handlerErr == nil {
				writeJSON(w, 200, preview)
				return
			}
		}
	case "/api/media/load":
		if r.Method != "POST" {
			handlerErr = methodError()
			break
		}
		var req LoadRequest
		if handlerErr = decodeJSON(w, r, &req); handlerErr == nil {
			handlerErr = s.Engine.Load(req, 0)
		}
		if handlerErr == nil {
			writeJSON(w, 202, s.Engine.Snapshot())
			return
		}
	case "/api/media/unload":
		if r.Method != "POST" {
			handlerErr = methodError()
			break
		}
		var req struct{}
		if handlerErr = decodeJSON(w, r, &req); handlerErr == nil {
			handlerErr = s.Engine.Command("unload", nil)
		}
		if handlerErr == nil {
			writeJSON(w, 200, s.Engine.Snapshot())
			return
		}
	case "/api/diagnostics":
		if r.Method == "GET" {
			writeJSON(w, 200, s.Engine.Snapshot().Diagnostics)
			return
		}
		if r.Method != "POST" {
			handlerErr = methodError()
			break
		}
		var req struct{}
		if handlerErr = decodeJSON(w, r, &req); handlerErr == nil {
			e := s.Engine
			if handlerErr = e.startDiagnostics(Diagnose); handlerErr != nil {
				break
			}
			writeJSON(w, 202, e.Snapshot().Diagnostics)
			return
		}
	case "/api/openapi.yaml":
		if r.Method != "GET" {
			handlerErr = methodError()
			break
		}
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(s.Spec)
		return
	default:
		if strings.HasPrefix(r.URL.Path, "/api/music/art/") && r.Method == "GET" {
			id := strings.TrimPrefix(r.URL.Path, "/api/music/art/")
			if artID.MatchString(id) {
				path := filepath.Join(s.Engine.Config.StateDir, "art", id+".png")
				if _, err := os.Stat(path); err == nil {
					w.Header().Set("Content-Type", "image/png")
					http.ServeFile(w, r, path)
					return
				}
			}
		}
		handlerErr = fail("NOT_FOUND", 404, "endpoint or asset not found")
	}
	writeError(w, handlerErr)
}
func methodError() error { return fail("METHOD_NOT_ALLOWED", 405, "method not allowed") }

var artID = regexp.MustCompile(`^[a-f0-9]{32}$`)
