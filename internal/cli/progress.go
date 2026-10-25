package cli

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/rivo/uniseg"
)

var progressFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type progressSession struct {
	stop, done chan struct{}
	stopping   bool
}

// progress owns one transient terminal line. Nonterminal progress uses ordinary
// lines, and disabled progress never writes or starts a goroutine.
type progress struct {
	out                     io.Writer
	enabled, animate, color bool
	width                   int
	interval                time.Duration
	lifecycle               sync.Mutex
	mu                      sync.Mutex
	session                 *progressSession
	message                 string
	frame                   int
	err                     error
}

func newProgress(out io.Writer, enabled, animate, color bool, width int) *progress {
	if out == nil {
		out = io.Discard
	}
	if width <= 0 {
		width = 80
	}
	return &progress{out: out, enabled: enabled, animate: animate, color: color, width: width, interval: 80 * time.Millisecond}
}

// Start renders immediately so callers can start work only after its label is
// visible. Reusing the widget first finishes its previous operation.
func (p *progress) Start(ctx context.Context, message string) error {
	if !p.enabled {
		return nil
	}
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	if err := p.stop(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	s := &progressSession{stop: make(chan struct{}), done: make(chan struct{})}
	p.session, p.message, p.frame = s, oneLine(message), 0
	p.paint()
	err := p.err
	if !p.animate || err != nil {
		close(s.done)
	} else {
		go p.spin(ctx, s)
	}
	p.mu.Unlock()
	return err
}

// Update immediately displays a changed phase. Any output error is returned by
// Stop, allowing operation callbacks to remain simple notifications.
func (p *progress) Update(message string) {
	if !p.enabled {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	message = oneLine(message)
	if p.session == nil || p.session.stopping || p.err != nil || message == p.message {
		return
	}
	p.message = message
	p.paint()
}

// Stop joins the animation before clearing its line. It is safe to call more
// than once, including after cancellation or an output error.
func (p *progress) Stop() error {
	if !p.enabled {
		return nil
	}
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	return p.stop()
}

// stop is called with lifecycle held. Writers use mu, which is released while
// joining the animation so that a pending frame can finish without deadlocking.
func (p *progress) stop() error {
	p.mu.Lock()
	s := p.session
	if s == nil {
		err := p.err
		p.mu.Unlock()
		return err
	}
	s.stopping = true
	close(s.stop)
	p.mu.Unlock()
	<-s.done
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.animate {
		p.write("\r\x1b[2K")
	}
	p.session = nil
	return p.err
}

func (p *progress) spin(ctx context.Context, s *progressSession) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticker.C:
			p.mu.Lock()
			if s.stopping || ctx.Err() != nil || p.err != nil {
				p.mu.Unlock()
				return
			}
			p.frame = (p.frame + 1) % len(progressFrames)
			p.paint()
			failed := p.err != nil
			p.mu.Unlock()
			if failed {
				return
			}
		}
	}
}

// paint and write are called with mu held, including synchronous first frames
// and phase updates, so io.Writer need not support concurrent use.
func (p *progress) paint() {
	frame := "…"
	if p.animate {
		frame = progressFrames[p.frame]
	}
	line := truncateProgress(frame+" "+p.message, p.width-1)
	if p.animate {
		if p.color && strings.HasPrefix(line, frame) {
			line = "\x1b[36m" + frame + "\x1b[0m" + strings.TrimPrefix(line, frame)
		}
		p.write("\r\x1b[2K" + line)
		return
	}
	p.write(line + "\n")
}

func (p *progress) write(value string) {
	n, err := io.WriteString(p.out, value)
	if err == nil && n != len(value) {
		err = io.ErrShortWrite
	}
	if p.err == nil {
		p.err = err
	}
}

func truncateProgress(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if uniseg.StringWidth(value) <= width {
		return value
	}
	var result strings.Builder
	remaining := width - 1 // Reserve one display cell for the ellipsis.
	graphemes := uniseg.NewGraphemes(value)
	for graphemes.Next() {
		part := graphemes.Str()
		cells := uniseg.StringWidth(part)
		if cells > remaining {
			break
		}
		result.WriteString(part)
		remaining -= cells
	}
	return result.String() + "…"
}
