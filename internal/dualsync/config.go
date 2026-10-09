package dualsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const Width, Height = 1920, 1080
const FPS = 24000.0 / 1001
const Samples = 2002
const FrameBytes = Width * Height * 3 / 2
const AudioBytes = Samples * 4

type Settings struct {
	SubServer      string  `json:"sub_server"`
	DubServer      string  `json:"dub_server"`
	StreamPath     string  `json:"stream_path"`
	SubUsername    string  `json:"sub_username"`
	SubPassword    string  `json:"sub_password,omitempty"`
	DubUsername    string  `json:"dub_username"`
	DubPassword    string  `json:"dub_password,omitempty"`
	MusicFolder    string  `json:"music_folder"`
	MusicVolume    float64 `json:"music_volume"`
	MusicSource    string  `json:"music_source"`
	MusicShuffle   bool    `json:"music_shuffle"`
	PlexServer     string  `json:"plex_server"`
	PlexToken      string  `json:"plex_token,omitempty"`
	PlexPlaylistID string  `json:"plex_playlist_id"`
	HUD            bool    `json:"hud_enabled"`
}
type Config struct {
	MediaRoot        string   `json:"media_root"`
	Listen           string   `json:"listen"`
	StateDir         string   `json:"state_dir"`
	Encoder          string   `json:"encoder"`
	Device           string   `json:"device"`
	NoWebUI          bool     `json:"no_webui"`
	WebRoot          string   `json:"webroot,omitempty"`
	Assets           string   `json:"assets,omitempty"`
	AllowUnsupported bool     `json:"allow_unsupported"`
	Settings         Settings `json:"settings"`
}

func Defaults(prefix string) Config {
	return Config{Listen: "127.0.0.1:8090", StateDir: "/var/lib/dualsync", Encoder: "auto", WebRoot: filepath.Join(prefix, "webroot"), Assets: filepath.Join(prefix, "assets"), Settings: Settings{StreamPath: "live/dualsync", MusicVolume: 25, MusicShuffle: true}}
}
func LoadConfig(path string, prefix string) (Config, error) {
	c := Defaults(prefix)
	if path != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			return c, e
		}
		if e = json.Unmarshal(b, &c); e != nil {
			return c, e
		}
	}
	if c.WebRoot == "" {
		c.WebRoot = filepath.Join(prefix, "webroot")
	}
	if c.Assets == "" {
		c.Assets = filepath.Join(prefix, "assets")
	}
	return c, nil
}

var streamPathPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]*$`)

func (s Settings) Validate() error {
	if s.PlexPlaylistID != "" {
		if _, err := strconv.ParseUint(s.PlexPlaylistID, 10, 64); err != nil {
			return errors.New("invalid Plex playlist ID")
		}
	}
	if s.MusicSource != "" && s.MusicSource != "folder" && s.MusicSource != "plex" && s.MusicSource != "off" {
		return errors.New("invalid music source")
	}
	if s.PlexServer != "" {
		u, err := url.Parse(s.PlexServer)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return errors.New("plex_server must be an HTTP(S) base URL")
		}
	}
	if s.MusicSource == "plex" && (s.PlexServer == "" || s.PlexToken == "" || s.PlexPlaylistID == "") {
		return errors.New("Plex server, token and playlist are required")
	}
	for _, base := range []string{s.SubServer, s.DubServer} {
		if base == "" {
			continue
		}
		u, e := url.Parse(base)
		if e != nil {
			return errors.New("invalid server URL")
		}
		if u.Scheme != "rtmp" && u.Scheme != "rtmps" && u.Scheme != "rtsp" && u.Scheme != "rtsps" {
			return errors.New("server must use RTMP or RTSP")
		}
		if u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return errors.New("server must be a base URL without credentials, query or path")
		}
	}
	if !streamPathPattern.MatchString(s.StreamPath) {
		return errors.New("invalid stream path")
	}
	for _, p := range strings.Split(s.StreamPath, "/") {
		if p == "" || p == "." || p == ".." {
			return errors.New("invalid stream path component")
		}
	}
	if math.IsNaN(s.MusicVolume) || math.IsInf(s.MusicVolume, 0) || s.MusicVolume < 0 || s.MusicVolume > 100 {
		return errors.New("music volume must be between 0 and 100")
	}
	for _, v := range []string{s.SubUsername, s.SubPassword, s.DubUsername, s.DubPassword} {
		for _, r := range v {
			if r < 32 {
				return errors.New("credentials contain control characters")
			}
		}
	}
	return nil
}
func (s Settings) Public() map[string]any {
	b, _ := json.Marshal(s)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	delete(m, "sub_password")
	delete(m, "dub_password")
	delete(m, "plex_token")
	m["plex_token_set"] = s.PlexToken != ""
	m["sub_password_set"] = s.SubPassword != ""
	m["dub_password_set"] = s.DubPassword != ""
	return m
}
func (s Settings) URL(side string) string {
	base, user, pass := s.SubServer, s.SubUsername, s.SubPassword
	if side == "dub" {
		base, user, pass = s.DubServer, s.DubUsername, s.DubPassword
	}
	u, _ := url.Parse(base)
	u.Path = "/" + s.StreamPath
	q := url.Values{"dualsync": {"1"}}
	if user != "" {
		if strings.HasPrefix(u.Scheme, "rtmp") {
			q.Set("user", user)
			q.Set("pass", pass)
		} else {
			u.User = url.UserPassword(user, pass)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

var secretURL = regexp.MustCompile(`(?i)(rtsps?://|rtmps?://)[^\s/@]+@`)
var secretQuery = regexp.MustCompile(`(?i)([?&](?:pass|password|token)=)[^&\s]+`)

func Redact(s string) string {
	return secretQuery.ReplaceAllString(secretURL.ReplaceAllString(s, "${1}[credentials]@"), "${1}[redacted]")
}
func SaveJSON(path string, v any) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".save-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(append(b, '\n'))
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(name, path)
}
func ReadJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}

type APIError struct {
	Code    string
	Message string
	HTTP    int
}

func (e *APIError) Error() string { return e.Message }
func fail(code string, status int, format string, a ...any) error {
	return &APIError{code, fmt.Sprintf(format, a...), status}
}
