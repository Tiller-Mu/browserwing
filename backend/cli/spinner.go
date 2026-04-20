package cli

import (
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

type spinner struct {
	msg    string
	frames []string
	stop   chan struct{}
	done   chan struct{}
	mu     sync.Mutex
}

func newSpinner(msg string) *spinner {
	return &spinner{
		msg:    msg,
		frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}

func (s *spinner) Start() {
	if !isTTY() {
		fmt.Fprintf(os.Stderr, "%s ...\n", s.msg)
		close(s.done)
		return
	}

	go func() {
		defer close(s.done)
		i := 0
		for {
			select {
			case <-s.stop:
				fmt.Fprintf(os.Stderr, "\r\033[K")
				return
			default:
				s.mu.Lock()
				fmt.Fprintf(os.Stderr, "\r  %s %s", s.frames[i%len(s.frames)], s.msg)
				s.mu.Unlock()
				i++
				time.Sleep(80 * time.Millisecond)
			}
		}
	}()
}

func (s *spinner) Stop(result string) {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
	if isTTY() && result != "" {
		fmt.Fprintf(os.Stderr, "  %s\n", result)
	}
}

func isTTY() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}
