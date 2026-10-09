package dualsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type CPUInfo struct {
	Virtual        bool   `json:"virtual"`
	Virtualization string `json:"virtualization"`
	PhysicalCores  int    `json:"physical_cores"`
	UsableVCPUs    int    `json:"usable_vcpus"`
	Budget         int    `json:"budget"`
	Eligible       bool   `json:"eligible"`
	Basis          string `json:"basis"`
}
type EncoderTest struct {
	Encoder   Encoder `json:"encoder"`
	Name      string  `json:"name"`
	Available bool    `json:"available"`
	Tested    bool    `json:"tested"`
	Passed    bool    `json:"passed"`
	FPS       float64 `json:"fps_per_output"`
	Error     string  `json:"error"`
}
type Report struct {
	Supported   bool          `json:"supported"`
	CPUFallback bool          `json:"cpu_fallback"`
	Selected    Encoder       `json:"selected"`
	CPU         CPUInfo       `json:"cpu"`
	Tests       []EncoderTest `json:"tests"`
	Missing     []string      `json:"missing"`
	FFmpeg      string        `json:"ffmpeg"`
	Fingerprint string        `json:"fingerprint"`
	Created     string        `json:"created"`
	Warning     string        `json:"warning"`
	Running     bool          `json:"running"`
}

func parseCPUList(s string) map[int]bool {
	out := map[int]bool{}
	for _, part := range strings.Split(strings.TrimSpace(s), ",") {
		ab := strings.Split(part, "-")
		a, e := strconv.Atoi(ab[0])
		if e != nil {
			continue
		}
		b := a
		if len(ab) == 2 {
			b, _ = strconv.Atoi(ab[1])
		}
		if b-a > 100000 {
			continue
		}
		for n := a; n <= b; n++ {
			out[n] = true
		}
	}
	return out
}
func cpuBudget(topology, allowed, virt string, quota float64) CPUInfo {
	allowedSet := parseCPUList(allowed)
	cores := map[string]bool{}
	logical := 0
	for _, line := range strings.Split(topology, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 3 {
			continue
		}
		id, e := strconv.Atoi(fields[0])
		if e != nil {
			continue
		}
		if len(allowedSet) > 0 && !allowedSet[id] {
			continue
		}
		if len(fields) > 3 && strings.EqualFold(fields[3], "N") {
			continue
		}
		logical++
		cores[fields[1]+":"+fields[2]] = true
	}
	usable := logical
	if quota > 0 {
		usable = min(usable, int(quota))
	}
	virtual := virt != "" && virt != "none"
	c := CPUInfo{Virtual: virtual, Virtualization: virt, PhysicalCores: len(cores), UsableVCPUs: usable, Basis: "physical cores"}
	c.Budget = min(len(cores), usable)
	if virtual {
		c.PhysicalCores = 0
		c.Budget = usable
		c.Basis = "allocated vCPUs; host physical cores unknown"
	}
	c.Eligible = c.Budget > 4
	return c
}
func detectCPU() CPUInfo {
	b, _ := exec.Command("lscpu", "-p=CPU,CORE,SOCKET,ONLINE").Output()
	allowed := ""
	if status, e := os.ReadFile("/proc/self/status"); e == nil {
		for _, s := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(s, "Cpus_allowed_list:") {
				allowed = strings.TrimSpace(strings.TrimPrefix(s, "Cpus_allowed_list:"))
			}
		}
	}
	virt, _ := exec.Command("systemd-detect-virt").Output()
	quota := 0.0
	cg, _ := os.ReadFile("/proc/self/cgroup")
	for _, line := range strings.Split(string(cg), "\n") {
		if strings.HasPrefix(line, "0::") {
			dir := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(line, "0::"))
			for strings.HasPrefix(dir, "/sys/fs/cgroup") {
				if raw, e := os.ReadFile(filepath.Join(dir, "cpu.max")); e == nil {
					f := strings.Fields(string(raw))
					if len(f) == 2 && f[0] != "max" {
						a, _ := strconv.ParseFloat(f[0], 64)
						p, _ := strconv.ParseFloat(f[1], 64)
						if p > 0 && (quota == 0 || a/p < quota) {
							quota = a / p
						}
					}
				}
				if dir == "/sys/fs/cgroup" {
					break
				}
				dir = filepath.Dir(dir)
			}
		}
	}
	return cpuBudget(string(b), allowed, strings.TrimSpace(string(virt)), quota)
}
func Inventory() Report {
	r := Report{CPU: detectCPU(), Tests: []EncoderTest{}, Missing: []string{}, Created: time.Now().UTC().Format(time.RFC3339)}
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, e := exec.LookPath(tool); e != nil {
			r.Missing = append(r.Missing, tool)
		}
	}
	if len(r.Missing) > 0 {
		r.Warning = "UNSUPPORTED: required media tools missing"
		return r
	}
	version, _ := exec.Command("ffmpeg", "-version").Output()
	r.FFmpeg = strings.Split(string(version), "\n")[0]
	encoders, _ := exec.Command("ffmpeg", "-hide_banner", "-encoders").CombinedOutput()
	filters, _ := exec.Command("ffmpeg", "-hide_banner", "-filters").CombinedOutput()
	for _, name := range []string{"subtitles", "overlay", "scale", "fps"} {
		if !strings.Contains(string(filters), " "+name+" ") {
			r.Missing = append(r.Missing, "FFmpeg filter "+name)
		}
	}
	if gpus, e := exec.Command("nvidia-smi", "--query-gpu=index,name", "--format=csv,noheader,nounits").Output(); e == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(gpus)), "\n") {
			f := strings.SplitN(line, ",", 2)
			if len(f) == 2 {
				r.Tests = append(r.Tests, EncoderTest{Encoder: Encoder{"nvenc", strings.TrimSpace(f[0])}, Name: strings.TrimSpace(f[1]), Available: strings.Contains(string(encoders), "h264_nvenc")})
			}
		}
	}
	devices, _ := filepath.Glob("/sys/class/drm/renderD*")
	for _, path := range devices {
		vendor, _ := os.ReadFile(filepath.Join(path, "device/vendor"))
		v := strings.TrimSpace(string(vendor))
		device := "/dev/dri/" + filepath.Base(path)
		if v == "0x8086" {
			r.Tests = append(r.Tests, EncoderTest{Encoder: Encoder{"qsv", device}, Name: "Intel " + filepath.Base(path), Available: strings.Contains(string(encoders), "h264_qsv")})
		}
		if v == "0x8086" || v == "0x1002" {
			name := "AMD"
			if v == "0x8086" {
				name = "Intel"
			}
			r.Tests = append(r.Tests, EncoderTest{Encoder: Encoder{"vaapi", device}, Name: name + " " + filepath.Base(path), Available: strings.Contains(string(encoders), "h264_vaapi")})
		}
	}
	r.Tests = append(r.Tests, EncoderTest{Encoder: Encoder{Kind: "cpu"}, Name: "CPU libx264", Available: strings.Contains(string(encoders), "libx264")})
	b, _ := json.Marshal(struct {
		CPU    CPUInfo
		FFmpeg string
		Tests  []EncoderTest
	}{r.CPU, r.FFmpeg, r.Tests})
	driver, _ := exec.Command("nvidia-smi", "--query-gpu=driver_version,pci.bus_id", "--format=csv,noheader").Output()
	b = append(b, driver...)
	kernel, _ := exec.Command("uname", "-r").Output()
	b = append(b, kernel...)
	for _, path := range devices {
		for _, name := range []string{"vendor", "device", "revision", "subsystem_device"} {
			data, _ := os.ReadFile(filepath.Join(path, "device", name))
			b = append(b, data...)
		}
		path, _ := filepath.EvalSymlinks(filepath.Join(path, "device/driver"))
		b = append(b, []byte(path)...)
	}
	sum := sha256.Sum256(b)
	r.Fingerprint = hex.EncodeToString(sum[:])
	r.Warning = "Encoding tests have not been run; host is not qualified"
	return r
}
func Diagnose(ctx context.Context, assets string) Report {
	r := Inventory()
	if len(r.Missing) > 0 {
		return r
	}
	for i := range r.Tests {
		t := &r.Tests[i]
		if !t.Available {
			t.Error = "FFmpeg encoder unavailable"
			continue
		}
		if t.Encoder.Kind == "cpu" && !r.CPU.Eligible {
			t.Error = "CPU fallback requires more than four usable cores/vCPUs"
			continue
		}
		t.Tested = true
		fps, err := testEncoder(ctx, t.Encoder, assets)
		t.FPS = fps
		if err != nil {
			t.Error = Redact(err.Error())
			if len(t.Error) > 2000 {
				t.Error = t.Error[:2000]
			}
			continue
		}
		t.Passed = fps >= FPS*1.25
		if !t.Passed {
			t.Error = "Concurrent throughput below required 1.25x headroom"
		}
		if t.Passed {
			if r.Selected.Kind == "" {
				r.Selected = t.Encoder
			}
			if t.Encoder.Kind == "cpu" {
				r.CPUFallback = true
			}
		}
	}
	r.Supported = r.Selected.Kind != ""
	r.Warning = ""
	if !r.Supported {
		r.Warning = "UNSUPPORTED: no encoder passed the dual-stream qualification test"
	}
	return r
}
func testEncoder(parent context.Context, enc Encoder, assets string) (float64, error) {
	ctx, cancel := context.WithTimeout(parent, 150*time.Second)
	defer cancel()
	dir, e := os.MkdirTemp("", "dualsync-encoding-*")
	if e != nil {
		return 0, e
	}
	defer os.RemoveAll(dir)
	frames := 720
	start := time.Now()
	pubs := []*Publisher{}
	for i := 0; i < 2; i++ {
		p, e := startPublisher(filepath.Join(dir, fmt.Sprintf("%d.mkv", i)), enc, &frames)
		if e != nil {
			for _, p := range pubs {
				p.Close()
			}
			return 0, e
		}
		pubs = append(pubs, p)
	}
	defer func() {
		for _, p := range pubs {
			p.Close()
		}
	}()
	source, e := NewRawReader([]string{"-threads", "2", "-filter_threads", "2", "-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=24000/1001", "-frames:v", strconv.Itoa(frames), "-pix_fmt", "yuv420p", "-f", "rawvideo", "pipe:1"}, FrameBytes, false)
	if e != nil {
		return 0, e
	}
	defer source.Close()
	renderer := NewRenderer(assets)
	for frame := 0; frame < frames; {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		failed := false
		can := true
		for _, p := range pubs {
			select {
			case <-p.done:
				failed = true
			default:
			}
			can = can && p.CanAccept()
		}
		if failed {
			return 0, fmt.Errorf("encoder exited before all frames: %s", pubs[0].log.String()+pubs[1].log.String())
		}
		if !can || !source.Ready() {
			time.Sleep(time.Millisecond)
			continue
		}
		video := source.Take()
		if video == nil {
			if source.EOF() {
				return 0, fmt.Errorf("synthetic source ended early")
			}
			continue
		}
		video = renderer.Render(Scene{Phase: "playing", Frame: video, HUD: true, Position: float64(frame) / FPS, Duration: float64(frames) / FPS})
		for _, p := range pubs {
			p.Enqueue(video, silence)
		}
		frame++
	}
	for _, p := range pubs {
		p.Finish()
	}
	for _, p := range pubs {
		select {
		case <-p.done:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		if p.Status().Error != "" {
			return 0, fmt.Errorf("encoding failed: %s", p.Status().Error)
		}
	}
	elapsed := time.Since(start).Seconds()
	fps := float64(frames) / elapsed
	for i := 0; i < 2; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%d.mkv", i))
		raw, e := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=nb_read_frames,width,height,codec_name", "-of", "json", path).Output()
		if e != nil {
			return fps, fmt.Errorf("encoded file unreadable")
		}
		var probe struct {
			Streams []struct {
				Frames string `json:"nb_read_frames"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
				Codec  string `json:"codec_name"`
			} `json:"streams"`
		}
		if json.Unmarshal(raw, &probe) != nil || len(probe.Streams) != 1 {
			return fps, fmt.Errorf("invalid encoded stream metadata")
		}
		s := probe.Streams[0]
		n, _ := strconv.Atoi(s.Frames)
		if n != frames || s.Width != Width || s.Height != Height || s.Codec != "h264" {
			return fps, fmt.Errorf("encoded frame count, codec or dimensions incorrect")
		}
		if output, e := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-i", path, "-map", "0:v:0", "-an", "-fps_mode", "passthrough", "-enc_time_base", "demux", "-f", "rawvideo", "-y", "/dev/null").CombinedOutput(); e != nil || len(output) > 0 {
			return fps, fmt.Errorf("decode verification failed: %s", Redact(string(output)))
		}
		audioCommand := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-i", path, "-map", "0:a:0", "-vn", "-f", "s16le", "pipe:1")
		var audioLog logTail
		audioCommand.Stderr = &audioLog
		audioSamples, err := audioCommand.Output()
		if err != nil || audioLog.String() != "" || len(audioSamples) < frames*AudioBytes*99/100 {
			return fps, fmt.Errorf("audio verification failed: insufficient PCM samples or decode errors: %s", audioLog.String())
		}
	}
	return fps, nil
}
