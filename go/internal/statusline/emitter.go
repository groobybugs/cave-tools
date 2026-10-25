package statusline

import (
	"os"
	"sync"
	"time"
)

// EmitDebounce is the trailing-edge window that collapses a burst of MCP tool
// calls into one badge write.
const EmitDebounce = 300 * time.Millisecond

// EmitDisabled reports the same opt-out the statusline script honours: when
// savings are hidden, there is no point refreshing the badge file.
func EmitDisabled() bool {
	return os.Getenv("CAVE_TOOLS_STATUSLINE_SAVINGS") == "0"
}

// Emitter coalesces badge refreshes. Schedule is cheap (a timer reset) and the
// build/write work happens on the timer goroutine, never on the tool response
// path. A panic from a custom writer is swallowed: the badge is best-effort.
type Emitter struct {
	mu       sync.Mutex
	delay    time.Duration
	build    func() string
	write    func(string)
	disabled func() bool
	timer    *time.Timer
	dirty    bool
}

func newEmitter(delay time.Duration, build func() string, write func(string), disabled func() bool) *Emitter {
	if delay <= 0 {
		delay = EmitDebounce
	}
	return &Emitter{delay: delay, build: build, write: write, disabled: disabled}
}

// NewEmitter returns an emitter bound to the real aggregate/write functions.
func NewEmitter() *Emitter {
	return newEmitter(EmitDebounce, BuildSuffix, Write, EmitDisabled)
}

// Schedule marks the badge stale and (re)arms the trailing-edge timer.
func (e *Emitter) Schedule() {
	if e == nil || (e.disabled != nil && e.disabled()) {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.dirty = true
	if e.timer != nil {
		e.timer.Stop()
	}
	e.timer = time.AfterFunc(e.delay, e.fire)
}

// Flush writes immediately when a refresh is pending, cancelling the timer.
func (e *Emitter) Flush() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	e.emitLocked()
}

func (e *Emitter) fire() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.timer = nil
	e.emitLocked()
}

func (e *Emitter) emitLocked() {
	if !e.dirty {
		return
	}
	e.dirty = false
	defer func() { _ = recover() }()
	e.write(e.build())
}

var defaultEmitter = NewEmitter()

// Schedule arms the process-wide badge refresh (called after a tool call).
func Schedule() { defaultEmitter.Schedule() }

// Flush writes the process-wide pending badge now (server shutdown).
func Flush() { defaultEmitter.Flush() }
