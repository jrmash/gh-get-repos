package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rivo/uniseg"
)

type progressRecorder struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	writes chan string
}

func (r *progressRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, err := r.buffer.Write(data)
	if r.writes != nil {
		select {
		case r.writes <- string(data):
		default:
		}
	}
	return n, err
}

func (r *progressRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buffer.String()
}

func awaitProgressWrite(t *testing.T, writes <-chan string) string {
	t.Helper()
	select {
	case value := <-writes:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("progress did not write")
		return ""
	}
}

func TestProgressImmediateFrameUpdatesAndCleanup(t *testing.T) {
	var out bytes.Buffer
	p := newProgress(&out, true, true, true, 80)
	p.interval = time.Hour
	if err := p.Start(context.Background(), "Cloning Octo/repo"); err != nil {
		t.Fatal(err)
	}
	want := "\r\x1b[2K\x1b[36m⠋\x1b[0m Cloning Octo/repo"
	if out.String() != want {
		t.Fatalf("first frame = %q", out.String())
	}
	p.Update("Fetching\nOcto/repo")
	want += "\r\x1b[2K\x1b[36m⠋\x1b[0m Fetching Octo/repo"
	if out.String() != want {
		t.Fatalf("phase update = %q", out.String())
	}
	p.Update("Fetching Octo/repo")
	if out.String() != want {
		t.Fatal("identical update repainted the frame")
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	want += "\r\x1b[2K"
	if out.String() != want {
		t.Fatalf("stop did not clear = %q", out.String())
	}
	p.Update("must not appear")
	if err := p.Stop(); err != nil || out.String() != want {
		t.Fatalf("stop was not idempotent: %v %q", err, out.String())
	}
}

func TestProgressPlainLinesAndReuse(t *testing.T) {
	var out bytes.Buffer
	p := newProgress(&out, true, false, true, 80)
	p.Update("before start")
	if err := p.Start(context.Background(), "Discovering repositories"); err != nil {
		t.Fatal(err)
	}
	p.Update("Discovering repositories")
	p.Update("Cloning Octo/repo")
	if err := p.Start(context.Background(), "Fetching Octo/other"); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	want := "… Discovering repositories\n… Cloning Octo/repo\n… Fetching Octo/other\n"
	if out.String() != want {
		t.Fatalf("plain progress = %q", out.String())
	}
}

func TestProgressDisabledAndCanceledStart(t *testing.T) {
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, animate := range []bool{false, true} {
		p := newProgress(&out, false, animate, true, 0)
		if err := p.Start(ctx, "disabled"); err != nil {
			t.Fatal(err)
		}
		p.Update("disabled update")
		if err := p.Stop(); err != nil || p.session != nil || out.Len() != 0 {
			t.Fatalf("disabled progress was active: %v %q", err, out.String())
		}
	}
	p := newProgress(&out, true, true, false, 80)
	if err := p.Start(ctx, "canceled"); !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("canceled start: %v %q", err, out.String())
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if p := newProgress(nil, true, false, false, -1); p.width != 80 || p.out != io.Discard {
		t.Fatalf("defaults = %+v", p)
	}
}

func TestProgressTicksAndCancellation(t *testing.T) {
	out := &progressRecorder{writes: make(chan string, 16)}
	p := newProgress(out, true, true, false, 80)
	p.interval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx, "Discovering"); err != nil {
		t.Fatal(err)
	}
	if got := awaitProgressWrite(t, out.writes); got != "\r\x1b[2K⠋ Discovering" {
		t.Fatal(got)
	}
	if got := awaitProgressWrite(t, out.writes); got != "\r\x1b[2K⠙ Discovering" {
		t.Fatal(got)
	}
	p.mu.Lock()
	done := p.session.done
	p.mu.Unlock()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not stop the animation")
	}
	before := out.String()
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != before+"\r\x1b[2K" {
		t.Fatalf("stop wrote another frame: %q", got)
	}
}

func TestProgressConcurrentUpdatesAndStops(t *testing.T) {
	// bytes.Buffer deliberately has no lock: the widget must serialize all of
	// its own writes, even when frames, phase changes, and Stop race.
	var out bytes.Buffer
	p := newProgress(&out, true, true, false, 80)
	p.interval = time.Millisecond
	if err := p.Start(context.Background(), "Starting"); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Go(func() {
			for j := 0; j < 100; j++ {
				p.Update("Fetching")
				p.Update("Updating")
			}
			if err := p.Stop(); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if !strings.HasSuffix(out.String(), "\r\x1b[2K") {
		t.Fatalf("terminal line was not cleared: %q", out.String())
	}
}

type progressWriterFunc func([]byte) (int, error)

func (f progressWriterFunc) Write(data []byte) (int, error) { return f(data) }

func TestProgressWriteErrors(t *testing.T) {
	failure := errors.New("output unavailable")
	for _, phase := range []string{"start", "update", "tick", "clear", "short write"} {
		t.Run(phase, func(t *testing.T) {
			calls := 0
			writes := make(chan string, 16)
			writer := progressWriterFunc(func(data []byte) (int, error) {
				calls++
				writes <- string(data)
				if phase == "short write" {
					return len(data) - 1, nil
				}
				if phase == "start" || calls == 2 {
					return 0, failure
				}
				return len(data), nil
			})
			p := newProgress(writer, true, true, false, 80)
			p.interval = time.Hour
			if phase == "tick" {
				p.interval = time.Millisecond
			}
			err := p.Start(context.Background(), "Starting")
			if phase == "start" || phase == "short write" {
				if err == nil {
					t.Fatal("first write error was ignored")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if phase == "update" {
				p.Update("Fetching")
				p.Update("ignored after failure")
			}
			if phase == "tick" {
				awaitProgressWrite(t, writes)
				awaitProgressWrite(t, writes)
			}
			want := failure
			if phase == "short write" {
				want = io.ErrShortWrite
			}
			if err := p.Stop(); !errors.Is(err, want) {
				t.Fatalf("stop = %v, want %v", err, want)
			}
			if err := p.Stop(); !errors.Is(err, want) {
				t.Fatalf("repeated stop = %v", err)
			}
			if err := p.Start(context.Background(), "Retry"); !errors.Is(err, want) {
				t.Fatalf("restart lost previous output failure: %v", err)
			}
		})
	}
}

func TestProgressTruncatesDisplayCells(t *testing.T) {
	for _, tc := range []struct {
		value string
		width int
		want  string
	}{
		{"normal", 10, "normal"},
		{"normal", 0, ""},
		{"normal", 1, "…"},
		{"normal", 5, "norm…"},
		{"日日本", 4, "日…"},
		{"e\u0301clair", 3, "e\u0301c…"},
		{"👩‍💻work", 3, "👩‍💻…"},
	} {
		if got := truncateProgress(tc.value, tc.width); got != tc.want || uniseg.StringWidth(got) > tc.width {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.value, tc.width, got, tc.want)
		}
	}
	var out bytes.Buffer
	p := newProgress(&out, true, false, false, 10)
	if err := p.Start(context.Background(), "Cloning 日日本"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "… Clonin…\n" {
		t.Fatal(got)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}
