package dockerapi

import (
	"bytes"
	"encoding/binary"
	"runtime"
	"strings"
	"testing"
	"time"
)

func frame(stream byte, payload string) []byte {
	var b bytes.Buffer
	b.Write([]byte{stream, 0, 0, 0})
	_ = binary.Write(&b, binary.BigEndian, uint32(len(payload)))
	b.WriteString(payload)
	return b.Bytes()
}

func collect(t *testing.T, stream []byte, tty bool) []LogLine {
	t.Helper()
	var out []LogLine
	if err := ReadLogStream(bytes.NewReader(stream), tty, func(l LogLine) { out = append(out, l) }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReadLogStreamMultiplexed(t *testing.T) {
	var s []byte
	s = append(s, frame(1, "2026-10-24T20:00:00.123456789Z hello\n2026-10-24T20:00:01Z wor")...)
	s = append(s, frame(2, "2026-10-24T20:00:02Z oops\r\n")...)
	s = append(s, frame(1, "ld\n")...)              // completes the stdout line split across frames
	s = append(s, frame(0, "stdin echo\n")...)      // ignored
	s = append(s, frame(1, "no newline at end")...) // flushed at EOF

	got := collect(t, s, false)
	want := []struct {
		stream byte
		text   string
	}{{1, "hello"}, {2, "oops"}, {1, "world"}, {1, "no newline at end"}}
	if len(got) != len(want) {
		t.Fatalf("got %d lines: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].Stream != w.stream || got[i].Text != w.text {
			t.Errorf("line %d = {%d %q}, want {%d %q}", i, got[i].Stream, got[i].Text, w.stream, w.text)
		}
	}
	if !got[0].Time.Equal(time.Date(2026, 10, 24, 20, 0, 0, 123456789, time.UTC)) {
		t.Errorf("timestamp = %v", got[0].Time)
	}
	if !got[3].Time.IsZero() {
		t.Errorf("a line without timestamp must have a zero time, got %v", got[3].Time)
	}
}

func TestReadLogStreamTTY(t *testing.T) {
	got := collect(t, []byte("2026-10-24T20:00:00Z a\n2026-10-24T20:00:01Z b\r\npartial"), true)
	if len(got) != 3 || got[0].Text != "a" || got[1].Text != "b" || got[2].Text != "partial" {
		t.Fatalf("got %+v", got)
	}
}

func TestReadLogStreamBoundsLongLines(t *testing.T) {
	huge := strings.Repeat("x", 5*MaxLogLine)
	// One 5x line delivered in several frames, followed by a normal line.
	var s []byte
	for i := 0; i < 5; i++ {
		s = append(s, frame(1, huge[i*MaxLogLine:(i+1)*MaxLogLine])...)
	}
	s = append(s, frame(1, "\nnext\n")...)
	got := collect(t, s, false)
	if len(got) != 2 {
		t.Fatalf("got %d lines", len(got))
	}
	if len(got[0].Text) != MaxLogLine || !got[0].Truncated {
		t.Errorf("long line: len=%d truncated=%v, want %d and true", len(got[0].Text), got[0].Truncated, MaxLogLine)
	}
	if got[1].Text != "next" || got[1].Truncated {
		t.Errorf("line after a long one: %+v", got[1])
	}
}

// A frame header may promise far more than is sent. The reader must neither
// allocate that much nor loop forever: it streams in fixed chunks and fails
// when the data ends.
func TestReadLogStreamHugeFrameHeaderIsNotBuffered(t *testing.T) {
	hdr := []byte{1, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}
	s := append(append([]byte{}, hdr...), []byte("short")...)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := ReadLogStream(bytes.NewReader(s), false, func(LogLine) {})
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("expected an error: the 4 GiB frame never completes")
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 8<<20 {
		t.Fatalf("reader allocated %d bytes for a bogus 4 GiB frame", grown)
	}
}

func TestReadLogStreamLargeFrameIsStreamed(t *testing.T) {
	// One 3 MiB frame holding many short lines must be delivered line by line.
	var payload strings.Builder
	for i := 0; i < 100000; i++ {
		payload.WriteString("line number ")
		payload.WriteString(strings.Repeat("x", 20))
		payload.WriteByte('\n')
	}
	got := collect(t, frame(1, payload.String()), false)
	if len(got) != 100000 || got[0].Text != "line number "+strings.Repeat("x", 20) {
		t.Fatalf("got %d lines", len(got))
	}
}

func TestReadLogStreamTruncatedInput(t *testing.T) {
	s := frame(1, "complete line\n")
	s = append(s, 1, 0, 0, 0, 0, 0, 0, 50) // header promises 50 bytes, none follow
	err := ReadLogStream(bytes.NewReader(s), false, func(LogLine) {})
	if err == nil {
		t.Fatal("expected an error for a stream cut mid-frame")
	}
}

func FuzzReadLogStream(f *testing.F) {
	f.Add(frame(1, "a\nb"), false)
	f.Add([]byte("x\ny"), true)
	f.Fuzz(func(t *testing.T, data []byte, tty bool) {
		_ = ReadLogStream(bytes.NewReader(data), tty, func(l LogLine) {
			if len(l.Text) > MaxLogLine {
				t.Fatalf("line of %d bytes exceeds MaxLogLine", len(l.Text))
			}
		})
	})
}
