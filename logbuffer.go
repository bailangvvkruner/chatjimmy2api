package main

import (
	"strings"
	"sync"
)

const maxLogLines = 500

// logBuffer is a ring buffer that captures log output in memory.
// It implements io.Writer so it can be used as a log.SetOutput target.
type logBuffer struct {
	mu    sync.Mutex
	lines []string
	pos   int // next write position
	full  bool
}

var debugLog = &logBuffer{lines: make([]string, maxLogLines)}

func (b *logBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	line := strings.TrimRight(string(p), "\n\r")
	for _, l := range strings.Split(line, "\n") {
		b.lines[b.pos] = l
		b.pos++
		if b.pos >= maxLogLines {
			b.pos = 0
			b.full = true
		}
	}
	return len(p), nil
}

// Lines returns captured log lines in chronological order.
func (b *logBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.full {
		out := make([]string, b.pos)
		copy(out, b.lines[:b.pos])
		return out
	}

	out := make([]string, maxLogLines)
	n := copy(out, b.lines[b.pos:])
	copy(out[n:], b.lines[:b.pos])
	return out
}
