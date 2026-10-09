package dualsync

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type logTail struct {
	mu   sync.Mutex
	data []byte
}

func (l *logTail) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.data = append(l.data, p...)
	if len(l.data) > 3000 {
		l.data = l.data[len(l.data)-3000:]
	}
	return len(p), nil
}
func (l *logTail) String() string { l.mu.Lock(); defer l.mu.Unlock(); return Redact(string(l.data)) }

type RawReader struct {
	cmd    *exec.Cmd
	queue  chan []byte
	cancel context.CancelFunc
	done   chan struct{}
	log    logTail
	mu     sync.Mutex
	err    string
}

func NewRawReader(args []string, size int, pad bool) (*RawReader, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ffmpeg", append([]string{"-hide_banner", "-nostdin", "-loglevel", "warning"}, args...)...)
	r := &RawReader{cmd: cmd, queue: make(chan []byte, 12), cancel: cancel, done: make(chan struct{})}
	cmd.Stderr = &r.log
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		cancel()
		return nil, e
	}
	if e = cmd.Start(); e != nil {
		cancel()
		return nil, e
	}
	go func() {
		defer close(r.done)
		defer close(r.queue)
		for {
			data := make([]byte, size)
			n, e := io.ReadFull(pipe, data)
			if e != nil && (!pad || n == 0) {
				break
			}
			select {
			case r.queue <- data:
			case <-ctx.Done():
				_ = cmd.Wait()
				return
			}
			if e != nil {
				break
			}
		}
		e := cmd.Wait()
		if e != nil && ctx.Err() == nil {
			r.mu.Lock()
			r.err = r.log.String()
			if r.err == "" {
				r.err = e.Error()
			}
			r.mu.Unlock()
		}
	}()
	return r, nil
}
func (r *RawReader) Ready() bool {
	if r == nil {
		return true
	}
	if len(r.queue) > 0 {
		return true
	}
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}
func (r *RawReader) EOF() bool {
	if r == nil {
		return true
	}
	select {
	case <-r.done:
		return len(r.queue) == 0
	default:
		return false
	}
}
func (r *RawReader) Take() []byte {
	if r == nil {
		return nil
	}
	select {
	case b := <-r.queue:
		return b
	default:
		return nil
	}
}
func (r *RawReader) Error() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}
func (r *RawReader) Close() {
	if r != nil {
		r.cancel()
	}
}
func filterFilename(s string) string {
	s = strings.NewReplacer("\\", "\\\\", "'", "\\'", ":", "\\:").Replace(s)
	var out strings.Builder
	for _, c := range s {
		if strings.ContainsRune("\\'[],;", c) {
			out.WriteByte('\\')
		}
		out.WriteRune(c)
	}
	return out.String()
}
func videoReader(s Source, pos float64) (*RawReader, error) {
	args := []string{"-threads", "2", "-filter_threads", "2", "-filter_complex_threads", "2", "-ss", strconv.FormatFloat(pos, 'f', 6, 64), "-copyts", "-i", s.File}
	scale := fmt.Sprintf("setpts=PTS-STARTPTS,fps=24000/1001,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,format=yuv420p", Width, Height, Width, Height)
	if s.Subtitle < 0 {
		args = append(args, "-map", fmt.Sprintf("0:%d", s.Video), "-vf", scale)
	} else if s.SubtitleCodec == "hdmv_pgs_subtitle" || s.SubtitleCodec == "dvd_subtitle" || s.SubtitleCodec == "dvb_subtitle" {
		args = append(args, "-filter_complex", fmt.Sprintf("[0:%d][0:%d]overlay=eof_action=pass:repeatlast=1,%s[v]", s.Video, s.Subtitle, scale), "-map", "[v]")
	} else {
		file := s.File
		if s.SubtitleFile != "" {
			file = s.SubtitleFile
		}
		args = append(args, "-map", fmt.Sprintf("0:%d", s.Video), "-vf", fmt.Sprintf("subtitles=filename=%s:si=%d,%s", filterFilename(file), s.SubtitleOrdinal, scale))
	}
	args = append(args, "-an", "-sn", "-thread_queue_size", "2", "-f", "rawvideo", "pipe:1")
	return NewRawReader(args, FrameBytes, false)
}
func audioReader(path string, pos float64, index int) (*RawReader, error) {
	if index < 0 {
		return nil, nil
	}
	return NewRawReader([]string{"-threads", "1", "-ss", strconv.FormatFloat(pos, 'f', 6, 64), "-i", path, "-map", fmt.Sprintf("0:%d", index), "-vn", "-sn", "-af", "aresample=48000", "-ar", "48000", "-ac", "2", "-c:a", "pcm_s16le", "-thread_queue_size", "2", "-f", "s16le", "pipe:1"}, AudioBytes, true)
}

type Side struct {
	video, audio *RawReader
	last         []byte
}

func NewSide(s Source, pos float64) (*Side, error) {
	v, e := videoReader(s, pos)
	if e != nil {
		return nil, e
	}
	a, e := audioReader(s.File, pos, s.Audio)
	if e != nil {
		v.Close()
		return nil, e
	}
	return &Side{video: v, audio: a}, nil
}
func (s *Side) Close() {
	if s != nil {
		s.video.Close()
		s.audio.Close()
	}
}
func (s *Side) Ready() bool { return s != nil && s.video.Ready() && s.audio.Ready() }
func (s *Side) EOF() bool   { return s.video.EOF() && s.audio.EOF() }
func (s *Side) Error() string {
	if s.video.Error() != "" {
		return s.video.Error()
	}
	return s.audio.Error()
}
func (s *Side) Take() ([]byte, []byte) {
	if b := s.video.Take(); b != nil {
		s.last = b
	}
	return s.last, s.audio.Take()
}
func (s *Side) Frame() []byte {
	if s.last == nil {
		s.last = s.video.Take()
	}
	return s.last
}

type Encoder struct {
	Kind   string `json:"kind"`
	Device string `json:"device"`
}

func (e Encoder) Args() []string {
	switch e.Kind {
	case "nvenc":
		a := []string{"-c:v", "h264_nvenc", "-preset", "p4", "-tune", "ull", "-rc", "vbr"}
		if e.Device != "" {
			a = append(a, "-gpu", e.Device)
		}
		return a
	case "qsv":
		return []string{"-init_hw_device", "vaapi=va:" + e.Device, "-init_hw_device", "qsv=qs@va", "-filter_hw_device", "qs", "-vf", "format=nv12,hwupload=extra_hw_frames=64", "-c:v", "h264_qsv", "-preset", "veryfast"}
	case "vaapi":
		return []string{"-vaapi_device", e.Device, "-vf", "format=nv12,hwupload", "-c:v", "h264_vaapi"}
	default:
		return []string{"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency"}
	}
}

type OutputStatus struct {
	Ready        bool    `json:"ready"`
	PID          int     `json:"pid"`
	OutTime      float64 `json:"out_time"`
	Restarts     int     `json:"restarts"`
	Error        string  `json:"error"`
	QueuedFrames int     `json:"queued_frames"`
	Encoder      string  `json:"encoder"`
}
type Publisher struct {
	cmd          *exec.Cmd
	v, a         chan []byte
	done         chan struct{}
	cancel       context.CancelFunc
	mu           sync.Mutex
	ready        bool
	outTime      float64
	progress     time.Time
	started      time.Time
	log          logTail
	err          string
	Encoder      Encoder
	Restarts     int
	retryAt      time.Time
	video, audio *os.File
}

func NewPublisher(settings Settings, side string, enc Encoder) (*Publisher, error) {
	return startPublisher(settings.URL(side), enc, nil)
}
func startPublisher(destination string, enc Encoder, frames *int) (*Publisher, error) {
	vr, vw, e := os.Pipe()
	if e != nil {
		return nil, e
	}
	ar, aw, e := os.Pipe()
	if e != nil {
		vr.Close()
		vw.Close()
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Publisher{v: make(chan []byte, 8), a: make(chan []byte, 8), done: make(chan struct{}), cancel: cancel, Encoder: enc, progress: time.Now(), started: time.Now(), video: vw, audio: aw}
	// The raw frames are already BT.709 limited range (decoders keep the source matrix; the compositor
	// writes BT.709). Declaring that on the input stops FFmpeg from assuming BT.601 and inserting a
	// per-frame CPU colour conversion to match the BT.709 output tags.
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "info", "-nostats", "-stats_period", "0.5", "-progress", "pipe:1", "-thread_queue_size", "8", "-probesize", "32", "-analyzeduration", "0", "-f", "rawvideo", "-pixel_format", "yuv420p", "-video_size", "1920x1080", "-framerate", "24000/1001", "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv", "-i", "pipe:3", "-thread_queue_size", "8", "-probesize", "32", "-analyzeduration", "0", "-f", "s16le", "-ar", "48000", "-ac", "2", "-i", "pipe:4", "-map", "0:v:0", "-map", "1:a:0"}
	args = append(args, enc.Args()...)
	args = append(args, "-b:v", "8M", "-maxrate", "12M", "-bufsize", "16M", "-g", "48", "-bf", "0", "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv", "-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-ac", "2", "-rw_timeout", "10000000")
	if frames != nil {
		args = append(args, "-frames:v", strconv.Itoa(*frames), "-f", "matroska", "-y", destination)
	} else if strings.HasPrefix(destination, "rtsp") {
		args = append(args, "-rtsp_transport", "tcp", "-muxdelay", "0.1", "-f", "rtsp", destination)
	} else {
		args = append(args, "-flvflags", "no_duration_filesize", "-f", "flv", destination)
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.ExtraFiles = []*os.File{vr, ar}
	cmd.Stderr = &p.log
	pipe, e := cmd.StdoutPipe()
	if e == nil {
		e = cmd.Start()
	}
	vr.Close()
	ar.Close()
	if e != nil {
		cancel()
		vw.Close()
		aw.Close()
		return nil, e
	}
	p.cmd = cmd
	writer := func(q <-chan []byte, f *os.File) {
		defer f.Close()
		for {
			select {
			case b, ok := <-q:
				if !ok {
					return
				}
				for len(b) > 0 {
					n, e := f.Write(b)
					if e != nil {
						return
					}
					b = b[n:]
				}
			case <-ctx.Done():
				return
			}
		}
	}
	go writer(p.v, vw)
	go writer(p.a, aw)
	go func() {
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			s := sc.Text()
			if strings.HasPrefix(s, "out_time_us=") {
				us, e := strconv.ParseFloat(strings.TrimPrefix(s, "out_time_us="), 64)
				if e == nil {
					p.mu.Lock()
					p.outTime = us / 1e6
					p.ready = p.outTime > 0
					p.progress = time.Now()
					p.mu.Unlock()
				}
			}
		}
	}()
	go func() {
		e := cmd.Wait()
		p.mu.Lock()
		if e != nil && ctx.Err() == nil {
			p.err = p.log.String()
			if p.err == "" {
				p.err = e.Error()
			}
		}
		p.mu.Unlock()
		cancel()
		close(p.done)
	}()
	return p, nil
}
func (p *Publisher) Status() OutputStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	ready := p.ready
	select {
	case <-p.done:
		ready = false
	default:
	}
	return OutputStatus{ready, p.cmd.Process.Pid, p.outTime, p.Restarts, p.err, len(p.v), p.Encoder.Kind}
}
func (p *Publisher) CanAccept() bool {
	select {
	case <-p.done:
		return false
	default:
		return len(p.v) < cap(p.v) && len(p.a) < cap(p.a)
	}
}
func (p *Publisher) Enqueue(v, a []byte) {
	if !p.CanAccept() {
		return
	}
	if v == nil {
		v = blackFrame
	}
	if a == nil {
		a = silence
	}
	p.v <- v
	p.a <- a
}
func (p *Publisher) Failed(now time.Time) bool {
	select {
	case <-p.done:
		return true
	default:
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if now.Sub(p.progress) > 20*time.Second {
		p.err = "Publisher stopped accepting media; reconnecting."
		return true
	}
	return false
}
func (p *Publisher) Close()  { p.cancel(); p.video.Close(); p.audio.Close() }
func (p *Publisher) Finish() { close(p.v); close(p.a) }

var silence = make([]byte, AudioBytes)
var blackFrame = func() []byte {
	b := make([]byte, FrameBytes)
	for i := 0; i < Width*Height; i++ {
		b[i] = 16
	}
	for i := Width * Height; i < len(b); i++ {
		b[i] = 128
	}
	return b
}()

func decodeImage(ctx context.Context, path string, w, h int) ([]byte, error) {
	args := []string{"-hide_banner", "-v", "error", "-threads", "1", "-i", path, "-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,format=rgba", w, h, w, h), "-f", "rawvideo", "pipe:1"}
	b, e := exec.CommandContext(ctx, "ffmpeg", args...).Output()
	if e != nil || len(b) != w*h*4 {
		return nil, fmt.Errorf("artwork could not be decoded")
	}
	return bytes.Clone(b), nil
}
