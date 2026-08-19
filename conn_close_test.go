package websocket

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestCloseStatusOK(t *testing.T) {
	c := &Conn{Conn: &FakeConn{b: closeFrame(false, StatusOK, "")}}

	n, err := readTimeout(t, c, make([]byte, 16))
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("wanted 0, EOF; got %v, %v", n, err)
	}
}

func TestCloseStatusOnly(t *testing.T) {
	c := &Conn{Conn: &FakeConn{b: closeFrame(false, StatusProtocol, "")}}

	n, err := readTimeout(t, c, make([]byte, 16))
	if n != 0 {
		t.Errorf("wanted 0 bytes, got %v", n)
	}

	var s Status

	if !errors.As(err, &s) || s != StatusProtocol {
		t.Errorf("wanted status %v, got %v (%T)", int(StatusProtocol), err, err)
	}
}

func TestCloseStatusText(t *testing.T) {
	c := &Conn{Conn: &FakeConn{b: closeFrame(false, StatusInternal, "bye")}}

	_, err := readTimeout(t, c, make([]byte, 16))

	var s *StatusText

	if !errors.As(err, &s) {
		t.Fatalf("wanted *StatusText, got %v (%T)", err, err)
	}

	if s.Status != StatusInternal || s.Text != "bye" {
		t.Errorf("wanted %v %q, got %v %q (%d bytes)", int(StatusInternal), "bye", int(s.Status), s.Text, len(s.Text))
	}
}

func TestCloseStatusTextMasked(t *testing.T) {
	c := &Conn{Conn: &FakeConn{b: closeFrame(true, StatusInternal, "bye")}}

	_, err := readTimeout(t, c, make([]byte, 16))

	var s *StatusText

	if !errors.As(err, &s) {
		t.Fatalf("wanted *StatusText, got %v (%T)", err, err)
	}

	if s.Status != StatusInternal || s.Text != "bye" {
		t.Errorf("wanted %v %q, got %v %q (%d bytes)", int(StatusInternal), "bye", int(s.Status), s.Text, len(s.Text))
	}
}

func TestCloseAfterData(t *testing.T) {
	b := frame(FrameBinary, false, []byte("hello"))
	b = append(b, closeFrame(false, StatusInternal, "bye")...)

	c := &Conn{Conn: &FakeConn{b: b}}

	p := make([]byte, 16)

	n, err := readTimeout(t, c, p)
	if err != nil {
		t.Fatalf("read data: %v", err)
	}

	if string(p[:n]) != "hello" {
		t.Errorf("wanted %q, got %q", "hello", p[:n])
	}

	_, err = readTimeout(t, c, p)

	var s *StatusText

	if !errors.As(err, &s) {
		t.Fatalf("wanted *StatusText, got %v (%T)", err, err)
	}

	if s.Status != StatusInternal || s.Text != "bye" {
		t.Errorf("wanted %v %q, got %v %q (%d bytes)", int(StatusInternal), "bye", int(s.Status), s.Text, len(s.Text))
	}
}

func TestCloseHeaderOnly(t *testing.T) {
	c := &Conn{Conn: &FakeConn{b: []byte{byte(FrameClose) | finbit, 5}}}

	_, err := readTimeout(t, c, make([]byte, 16))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("wanted %v, got %v (%T)", io.ErrUnexpectedEOF, err, err)
	}
}

func TestClosePartialPayload(t *testing.T) {
	c := &Conn{Conn: &FakeConn{b: []byte{byte(FrameClose) | finbit, 5, 0x03}}}

	_, err := readTimeout(t, c, make([]byte, 16))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("wanted %v, got %v (%T)", io.ErrUnexpectedEOF, err, err)
	}
}

func TestClosePipeNoExtraRead(t *testing.T) {
	cl, peer := net.Pipe()

	donec := make(chan struct{})

	var wg sync.WaitGroup

	defer wg.Wait()
	defer close(donec)
	defer peer.Close()
	defer cl.Close()

	wg.Add(1)

	go func() {
		defer wg.Done()

		_, err := peer.Write(closeFrame(false, StatusInternal, "bye"))
		if err != nil {
			t.Errorf("peer write: %v", err)
		}

		<-donec
	}()

	c := &Conn{Conn: cl}

	_, err := readTimeout(t, c, make([]byte, 16))

	var s *StatusText

	if !errors.As(err, &s) {
		t.Fatalf("wanted *StatusText, got %v (%T)", err, err)
	}

	if s.Status != StatusInternal || s.Text != "bye" {
		t.Errorf("wanted %v %q, got %v %q (%d bytes)", int(StatusInternal), "bye", int(s.Status), s.Text, len(s.Text))
	}
}

func TestClosePipeSegments(t *testing.T) {
	b := closeFrame(false, StatusInternal, "bye")

	c := pipeConn(t, 0, func(peer net.Conn) {
		for _, part := range [][]byte{b[:2], b[2:3], b[3:4], b[4:]} {
			_, err := peer.Write(part)
			if err != nil {
				t.Errorf("peer write: %v", err)
				return
			}
		}
	})

	_, err := readTimeout(t, c, make([]byte, 16))

	var s *StatusText

	if !errors.As(err, &s) {
		t.Fatalf("wanted *StatusText, got %v (%T)", err, err)
	}

	if s.Status != StatusInternal || s.Text != "bye" {
		t.Errorf("wanted %v %q, got %v %q", int(StatusInternal), "bye", int(s.Status), s.Text)
	}
}

func TestClosePipeSplitPayload(t *testing.T) {
	cl, peer := net.Pipe()

	donec := make(chan struct{})

	var wg sync.WaitGroup

	defer wg.Wait()
	defer close(donec)
	defer peer.Close()
	defer cl.Close()

	b := closeFrame(false, StatusInternal, "bye")

	wg.Add(1)

	go func() {
		defer wg.Done()

		_, err := peer.Write(b[:4])
		if err != nil {
			t.Errorf("peer write head: %v", err)
		}

		time.Sleep(50 * time.Millisecond)

		_, err = peer.Write(b[4:])
		if err != nil {
			t.Errorf("peer write tail: %v", err)
		}

		<-donec
	}()

	c := &Conn{Conn: cl}

	_, err := readTimeout(t, c, make([]byte, 16))

	var s *StatusText

	if !errors.As(err, &s) {
		t.Fatalf("wanted *StatusText, got %v (%T)", err, err)
	}

	if s.Status != StatusInternal || s.Text != "bye" {
		t.Errorf("wanted %v %q, got %v %q (%d bytes)", int(StatusInternal), "bye", int(s.Status), s.Text, len(s.Text))
	}
}

func readTimeout(tb testing.TB, c *Conn, p []byte) (int, error) {
	tb.Helper()

	return callTimeout(tb, func() (int, error) { return c.Read(p) })
}

func callTimeout(tb testing.TB, f func() (int, error)) (int, error) {
	tb.Helper()

	type result struct {
		n   int
		err error
	}

	resc := make(chan result, 1)

	go func() {
		n, err := f()
		resc <- result{n: n, err: err}
	}()

	select {
	case r := <-resc:
		return r.n, r.err
	case <-time.After(2 * time.Second):
		tb.Fatal("call blocked")
	}

	return 0, nil
}

func closeFrame(mask bool, s Status, text string) []byte {
	return frame(FrameClose, mask, append([]byte{byte(s >> 8), byte(s)}, text...))
}

func frame(op Opcode, mask bool, payload []byte) []byte {
	b := []byte{byte(op) | finbit, 0}

	switch {
	case len(payload) <= maxLen7:
		b[1] = byte(len(payload))
	case len(payload) <= maxLen16:
		b[1] = len16
		b = binary.BigEndian.AppendUint16(b, uint16(len(payload)))
	default:
		b[1] = len64
		b = binary.BigEndian.AppendUint64(b, uint64(len(payload)))
	}

	if !mask {
		return append(b, payload...)
	}

	key := [4]byte{0x37, 0xfa, 0x21, 0x3d}

	b[1] |= maskedbit
	b = append(b, key[:]...)

	p := append([]byte(nil), payload...)
	maskBuf(p, key, 0)

	return append(b, p...)
}
