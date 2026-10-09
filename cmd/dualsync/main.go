package main

import (
	"context"
	"dualsync/api"
	"dualsync/internal/dualsync"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const version = "0.4.0"

func main() {
	if err := run(); err != nil {
		log.Print(dualsync.Redact(err.Error()))
		os.Exit(1)
	}
}
func run() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	prefix := filepath.Dir(executable)
	if filepath.Base(prefix) == "bin" {
		prefix = filepath.Dir(prefix)
	}
	args := os.Args[1:]
	command := "serve"
	if len(args) > 0 && (args[0] == "doctor" || args[0] == "migrate" || args[0] == "configure") {
		command = args[0]
		args = args[1:]
	}
	configPath := ""
	for i, a := range args {
		if (a == "--config" || a == "-config") && i+1 < len(args) {
			configPath = args[i+1]
		}
		if strings.HasPrefix(a, "--config=") || strings.HasPrefix(a, "-config=") {
			configPath = strings.SplitN(a, "=", 2)[1]
		}
	}
	c, err := dualsync.LoadConfig(configPath, prefix)
	if err != nil {
		return err
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.StringVar(&configPath, "config", configPath, "configuration file")
	f.StringVar(&c.MediaRoot, "media-root", c.MediaRoot, "library root")
	f.StringVar(&c.Listen, "listen", c.Listen, "HTTP bind address")
	f.StringVar(&c.StateDir, "state-dir", c.StateDir, "private state directory")
	f.StringVar(&c.Encoder, "encoder", c.Encoder, "auto, nvenc, qsv, vaapi or cpu")
	f.StringVar(&c.Device, "device", c.Device, "encoder device/index")
	f.StringVar(&c.WebRoot, "webroot", c.WebRoot, "static webroot")
	f.StringVar(&c.Assets, "assets", c.Assets, "compositor asset directory")
	f.BoolVar(&c.NoWebUI, "no-webui", c.NoWebUI, "disable frontend, retain API")
	f.BoolVar(&c.AllowUnsupported, "allow-unsupported", c.AllowUnsupported, "permit publishing on an unqualified host")
	encodingTest := f.Bool("encoding-test", false, "perform real dual-stream encoder qualification")
	jsonOut := f.Bool("json", false, "print JSON report")
	output := f.String("output", "", "write diagnostic report")
	python := f.String("python-state", "", "legacy Python state directory")
	showVersion := f.Bool("version", false, "print version")
	unitOutput := f.String("unit-output", "", "write systemd unit")
	serviceUser := f.String("service-user", "dualsync", "systemd service account")
	printState := f.Bool("print-state-dir", false, "print configured state directory")
	if err = f.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if command == "doctor" {
		report := dualsync.Inventory()
		if *encodingTest {
			report = dualsync.Diagnose(ctx, c.Assets)
		}
		if *output != "" {
			if err = dualsync.SaveJSON(*output, report); err != nil {
				return err
			}
		}
		if *jsonOut {
			_ = json.NewEncoder(os.Stdout).Encode(report)
		} else {
			fmt.Printf("CPU budget: %d (%s)\n", report.CPU.Budget, report.CPU.Basis)
			for _, t := range report.Tests {
				fmt.Printf("%s %s available=%v tested=%v passed=%v fps=%.1f %s\n", t.Encoder.Kind, t.Encoder.Device, t.Available, t.Tested, t.Passed, t.FPS, t.Error)
			}
			fmt.Println(report.Warning)
		}
		return nil
	}
	if command == "migrate" {
		if *python == "" {
			return fmt.Errorf("--python-state required")
		}
		return migrate(c, *python)
	}
	if command == "configure" {
		if *printState {
			fmt.Println(c.StateDir)
			return nil
		}
		return configure(c, prefix, *output, *unitOutput, *serviceUser, configPath)
	}
	if c.MediaRoot == "" {
		return fmt.Errorf("configure --media-root or media_root")
	}
	engine, err := dualsync.NewEngine(c)
	if err != nil {
		return err
	}
	defer engine.Close()
	server := &http.Server{Addr: c.Listen, Handler: dualsync.NewServer(engine, api.Spec), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: 45 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("DualSync %s listening on %s; streams stopped", version, c.Listen)
	err = server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
func migrate(c dualsync.Config, old string) error {
	var legacy struct {
		SubServer   string  `json:"sub_server"`
		DubServer   string  `json:"dub_server"`
		StreamPath  string  `json:"stream_path"`
		SubUsername string  `json:"sub_username"`
		SubPassword string  `json:"sub_password"`
		DubUsername string  `json:"dub_username"`
		DubPassword string  `json:"dub_password"`
		MusicFolder string  `json:"music_folder"`
		MusicVolume float64 `json:"music_volume"`
		SubFolder   string  `json:"sub_folder"`
		DubFolder   string  `json:"dub_folder"`
	}
	if err := dualsync.ReadJSON(filepath.Join(old, "settings.json"), &legacy); err != nil {
		return err
	}
	library, err := dualsync.NewLibrary(c.MediaRoot)
	if err != nil {
		return err
	}
	defer library.Close()
	rel := func(path string) (string, error) {
		r, err := filepath.Rel(library.Root, path)
		if err != nil {
			return "", err
		}
		r = filepath.ToSlash(r)
		if _, err = library.Resolve(r); err != nil {
			return "", err
		}
		return r, nil
	}
	music, err := rel(legacy.MusicFolder)
	if err != nil {
		return err
	}
	sub, err := rel(legacy.SubFolder)
	if err != nil {
		return err
	}
	dub, err := rel(legacy.DubFolder)
	if err != nil {
		return err
	}
	settings := dualsync.Settings{SubServer: legacy.SubServer, DubServer: legacy.DubServer, StreamPath: legacy.StreamPath, SubUsername: legacy.SubUsername, SubPassword: legacy.SubPassword, DubUsername: legacy.DubUsername, DubPassword: legacy.DubPassword, MusicFolder: music, MusicVolume: legacy.MusicVolume}
	if err = settings.Validate(); err != nil {
		return err
	}
	preview, err := library.Playlist(dualsync.PlaylistRequest{SubFolder: sub, DubFolder: dub})
	if err != nil {
		return err
	}
	var pos struct {
		Episode  int     `json:"episode"`
		Position float64 `json:"position"`
	}
	_ = dualsync.ReadJSON(filepath.Join(old, "position.json"), &pos)
	index := max(0, min(pos.Episode-1, len(preview.Items)-1))
	load := dualsync.LoadRequest{Mode: "paired", Title: "Imported paired playlist", Items: preview.Items, StartIndex: index}
	if err = dualsync.SaveJSON(filepath.Join(c.StateDir, "settings.json"), settings); err != nil {
		return err
	}
	return dualsync.SaveJSON(filepath.Join(c.StateDir, "position.json"), map[string]any{"load": load, "index": index, "position": pos.Position})
}
