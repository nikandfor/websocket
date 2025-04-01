package websocket

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
)

type (
	Server struct{}

	Conn struct {
		net.Conn

		wmu  sync.Mutex
		wbuf []byte

		rmu  sync.Mutex
		rbuf []byte

		st, end int // unparsed data in rbuf
	}

	FlusherError interface {
		FlushError() error
	}

	Flusher interface {
		Flush()
	}

	flusher struct {
		Flusher
	}
)

const (
	FIN        = 0x80
	OpcodeMask = 0xf

	Masked   = 0x80
	Len7Mask = 0x7f

	Len16 = 128 - 2
	Len64 = 128 - 1

	MaxLen7  = 128 - 3
	MaxLen16 = 1<<16 - 1
	MaxLen64 = 1<<62 - 1

	// Non-control frames
	FrameContinue = 0
	FrameText     = 1
	FrameBinary   = 2

	// Control Frames
	FrameClose = 0x8
	FramePing  = 0x9
	FramePong  = 0xa
)

var (
	ErrNotWebsocket = errors.New("not websocket")
	ErrProtocol     = errors.New("protocol error")
	ErrNotHijacker  = errors.New("response is not hijacker")
	ErrExtraData    = errors.New("extra data in request")
)

func (s *Server) Handshake(ctx context.Context, w http.ResponseWriter, req *http.Request) (*Conn, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, ErrNotHijacker
	}

	var key []byte

	h := req.Header

	if v := h.Get("Connection"); v != "Upgrade" {
		return nil, ErrNotWebsocket
	}
	if v := h.Get("Upgrade"); v != "websocket" {
		return nil, ErrNotWebsocket
	}
	if v := h.Get("Sec-WebSocket-Version"); v != "13" {
		return nil, ErrNotWebsocket
	}

	if v := h.Get("Sec-WebSocket-Key"); v == "" {
		return nil, ErrProtocol
	} else if x, err := base64.URLEncoding.DecodeString(v); err != nil {
		return nil, fmt.Errorf("decode Sec-Key: %w", err)
	} else {
		key = x
	}

	key = append(key, "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"...)
	sum := sha1.Sum(key)
	accept := base64.URLEncoding.EncodeToString(sum[:])

	h = w.Header()

	h.Set("Connection", "Upgrade")
	h.Set("Upgrade", "websocket")
	h.Set("Sec-WebSocket-Accept", accept)

	w.WriteHeader(http.StatusSwitchingProtocols)

	c, buf, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack: %w", err)
	}

	if buf.Reader.Buffered() != 0 || buf.Writer.Buffered() != 0 {
		return nil, ErrExtraData
	}

	wc := &Conn{
		Conn: c,
	}

	return wc, nil
}

func (c *Conn) Write(p []byte) (int, error) {
	defer c.wmu.Unlock()
	c.wmu.Lock()

	b := c.wbuf

	var l7 byte

	switch {
	case len(p) <= MaxLen7:
		l7 = byte(len(p))
	case len(p) <= MaxLen16:
		l7 = Len16
	case len(p) <= MaxLen64:
		l7 = Len64
	default:
		panic(len(p))
	}

	b = append(b, FIN|FrameText, l7)

	switch l7 {
	case Len16:
		b = binary.BigEndian.AppendUint16(b, uint16(len(p)))
	case Len64:
		b = binary.BigEndian.AppendUint64(b, uint64(len(p)))
	}

	b = append(b, p...)

	c.wbuf = b[:0]

	n, err := c.Conn.Write(b)
	if err != nil {
		return n, err
	}

	return n, nil
}

func (c *Conn) Read(p []byte) (n int, err error) {
	defer c.rmu.Unlock()
	c.rmu.Lock()

	for {
		opcode, st, end := c.decodeFrame(c.rbuf[:c.end], c.st)
		if end < 0 {
			c.rbuf = grow(c.rbuf, c.end+32)

			m, err := c.Conn.Read(c.rbuf[c.end:])
			c.end += m
			if err != nil {
				return 0, err
			}

			if c.end == len(c.rbuf) && c.st >= len(c.rbuf)/2 {
				m = copy(c.rbuf, c.rbuf[c.st:c.end])
				c.st, c.end = 0, m
			}

			continue
		}

		switch opcode {
		case FrameText, FrameBinary:
		default:
			panic(opcode)
		}

		if end > st+len(p) {
			return 0, io.ErrShortBuffer
		}

		n = copy(p, c.rbuf[st:end])
		c.st = end
	}
}

func (c *Conn) decodeFrame(b []byte, st int) (opcode, i, end int) {
	i, end = st, -1

	if i+2 > len(b) {
		return
	}

	opcode = int(b[i] & OpcodeMask)

	switch opcode {
	case FrameText, FrameBinary:
	default:
		panic(b[i])
	}
	i++

	masked := b[i]&Masked != 0

	l := int(b[i] & Len7Mask)
	i++

	switch {
	case l <= MaxLen7:
		// l is fine already
	case l == Len16:
		if i+2 > len(b) {
			return
		}

		l = int(binary.BigEndian.Uint16(b[i:]))
		i += 2
	case l == Len64:
		if i+8 > len(b) {
			return
		}

		l = int(binary.BigEndian.Uint64(b[i:]))
		i += 8
	default:
		panic(l)
	}

	if masked && i+4 > len(b) {
		return
	}

	var key [4]byte

	if masked {
		i += copy(key[:], b[i:])
	}

	if i+l > len(b) {
		return
	}

	if masked {
		for j := range l {
			b[i+j] ^= key[j&3]
		}
	}

	return opcode, i, i + l
}

func (f flusher) FlushError() error { f.Flush(); return nil }

func grow(b []byte, n int) []byte {
	if n > cap(b) {
		b = append(b, make([]byte, n-cap(b))...)
	}

	return b[:cap(b)]
}
