package checkexec

import (
	"bytes"
	"fmt"
	"strings"
)

// logHeadBytes is how much of a check's output is kept from the start; the
// rest of MaxLogBytes holds the most recent output.
const logHeadBytes = 32 * 1024

type cappedBuffer struct {
	head        bytes.Buffer
	tail        []byte // ring holding the latest output once head is full
	next        int    // ring write position
	tailWritten int64  // bytes ever written past head
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := logHeadBytes - b.head.Len(); room > 0 {
		take := min(room, len(p))
		_, _ = b.head.Write(p[:take])
		p = p[take:]
	}
	if len(p) == 0 {
		return n, nil
	}
	if b.tail == nil {
		b.tail = make([]byte, MaxLogBytes-logHeadBytes)
	}
	b.tailWritten += int64(len(p))
	if len(p) > len(b.tail) {
		p = p[len(p)-len(b.tail):]
	}
	for len(p) > 0 {
		c := copy(b.tail[b.next:], p)
		b.next = (b.next + c) % len(b.tail)
		p = p[c:]
	}
	return n, nil
}

func (b *cappedBuffer) String() string {
	if b.tailWritten == 0 {
		return b.head.String()
	}
	var out strings.Builder
	out.WriteString(b.head.String())
	size := int64(len(b.tail))
	if b.tailWritten <= size {
		out.Write(b.tail[:b.tailWritten])
		return out.String()
	}
	fmt.Fprintf(&out, "\n[check log truncated: %d bytes omitted]\n", b.tailWritten-size)
	out.Write(b.tail[b.next:])
	out.Write(b.tail[:b.next])
	return out.String()
}
