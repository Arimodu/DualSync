package main

import (
	"dualsync/internal/dualsync"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdEscapingAndConfig(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "install with spaces % and quotes\"")
	os.MkdirAll(prefix, 0755)
	template := []byte("ExecStart=@BINARY@ --config @CONFIG@\nUser=@USER@\nRequiresMountsFor=@PREFIX@ @MEDIA_ROOT@\nReadWritePaths=@STATE_DIR@\n")
	os.WriteFile(filepath.Join(prefix, "dualsync.service.in"), template, 0600)
	os.MkdirAll(filepath.Join(prefix, "assets/fonts"), 0755)
	os.WriteFile(filepath.Join(prefix, "assets/fonts/NotoSansJP.ttf"), []byte("test"), 0600)
	os.Mkdir(filepath.Join(prefix, "webroot"), 0755)
	c := dualsync.Defaults(prefix)
	c.MediaRoot = t.TempDir()
	c.StateDir = t.TempDir()
	config := filepath.Join(t.TempDir(), "config.json")
	unit := filepath.Join(t.TempDir(), "dualsync.service")
	if err := configure(c, prefix, config, unit, "dualsync", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(unit)
	text := string(b)
	if !strings.Contains(text, "%%") || !strings.Contains(text, `\"`) || strings.Contains(text, "@BINARY@") {
		t.Fatal(text)
	}
	info, _ := os.Stat(config)
	if info.Mode().Perm() != 0600 {
		t.Fatal("config permissions")
	}
	if _, err := unitQuote("bad\npath"); err == nil {
		t.Fatal("newline allowed")
	}
	if err := configure(c, prefix, "", unit, "unsafe account", config); err == nil {
		t.Fatal("unsafe account allowed")
	}
	loaded, err := dualsync.LoadConfig(config, prefix)
	if err != nil || loaded.MediaRoot != c.MediaRoot {
		t.Fatal("config roundtrip")
	}
}
