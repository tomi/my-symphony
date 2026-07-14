package claude

import (
	"bufio"
	"io"
	"sync"
)

// boundedTail keeps the last `limit` bytes read from a stream, used only for
// failure diagnostics (SPEC §10.3). It is safe for concurrent read/access.
type boundedTail struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *boundedTail) readFrom(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 8*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		t.mu.Lock()
		t.buf = append(t.buf, line...)
		t.buf = append(t.buf, '\n')
		if len(t.buf) > t.limit {
			t.buf = t.buf[len(t.buf)-t.limit:]
		}
		t.mu.Unlock()
	}
}

func (t *boundedTail) string() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
