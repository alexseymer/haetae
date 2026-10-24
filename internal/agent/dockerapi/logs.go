package dockerapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Log reading limits. They bound the memory one followed container can use.
const (
	// MaxLogLine is the longest line delivered; the rest of a longer line is
	// dropped and reported through LogLine.Truncated.
	MaxLogLine = 16 << 10
	// maxFrame is the largest multiplexed frame accepted. Docker writes
	// frames of at most a few tens of KiB; anything bigger is a protocol error.
	maxFrame = 1 << 20
)

// LogLine is one line from a container's stdout or stderr.
type LogLine struct {
	Stream    byte      // 1 stdout, 2 stderr
	Time      time.Time // zero if the daemon sent no timestamp
	Text      string    // without the trailing newline
	Truncated bool      // the line was longer than MaxLogLine
}

// Logs follows a container's logs from since until ctx ends or the stream
// fails, calling fn for each complete line. tty must be the container's
// Config.Tty: TTY containers send a raw stream, others a multiplexed one.
// It returns nil when ctx is cancelled or the container stops.
func (c *Client) Logs(ctx context.Context, id string, tty bool, since time.Time, fn func(LogLine)) error {
	q := url.Values{}
	q.Set("follow", "1")
	q.Set("stdout", "1")
	q.Set("stderr", "1")
	q.Set("timestamps", "1")
	q.Set("tail", "0")
	if !since.IsZero() {
		q.Set("since", strconv.FormatInt(since.Unix(), 10))
	}
	path := "/containers/" + url.PathEscape(id) + "/logs?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return fmt.Errorf("dockerapi: logs: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("dockerapi: logs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.statusError("logs", resp)
	}

	err = ReadLogStream(resp.Body, tty, fn)
	if err != nil && ctx.Err() != nil {
		return nil
	}
	return err
}

// ReadLogStream decodes a Docker log stream (multiplexed unless tty) into
// lines. It returns nil at a clean end of stream.
func ReadLogStream(r io.Reader, tty bool, fn func(LogLine)) error {
	br := bufio.NewReaderSize(r, 32<<10)
	lines := map[byte]*lineBuf{1: {}, 2: {}}

	if tty {
		return pump(br, lines[1], 1, fn)
	}

	var hdr [8]byte
	payload := make([]byte, 0, 32<<10)
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) {
				flushAll(lines, fn)
				return nil
			}
			return fmt.Errorf("dockerapi: log header: %w", err)
		}
		stream := hdr[0]
		size := binary.BigEndian.Uint32(hdr[4:])
		if size > maxFrame {
			return fmt.Errorf("dockerapi: log frame of %d bytes exceeds limit", size)
		}
		if cap(payload) < int(size) {
			payload = make([]byte, size)
		}
		payload = payload[:size]
		if _, err := io.ReadFull(br, payload); err != nil {
			return fmt.Errorf("dockerapi: log payload: %w", err)
		}
		lb, ok := lines[stream]
		if !ok {
			continue // stdin echo or an unknown stream: not a log line
		}
		lb.feed(payload, stream, fn)
	}
}

func pump(r io.Reader, lb *lineBuf, stream byte, fn func(LogLine)) error {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			lb.feed(buf[:n], stream, fn)
		}
		if err != nil {
			lb.flush(stream, fn)
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("dockerapi: log stream: %w", err)
		}
	}
}

func flushAll(lines map[byte]*lineBuf, fn func(LogLine)) {
	for stream, lb := range lines {
		lb.flush(stream, fn)
	}
}

// lineBuf assembles lines that span frames, with a hard size bound.
type lineBuf struct {
	buf       []byte
	truncated bool
}

func (l *lineBuf) feed(p []byte, stream byte, fn func(LogLine)) {
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		chunk := p
		if i >= 0 {
			chunk = p[:i]
		}
		l.append(chunk)
		if i < 0 {
			return
		}
		l.emit(stream, fn)
		p = p[i+1:]
	}
}

func (l *lineBuf) append(chunk []byte) {
	room := MaxLogLine - len(l.buf)
	if room <= 0 {
		if len(chunk) > 0 {
			l.truncated = true
		}
		return
	}
	if len(chunk) > room {
		chunk = chunk[:room]
		l.truncated = true
	}
	l.buf = append(l.buf, chunk...)
}

func (l *lineBuf) flush(stream byte, fn func(LogLine)) {
	if len(l.buf) > 0 {
		l.emit(stream, fn)
	}
}

func (l *lineBuf) emit(stream byte, fn func(LogLine)) {
	line := bytes.TrimSuffix(l.buf, []byte{'\r'})
	ts, text := splitTimestamp(line)
	fn(LogLine{Stream: stream, Time: ts, Text: text, Truncated: l.truncated})
	l.buf = l.buf[:0]
	l.truncated = false
}

// splitTimestamp separates the RFC 3339 timestamp Docker prepends when
// timestamps=1 is requested.
func splitTimestamp(line []byte) (time.Time, string) {
	sp := bytes.IndexByte(line, ' ')
	if sp <= 0 || sp > 40 {
		return time.Time{}, string(line)
	}
	ts, err := time.Parse(time.RFC3339Nano, string(line[:sp]))
	if err != nil {
		return time.Time{}, string(line)
	}
	return ts, string(line[sp+1:])
}
