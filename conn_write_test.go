package websocket

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
)

type WriteConn struct {
	b []byte

	net.Conn
}

func TestCloseWritesFrame(t *testing.T) {
	var wc WriteConn

	c := &Conn{Conn: &wc}

	err := c.Close()
	if err != nil {
		t.Fatalf("close: %v", err)
	}

	want := []byte{byte(FrameClose) | finbit, 0}

	if !bytes.Equal(wc.b, want) {
		t.Errorf("wanted % x, got % x", want, wc.b)
	}
}

func TestCloseWriterWritesStatus(t *testing.T) {
	var wc WriteConn

	c := &Conn{Conn: &wc}

	err := c.CloseWriter(StatusGoingAway)
	if err != nil {
		t.Fatalf("close writer: %v", err)
	}

	want := []byte{byte(FrameClose) | finbit, 2, byte(StatusGoingAway >> 8), byte(StatusGoingAway & 0xff)}

	if !bytes.Equal(wc.b, want) {
		t.Errorf("wanted % x, got % x", want, wc.b)
	}
}

func TestCloseWriterBodyWritesStatusAndBody(t *testing.T) {
	var wc WriteConn

	c := &Conn{Conn: &wc}

	err := c.CloseWriterBody(StatusInternal, []byte("bye"))
	if err != nil {
		t.Fatalf("close writer body: %v", err)
	}

	want := append([]byte{byte(FrameClose) | finbit, 5, byte(StatusInternal >> 8), byte(StatusInternal & 0xff)}, "bye"...)

	if !bytes.Equal(wc.b, want) {
		t.Errorf("wanted % x, got % x", want, wc.b)
	}
}

func TestCloseWriterTwice(t *testing.T) {
	var wc WriteConn

	c := &Conn{Conn: &wc}

	err := c.CloseWriter(StatusGoingAway)
	if err != nil {
		t.Fatalf("close writer: %v", err)
	}

	sent := len(wc.b)

	for _, err := range []error{c.Close(), c.CloseWriter(StatusOK), c.CloseWriterBody(StatusOK, []byte("bye"))} {
		if err != nil {
			t.Errorf("second close: %v", err)
		}
	}

	if len(wc.b) != sent {
		t.Errorf("wanted %d bytes on the wire, got %d: % x", sent, len(wc.b), wc.b)
	}

	_, err = c.Write([]byte("hello"))
	if !errors.Is(err, ErrClosed) {
		t.Errorf("wanted %v, got %v (%T)", ErrClosed, err, err)
	}
}

func TestCloseWriterClientMasked(t *testing.T) {
	var wc WriteConn

	c := &Conn{Conn: &wc, client: 1}

	err := c.CloseWriter(StatusGoingAway)
	if err != nil {
		t.Fatalf("close writer: %v", err)
	}

	if len(wc.b) != 2+4+2 {
		t.Fatalf("wanted %d bytes, got %d: % x", 2+4+2, len(wc.b), wc.b)
	}

	if wc.b[1]&maskedbit == 0 {
		t.Errorf("mask bit is not set: % x", wc.b)
	}

	if wc.b[1]&len7Mask != 2 {
		t.Errorf("wanted payload length 2, got %d: % x", wc.b[1]&len7Mask, wc.b)
	}

	key := [4]byte(wc.b[2:6])
	body := append([]byte(nil), wc.b[6:]...)

	maskBuf(body, key, 0)

	want := []byte{byte(StatusGoingAway >> 8), byte(StatusGoingAway & 0xff)}

	if !bytes.Equal(body, want) {
		t.Errorf("wanted body % x, got % x", want, body)
	}
}

func TestCloseWriterDefaultStatus(t *testing.T) {
	var wc WriteConn

	c := &Conn{Conn: &wc}

	err := c.CloseWriter(0)
	if err != nil {
		t.Fatalf("close writer: %v", err)
	}

	want := []byte{byte(FrameClose) | finbit, 2, byte(StatusOK >> 8), byte(StatusOK & 0xff)}

	if !bytes.Equal(wc.b, want) {
		t.Errorf("wanted % x, got % x", want, wc.b)
	}
}

func TestWriteFragments(t *testing.T) {
	var wc WriteConn

	c := &Conn{Conn: &wc}

	for _, part := range []struct {
		op    Opcode
		final bool
		data  string
	}{
		{op: FrameBinary, data: "hello"},
		{op: FrameContinue, data: " big "},
		{op: FrameContinue, final: true, data: "world"},
	} {
		n, err := c.WriteFrame([]byte(part.data), part.op, part.final)
		if err != nil || n != len(part.data) {
			t.Fatalf("write %q: %v/%v %v", part.data, n, len(part.data), err)
		}
	}

	want := []byte{
		byte(FrameBinary), 5, 'h', 'e', 'l', 'l', 'o',
		byte(FrameContinue), 5, ' ', 'b', 'i', 'g', ' ',
		byte(FrameContinue) | finbit, 5, 'w', 'o', 'r', 'l', 'd',
	}

	if !bytes.Equal(wc.b, want) {
		t.Fatalf("wanted % x, got % x", want, wc.b)
	}

	r := &Conn{Conn: &FakeConn{b: wc.b}}

	p := make([]byte, 32)
	n := 0

	for _, part := range []string{"hello", " big ", "world"} {
		m, err := readTimeout(t, r, p[n:])
		if err != nil {
			t.Fatalf("read %q: %v", part, err)
		}

		if string(p[n:n+m]) != part {
			t.Errorf("wanted %q, got %q", part, p[n:n+m])
		}

		n += m
	}

	if string(p[:n]) != "hello big world" {
		t.Errorf("wanted %q, got %q", "hello big world", p[:n])
	}
}

func TestWriteLengths(t *testing.T) {
	for _, tc := range []struct {
		l    int
		head []byte
	}{
		{l: 125, head: []byte{byte(FrameBinary) | finbit, 125}},
		{l: 126, head: []byte{byte(FrameBinary) | finbit, len16, 0, 126}},
		{l: maxLen16, head: []byte{byte(FrameBinary) | finbit, len16, 0xff, 0xff}},
		{l: maxLen16 + 1, head: []byte{byte(FrameBinary) | finbit, len64, 0, 0, 0, 0, 0, 1, 0, 0}},
	} {
		payload := make([]byte, tc.l)
		for i := range payload {
			payload[i] = byte(i)
		}

		var wc WriteConn

		c := &Conn{Conn: &wc}

		n, err := c.Write(payload)
		if err != nil || n != tc.l {
			t.Fatalf("len %d: write %v/%v %v", tc.l, n, tc.l, err)
		}

		if !bytes.Equal(wc.b[:len(tc.head)], tc.head) {
			t.Errorf("len %d: wanted header % x, got % x", tc.l, tc.head, wc.b[:len(tc.head)])
		}

		if len(wc.b) != len(tc.head)+tc.l {
			t.Errorf("len %d: wanted %d bytes on the wire, got %d", tc.l, len(tc.head)+tc.l, len(wc.b))
		}

		r := &Conn{Conn: &FakeConn{b: wc.b}}

		p := make([]byte, tc.l)

		n, err = readTimeout(t, r, p)
		if err != nil {
			t.Fatalf("len %d: read: %v", tc.l, err)
		}

		if !bytes.Equal(p[:n], payload) {
			t.Errorf("len %d: payload is broken: got %d bytes", tc.l, n)
		}
	}
}

func TestWriteClientMasked(t *testing.T) {
	var wc WriteConn

	c := &Conn{Conn: &wc, client: 1}

	_, err := c.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	want := []byte{byte(FrameBinary) | finbit, maskedbit | 5}

	if !bytes.Equal(wc.b[:2], want) {
		t.Fatalf("wanted header % x, got % x", want, wc.b[:2])
	}

	if len(wc.b) != 2+4+5 {
		t.Fatalf("wanted %d bytes, got %d: % x", 2+4+5, len(wc.b), wc.b)
	}

	key := [4]byte(wc.b[2:6])
	body := append([]byte(nil), wc.b[6:]...)

	if bytes.Equal(body, []byte("hello")) && key != ([4]byte{}) {
		t.Errorf("payload is not masked: % x", wc.b)
	}

	maskBuf(body, key, 0)

	if string(body) != "hello" {
		t.Errorf("wanted %q, got %q", "hello", body)
	}
}

func TestPingPongClient(t *testing.T) {
	pongc := make(chan []byte, 1)

	c := pipeConn(t, 1, func(peer net.Conn) {
		_, err := peer.Write(frame(FramePing, false, []byte("ping!")))
		if err != nil {
			t.Errorf("peer write ping: %v", err)
			return
		}

		b := make([]byte, 64)

		n, err := peer.Read(b)
		if err != nil {
			t.Errorf("peer read pong: %v", err)
			return
		}

		pongc <- b[:n]

		_, err = peer.Write(frame(FrameBinary, false, []byte("hello")))
		if err != nil {
			t.Errorf("peer write data: %v", err)
		}
	})

	p := make([]byte, 16)

	n, err := readTimeout(t, c, p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if string(p[:n]) != "hello" {
		t.Errorf("wanted %q, got %q", "hello", p[:n])
	}

	pong := <-pongc

	want := []byte{byte(FramePong) | finbit, maskedbit | 5}

	if len(pong) != 2+4+5 || !bytes.Equal(pong[:2], want) {
		t.Fatalf("wanted masked pong % x, got % x", want, pong)
	}

	key := [4]byte(pong[2:6])
	body := append([]byte(nil), pong[6:]...)

	maskBuf(body, key, 0)

	if string(body) != "ping!" {
		t.Errorf("wanted %q, got %q", "ping!", body)
	}
}

func TestPingPong(t *testing.T) {
	pongc := make(chan []byte, 1)

	c := pipeConn(t, 0, func(peer net.Conn) {
		_, err := peer.Write(frame(FramePing, true, []byte("ping!")))
		if err != nil {
			t.Errorf("peer write ping: %v", err)
			return
		}

		b := make([]byte, 64)

		n, err := peer.Read(b)
		if err != nil {
			t.Errorf("peer read pong: %v", err)
			return
		}

		pongc <- b[:n]

		_, err = peer.Write(frame(FrameBinary, true, []byte("hello")))
		if err != nil {
			t.Errorf("peer write data: %v", err)
		}
	})

	p := make([]byte, 16)

	n, err := readTimeout(t, c, p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if string(p[:n]) != "hello" {
		t.Errorf("wanted %q, got %q", "hello", p[:n])
	}

	want := append([]byte{byte(FramePong) | finbit, 5}, "ping!"...)

	if pong := <-pongc; !bytes.Equal(pong, want) {
		t.Errorf("wanted pong % x, got % x", want, pong)
	}
}

func TestPingPongSplitPayload(t *testing.T) {
	pongc := make(chan []byte, 1)

	b := frame(FramePing, true, []byte("ping!"))

	c := pipeConn(t, 0, func(peer net.Conn) {
		for _, part := range [][]byte{b[:2+4+2], b[2+4+2:]} {
			_, err := peer.Write(part)
			if err != nil {
				t.Errorf("peer write ping: %v", err)
				return
			}
		}

		q := make([]byte, 64)

		n, err := peer.Read(q)
		if err != nil {
			t.Errorf("peer read pong: %v", err)
			return
		}

		pongc <- q[:n]

		_, err = peer.Write(frame(FrameBinary, true, []byte("hello")))
		if err != nil {
			t.Errorf("peer write data: %v", err)
		}
	})

	p := make([]byte, 16)

	n, err := readTimeout(t, c, p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if string(p[:n]) != "hello" {
		t.Errorf("wanted %q, got %q", "hello", p[:n])
	}

	want := append([]byte{byte(FramePong) | finbit, 5}, "ping!"...)

	if pong := <-pongc; !bytes.Equal(pong, want) {
		t.Errorf("wanted pong % x, got % x", want, pong)
	}
}

func (c *WriteConn) Write(p []byte) (int, error) {
	c.b = append(c.b, p...)

	return len(p), nil
}

func (c *WriteConn) Close() error { return nil }

// peer sends a ping split across reads, then a close frame, then closes:
// the last read carries payload and io.EOF at once
func TestPingPongThenClose(t *testing.T) {
	ping := frame(FramePing, true, []byte("ping!"))
	body := append(ping[len(ping)-3:], closeFrame(true, StatusGoingAway, "")...)

	var wc WriteConn

	c := &Conn{Conn: &eofConn{chunks: [][]byte{ping[:len(ping)-3], body}, w: &wc}}

	_, err := readTimeout(t, c, make([]byte, 16))

	var s Status

	if !errors.As(err, &s) || s != StatusGoingAway {
		t.Errorf("wanted status %v, got %v (%T)", int(StatusGoingAway), err, err)
	}

	want := append([]byte{byte(FramePong) | finbit, 5}, "ping!"...)

	if !bytes.Equal(wc.b, want) {
		t.Errorf("wanted pong % x, got % x", want, wc.b)
	}
}

// returns one chunk per Read, the last one together with io.EOF
type eofConn struct {
	chunks [][]byte
	w      *WriteConn

	net.Conn
}

func (c *eofConn) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}

	n := copy(p, c.chunks[0])
	c.chunks = c.chunks[1:]

	if len(c.chunks) == 0 {
		return n, io.EOF
	}

	return n, nil
}

func (c *eofConn) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *eofConn) Close() error                { return nil }
