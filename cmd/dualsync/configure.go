package main

import (
	"dualsync/internal/dualsync"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func unitQuote(s string) (string, error) {
	if strings.ContainsAny(s, "\n\r\x00") {
		return "", fmt.Errorf("systemd path contains control characters")
	}
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "%", "%%")
	return "\"" + s + "\"", nil
}
func configure(c dualsync.Config, prefix, output, unit, user, config string) error {
	if !filepath.IsAbs(c.MediaRoot) || !filepath.IsAbs(c.StateDir) {
		return fmt.Errorf("media_root and state_dir must be absolute paths")
	}
	library, err := dualsync.NewLibrary(c.MediaRoot)
	if err != nil {
		return err
	}
	library.Close()
	entries, err := os.Open(c.MediaRoot)
	if err != nil {
		return err
	}
	_, err = entries.Readdirnames(1)
	entries.Close()
	if err != nil && err != io.EOF {
		return err
	}
	if _, err = os.ReadFile(filepath.Join(c.Assets, "fonts", "NotoSansJP.ttf")); err != nil {
		return fmt.Errorf("cannot read bundled compositor font: %w", err)
	}
	if !c.NoWebUI {
		web, err := os.Open(c.WebRoot)
		if err != nil {
			return fmt.Errorf("cannot access webroot: %w", err)
		}
		web.Close()
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen must be host:port: %w", err)
	}
	if !regexp.MustCompile(`^[a-z_][a-z0-9_-]*\$?$`).MatchString(user) {
		return fmt.Errorf("invalid service account")
	}
	if output != "" {
		persisted := c
		if persisted.WebRoot == filepath.Join(prefix, "webroot") {
			persisted.WebRoot = ""
		}
		if persisted.Assets == filepath.Join(prefix, "assets") {
			persisted.Assets = ""
		}
		if err := dualsync.SaveJSON(output, persisted); err != nil {
			return err
		}
		config = output
	}
	if unit == "" {
		return nil
	}
	if config == "" {
		return fmt.Errorf("unit requires --config or --output")
	}
	b, err := os.ReadFile(filepath.Join(prefix, "dualsync.service.in"))
	if err != nil {
		return err
	}
	replacements := map[string]string{"@BINARY@": filepath.Join(prefix, "bin", "dualsync"), "@CONFIG@": config, "@PREFIX@": prefix, "@MEDIA_ROOT@": c.MediaRoot, "@STATE_DIR@": c.StateDir}
	text := string(b)
	workdir := strings.ReplaceAll(prefix, "%", "%%")
	if strings.ContainsAny(workdir, "\n\r\x00") {
		return fmt.Errorf("invalid working directory")
	}
	text = strings.ReplaceAll(text, "@WORKDIR@", workdir)
	for marker, path := range replacements {
		q, err := unitQuote(path)
		if err != nil {
			return err
		}
		text = strings.ReplaceAll(text, marker, q)
	}
	text = strings.ReplaceAll(text, "@USER@", user)
	return os.WriteFile(unit, []byte(text), 0644)
}
