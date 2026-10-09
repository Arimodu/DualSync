package dualsync

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

type subtitleJob struct {
	done chan struct{}
	path string
	err  error
	ctx  context.Context
}

type subtitleCache struct {
	root  string
	mu    sync.Mutex
	jobs  map[string]*subtitleJob
	slots chan struct{}
	pins  map[string]map[context.Context]bool
}

func newSubtitleCache(state string) *subtitleCache {
	return &subtitleCache{root: filepath.Join(state, "subtitles"), jobs: map[string]*subtitleJob{}, slots: make(chan struct{}, 2), pins: map[string]map[context.Context]bool{}}
}

func textSubtitle(s Source) bool {
	return s.Subtitle >= 0 && s.SubtitleCodec != "hdmv_pgs_subtitle" && s.SubtitleCodec != "dvd_subtitle" && s.SubtitleCodec != "dvb_subtitle"
}

func (c *subtitleCache) prepare(ctx context.Context, s Source) (string, error) {
	if s.Context != nil {
		ctx = s.Context
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	identity := s.CacheIdentity
	if identity == "" {
		info, err := os.Stat(s.File)
		if err != nil {
			return "", err
		}
		statID := ""
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			statID = fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
		}
		identity = fmt.Sprintf("%s:%s:%d:%d", s.File, statID, info.Size(), info.ModTime().UnixNano())
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte("v2:"+identity)))
	path := filepath.Join(c.root, key+".mks")
	c.hold(ctx, path)
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		_ = os.Chtimes(path, time.Now(), time.Now())
		return path, nil
	}
	c.mu.Lock()
	if job := c.jobs[key]; job != nil {
		c.mu.Unlock()
		select {
		case <-job.done:
			if job.ctx.Err() != nil && ctx.Err() == nil {
				return c.prepare(ctx, s)
			}
			return job.path, job.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	job := &subtitleJob{done: make(chan struct{}), ctx: ctx}
	c.jobs[key] = job
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.jobs, key); close(job.done); c.mu.Unlock() }()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		job.err = ctx.Err()
		return "", job.err
	}
	job.err = func() error {
		if err := os.MkdirAll(c.root, 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(c.root, key+"-*.tmp")
		if err != nil {
			return err
		}
		tmp := f.Name()
		_ = f.Close()
		defer os.Remove(tmp)
		timeout := 90 * time.Second
		if s.Spec.Transport == "plex" {
			timeout = 10 * time.Minute
		}
		work, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(work, "ffmpeg", "-hide_banner", "-nostdin", "-v", "error", "-y", "-copyts", "-i", s.File, "-map", "0:s", "-map", "0:t?", "-c", "copy", "-avoid_negative_ts", "disabled", "-fs", "268435456", "-f", "matroska", tmp)
		var tail logTail
		cmd.Stderr = &tail
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("subtitle cache: %w: %s", err, tail.String())
		}
		info, err := os.Stat(tmp)
		if err != nil {
			return err
		}
		if info.Size() >= 256<<20 {
			return fmt.Errorf("subtitle cache exceeds 256 MiB limit")
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.makeRoom(info.Size()) {
			return fmt.Errorf("subtitle cache full with selected media")
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
		return nil
	}()
	if job.err == nil {
		job.path = path
	}
	return job.path, job.err
}
func (c *subtitleCache) hold(ctx context.Context, path string) {
	c.mu.Lock()
	if c.pins[path] == nil {
		c.pins[path] = map[context.Context]bool{}
	}
	if c.pins[path][ctx] {
		c.mu.Unlock()
		return
	}
	c.pins[path][ctx] = true
	c.mu.Unlock()
	context.AfterFunc(ctx, func() {
		c.mu.Lock()
		delete(c.pins[path], ctx)
		if len(c.pins[path]) == 0 {
			delete(c.pins, path)
		}
		c.mu.Unlock()
	})
}
func (c *subtitleCache) makeRoom(incoming int64) bool {
	files, _ := os.ReadDir(c.root)
	type candidate struct {
		path     string
		size     int64
		modified time.Time
	}
	old := []candidate{}
	var total int64
	for _, file := range files {
		if filepath.Ext(file.Name()) != ".mks" {
			continue
		}
		info, err := file.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(c.root, file.Name())
		total += info.Size()
		if len(c.pins[path]) == 0 {
			old = append(old, candidate{path, info.Size(), info.ModTime()})
		}
	}
	sort.Slice(old, func(i, j int) bool { return old[i].modified.Before(old[j].modified) })
	for _, file := range old {
		if total+incoming <= 1<<30 {
			break
		}
		if os.Remove(file.path) == nil {
			total -= file.size
		}
	}
	return total+incoming <= 1<<30
}

func (c *subtitleCache) warm(ctx context.Context, s Source) {
	if textSubtitle(s) {
		go func() { _, _ = c.prepare(ctx, s) }()
	}
}
