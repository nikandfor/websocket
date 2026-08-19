package websocket

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestFrameSkipBuffered(t *testing.T) {
	b := frame(FrameBinary, false, []byte("abcdefghij"))
	b = append(b, frame(FrameBinary, false, []byte("xyz"))...)

	c := &Conn{Conn: &FakeConn{b: b}}

	p := make([]byte, 16)

	n, err := readFrameTimeout(t, c, p[:3])
	if err != nil || string(p[:n]) != "abc" {
		t.Fatalf("wanted %q, got %q, %v", "abc", p[:n], err)
	}

	n, err = readFrameTimeout(t, c, p)
	if err != nil {
		t.Fatalf("second frame: %v", err)
	}

	if string(p[:n]) != "xyz" {
		t.Errorf("wanted %q, got %q", "xyz", p[:n])
	}
}

func TestFrameSkipSplit(t *testing.T) {
	b := frame(FrameBinary, false, []byte("abcdefghij"))

	c := pipeConn(t, 0, func(peer net.Conn) {
		for _, part := range [][]byte{b[:2+5], b[2+5:], frame(FrameBinary, false, []byte("xyz"))} {
			_, err := peer.Write(part)
			if err != nil {
				t.Errorf("peer write: %v", err)
				return
			}
		}
	})

	p := make([]byte, 16)

	n, err := readFrameTimeout(t, c, p[:3])
	if err != nil || string(p[:n]) != "abc" {
		t.Fatalf("wanted %q, got %q, %v", "abc", p[:n], err)
	}

	n, err = readFrameTimeout(t, c, p)
	if err != nil {
		t.Fatalf("second frame: %v", err)
	}

	if string(p[:n]) != "xyz" {
		t.Errorf("wanted %q, got %q", "xyz", p[:n])
	}
}

func TestFrameSkipAfterDirectRead(t *testing.T) {
	big := make([]byte, 2*defaultReadBufSize)
	for i := range big {
		big[i] = byte(i)
	}

	b := frame(FrameBinary, false, big)
	b = append(b, frame(FrameBinary, false, []byte("xyz"))...)

	c := &Conn{Conn: &FakeConn{b: b}}

	p := make([]byte, len(big))

	n, err := readFrameTimeout(t, c, p)
	if err != nil {
		t.Fatalf("big frame: %v", err)
	}

	if !bytes.Equal(p[:n], big) {
		t.Fatalf("big frame is broken: got %d of %d bytes", n, len(big))
	}

	q := make([]byte, 16)

	n, err = readFrameTimeout(t, c, q)
	if err != nil {
		t.Fatalf("second frame: %v", err)
	}

	if string(q[:n]) != "xyz" {
		t.Errorf("wanted %q, got %q", "xyz", q[:n])
	}
}

func TestFrameSkipAfterPartialDirectRead(t *testing.T) {
	big := make([]byte, 2*defaultReadBufSize)
	for i := range big {
		big[i] = byte(i)
	}

	b := frame(FrameBinary, false, big)
	b = append(b, frame(FrameBinary, false, []byte("xyz"))...)

	c := &Conn{Conn: &FakeConn{b: b}}

	p := make([]byte, len(big)-1000)

	n, err := readFrameTimeout(t, c, p)
	if err != nil {
		t.Fatalf("big frame: %v", err)
	}

	if !bytes.Equal(p[:n], big[:n]) {
		t.Fatalf("big frame is broken: got %d of %d bytes", n, len(p))
	}

	q := make([]byte, 16)

	n, err = readFrameTimeout(t, c, q)
	if err != nil {
		t.Fatalf("second frame: %v", err)
	}

	if string(q[:n]) != "xyz" {
		t.Errorf("wanted %q, got %q", "xyz", q[:n])
	}
}

func TestControlFrameLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame []byte
	}{
		{name: "fragmented_ping", frame: []byte{byte(FramePing), 2, 'h', 'i'}},
		{name: "fragmented_close", frame: []byte{byte(FrameClose), 2, 0x03, 0xe8}},
		{name: "too_long_ping", frame: append([]byte{byte(FramePing) | finbit, len16, 0x00, 0xff}, make([]byte, 255)...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Conn{Conn: &FakeConn{b: tc.frame}}

			_, err := readTimeout(t, c, make([]byte, 16))
			if !errors.Is(err, ErrProtocol) {
				t.Errorf("wanted %v, got %v (%T)", ErrProtocol, err, err)
			}
		})
	}
}

func TestFrameRSVBits(t *testing.T) {
	for _, rsv := range []byte{0x40, 0x20, 0x10} {
		b := frame(FrameBinary, false, []byte("abc"))
		b[0] |= rsv

		c := &Conn{Conn: &FakeConn{b: b}}

		_, err := readTimeout(t, c, make([]byte, 16))
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("rsv %#02x: wanted %v, got %v (%T)", rsv, ErrUnsupported, err, err)
		}
	}
}

func TestFrameLen16Split(t *testing.T) {
	payload := make([]byte, 200)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}

	b := frame(FrameBinary, true, payload)

	c := pipeConn(t, 0, func(peer net.Conn) {
		for _, part := range [][]byte{b[:2], b[2:]} {
			_, err := peer.Write(part)
			if err != nil {
				t.Errorf("peer write: %v", err)
				return
			}
		}
	})

	p := make([]byte, 256)

	n, err := readTimeout(t, c, p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if !bytes.Equal(p[:n], payload) {
		t.Errorf("payload is broken: got %d of %d bytes", n, len(payload))
	}
}

func TestFrameLen64Overflow(t *testing.T) {
	b := []byte{byte(FrameBinary) | finbit, len64, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

	c := &Conn{Conn: &FakeConn{b: b}}

	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p)
			}
		}()

		_, err = c.Read(make([]byte, 16))

		return err
	}()

	if !errors.Is(err, ErrProtocol) {
		t.Errorf("wanted %v, got %v (%T)", ErrProtocol, err, err)
	}
}

func TestFrameReadContextCancel(t *testing.T) {
	b := frame(FrameBinary, false, []byte("abcdefghij"))

	c := pipeConn(t, 0, func(peer net.Conn) {
		_, err := peer.Write(b[:2+4])
		if err != nil {
			t.Errorf("peer write: %v", err)
		}
	})

	f, err := callFrameTimeout(t, c)
	if err != nil {
		t.Fatalf("next frame: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := make([]byte, 16)

	n, err := callTimeout(t, func() (int, error) { return f.ReadContext(ctx, p) })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("wanted %v, got %v (%T), %d bytes", context.Canceled, err, err, n)
	}
}

func TestNextRawFrame(t *testing.T) {
	b := frame(FramePing, false, []byte("ping!"))
	b = append(b, frame(FrameBinary, false, []byte("hello"))...)

	var wc WriteConn

	c := &Conn{Conn: &wc}
	c.rbuf = grow(c.rbuf, defaultReadBufSize)
	c.end = copy(c.rbuf, b)

	f, err := c.NextRawFrame(context.Background())
	if err != nil {
		t.Fatalf("next raw frame: %v", err)
	}

	if f.Opcode != FramePing || f.Length != 5 || !f.Final {
		t.Errorf("wanted ping 5 true, got %v %v %v", f.Opcode, f.Length, f.Final)
	}

	if len(wc.b) != 0 {
		t.Errorf("raw frame answered the ping: % x", wc.b)
	}

	p, err := f.ReadAppendTo(context.Background(), []byte("got:"))
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read append: %v", err)
	}

	if string(p) != "got:ping!" {
		t.Errorf("wanted %q, got %q", "got:ping!", p)
	}

	f, err = c.NextRawFrame(context.Background())
	if err != nil {
		t.Fatalf("second raw frame: %v", err)
	}

	if f.Opcode != FrameBinary || f.Length != 5 {
		t.Errorf("wanted binary 5, got %v %v", f.Opcode, f.Length)
	}
}

func TestFrameMore(t *testing.T) {
	b := frame(FrameBinary, false, []byte("abcdefghij"))

	c := &Conn{Conn: &FakeConn{b: b}}

	f, err := callFrameTimeout(t, c)
	if err != nil {
		t.Fatalf("next frame: %v", err)
	}

	if f.More() != 10 {
		t.Errorf("wanted 10 more, got %v", f.More())
	}

	p := make([]byte, 3)

	n, err := callTimeout(t, func() (int, error) { return f.Read(p) })
	if err != nil || n != 3 {
		t.Fatalf("read: %v/%v %v", n, 3, err)
	}

	if f.More() != 7 {
		t.Errorf("wanted 7 more, got %v", f.More())
	}
}

func TestFrameReadAppendToLimit(t *testing.T) {
	b := frame(FrameBinary, false, []byte("abcdefghij"))

	c := &Conn{Conn: &FakeConn{b: b}}

	f, err := callFrameTimeout(t, c)
	if err != nil {
		t.Fatalf("next frame: %v", err)
	}

	ctx := context.Background()

	p, err := f.ReadAppendToLimit(ctx, []byte("xy"), 2)
	if err != nil || string(p) != "xy" {
		t.Errorf("wanted %q, nil; got %q, %v", "xy", p, err)
	}

	if f.More() != 10 {
		t.Errorf("wanted 10 more, got %v", f.More())
	}

	p, err = f.ReadAppendToLimit(ctx, p, 6)
	if err != nil || string(p) != "xyabcd" {
		t.Errorf("wanted %q, nil; got %q, %v", "xyabcd", p, err)
	}

	p, err = f.ReadAppendToLimit(ctx, p, 0x100)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read rest: %v", err)
	}

	if string(p) != "xyabcdefghij" {
		t.Errorf("wanted %q, got %q", "xyabcdefghij", p)
	}

	err = func() (err error) {
		defer func() {
			err = fmt.Errorf("%v", recover())
		}()

		_, err = f.ReadAppendToLimit(ctx, []byte("xy"), 1)

		return err
	}()

	if err.Error() != "limit must be bigger than b" {
		t.Errorf("wanted panic on limit < len(b), got %v", err)
	}
}

func TestReadContextCancelBlocked(t *testing.T) {
	c := pipeConn(t, 0, func(peer net.Conn) {})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	p := make([]byte, 16)

	n, err := callTimeout(t, func() (int, error) { return c.ReadContext(ctx, p) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("wanted %v, got %v (%T), %d bytes", context.DeadlineExceeded, err, err, n)
	}
}

func readFrameTimeout(tb testing.TB, c *Conn, p []byte) (int, error) {
	tb.Helper()

	return callTimeout(tb, func() (int, error) {
		f, err := c.NextFrame(context.Background())
		if err != nil {
			return 0, err
		}

		n, err := f.ReadContext(context.Background(), p)
		if errors.Is(err, io.EOF) {
			err = nil
		}

		return n, err
	})
}

func callFrameTimeout(tb testing.TB, c *Conn) (Frame, error) {
	tb.Helper()

	var f Frame

	_, err := callTimeout(tb, func() (int, error) {
		var err error
		f, err = c.NextFrame(context.Background())

		return 0, err
	})

	return f, err
}

func pipeConn(tb testing.TB, client byte, peer func(c net.Conn)) *Conn {
	tb.Helper()

	cl, p := net.Pipe()

	donec := make(chan struct{})

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		peer(p)

		<-donec
	}()

	tb.Cleanup(func() {
		close(donec)
		p.Close()
		cl.Close()
		wg.Wait()
	})

	return &Conn{Conn: cl, client: client}
}

func TestFrameEmpty(t *testing.T) {
	b := frame(FrameBinary, false, nil)
	b = append(b, frame(FrameBinary, false, nil)...)
	b = append(b, frame(FrameBinary, false, []byte("hello"))...)
	b = append(b, closeFrame(false, StatusOK, "")...)

	c := &Conn{Conn: &FakeConn{b: b}}

	p := make([]byte, 16)

	n, err := readTimeout(t, c, p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if string(p[:n]) != "hello" {
		t.Errorf("wanted %q, got %q", "hello", p[:n])
	}

	n, err = readTimeout(t, c, p)
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("wanted 0, EOF; got %v, %v", n, err)
	}

	n, err = readTimeout(t, c, nil)
	if n != 0 || err != nil {
		t.Errorf("empty buffer: wanted 0, nil; got %v, %v", n, err)
	}
}

// a reader that stops making progress without reporting an error
func TestFrameNoProgress(t *testing.T) {
	for _, size := range []int{5, 2 * defaultReadBufSize} {
		b := frame(FrameBinary, false, make([]byte, size))

		c := &Conn{Conn: &stuckConn{b: b[:len(b)-size]}}

		_, err := readTimeout(t, c, make([]byte, size))
		if !errors.Is(err, ErrNoProgress) {
			t.Errorf("frame %d: wanted %v, got %v (%T)", size, ErrNoProgress, err, err)
		}
	}
}

type stuckConn struct {
	b []byte

	net.Conn
}

func (c *stuckConn) Read(p []byte) (int, error) {
	n := copy(p, c.b)
	c.b = c.b[n:]

	return n, nil
}

func (c *stuckConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *stuckConn) Close() error                { return nil }
