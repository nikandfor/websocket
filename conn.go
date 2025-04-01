package websocket

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
)

type (
	Conn struct {
		net.Conn
		r *bufio.Reader

		client byte

		wmu  sync.Mutex
		wbuf []byte

		rmu  sync.Mutex
		rbuf []byte

		st, end int // unparsed data in rbuf
	}
)

func (c *Conn) Write(p []byte) (int, error) {
	defer c.wmu.Unlock()
	c.wmu.Lock()

	b := c.wbuf

	var l7 byte

	switch {
	case len(p) <= maxLen7:
		l7 = byte(len(p))
	case len(p) <= maxLen16:
		l7 = len16
	case len(p) <= maxLen64:
		l7 = len64
	default:
		panic(len(p))
	}

	l7 |= c.client * masked

	b = append(b, fin|FrameText, l7)

	switch l7 {
	case len16:
		b = binary.BigEndian.AppendUint16(b, uint16(len(p)))
	case len64:
		b = binary.BigEndian.AppendUint64(b, uint64(len(p)))
	}

	var key [4]byte

	if c.client != 0 {
		_, _ = rand.Read(key[:])
	}

	payload := len(b)

	b = append(b, p...)

	if c.client != 0 {
		for j := range len(p) {
			b[payload+j] ^= key[j&3]
		}
	}

	c.wbuf = b[:0]

	_, err := c.Conn.Write(b)
	if err != nil {
		return len(p), err
	}

	return len(p), nil
}

func (c *Conn) Read(p []byte) (n int, err error) {
	defer c.rmu.Unlock()
	c.rmu.Lock()

	var f FrameMeta

	for {
		i, end := f.Parse(c.rbuf[:c.end], c.st)
		if f.Length != 0 && f.Length > len(p) {
			return 0, io.ErrShortBuffer
		}

		log.Printf("frame: [%x %x %x]  [%x %x %x %x]", f.HeaderBits, f.Length, f.Key, c.st, i, end, c.end)

		if end < 0 || end > c.end {
			if c.st >= c.end/2 {
				m := copy(c.rbuf, c.rbuf[c.st:c.end])
				c.st, c.end = 0, m
			}

			more := max(0x400, f.Length)

			c.rbuf = grow(c.rbuf, c.end+more)

			m, err := c.read(c.rbuf[c.end:])
			c.end += m
			if err != nil {
				return 0, err
			}

			continue
		}

		switch op := f.Opcode(); op {
		case FrameText, FrameBinary:
		case FramePing, FramePong:
			if op == FramePing {
				c.pong(c.rbuf[c.st:end])
			}

			c.st = end

			continue
		case FrameClose:
			switch {
			case f.Length == 0:
				return 0, io.EOF
			case f.Length == 2:
				return 0, f.Status
			case f.Length > 2:
				return 0, fmt.Errorf("closed: %v: %q", f.Status, c.rbuf[i+2:end])
			default:
				return 0, fmt.Errorf("closed: %q", c.rbuf[i:end])
			}
		default:
			panic(f.Opcode())
		}

		n, _ = f.Read(p, c.rbuf, i)
		c.st = end

		return n, nil
	}
}

func (c *Conn) pong(b []byte) {
	defer c.wmu.Unlock()
	c.wmu.Lock()

	st := len(c.wbuf)
	c.wbuf = append(c.wbuf, b...)

	c.wbuf[st] = c.wbuf[st]&0xf0 | FramePong
}

func (c *Conn) CloseCodeMessage(code int, msg string) (err error) {
	if len(msg) > 123 {
		msg = msg[:123]
	}

	b := c.wbuf
	b = append(b, fin|FrameClose, c.client*masked, byte(2+len(msg)))
	b = binary.BigEndian.AppendUint16(b, uint16(code))
	b = append(b, msg...)

	c.wbuf = b[:0]

	return c.writeClose(b)
}

func (c *Conn) Close() error {
	b := c.wbuf
	b = append(b, fin|FrameClose, c.client*masked, 0)
	c.wbuf = b[:0]

	return c.writeClose(b)
}

func (c *Conn) writeClose(b []byte) (err error) {
	defer func() {
		e := c.Conn.Close()
		if err == nil && e != nil {
			err = fmt.Errorf("close conn: %w", e)
		}
	}()

	_, err = c.Conn.Write(b)
	if err != nil {
		return fmt.Errorf("write close frame: %w", err)
	}

	return nil
}

func (c *Conn) read(p []byte) (n int, err error) {
	if c.r != nil {
		end := min(len(p), c.r.Buffered())

		n, err = c.r.Read(p[:end])
		if c.r.Buffered() == 0 {
			c.r = nil
		}

		return n, err
	}

	return c.Conn.Read(p)
}
