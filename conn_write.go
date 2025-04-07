package websocket

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
)

func (c *Conn) Write(p []byte) (int, error) {
	return c.WriteFrame(p, FrameBinary, true)
}

func (c *Conn) WriteFrame(p []byte, op byte, final bool) (int, error) {
	defer c.wmu.Unlock()
	c.wmu.Lock()

	b := c.wbuf
	finb := csel[byte](final, finbit, 0)

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

	b = append(b, op&opcodeMask|finb, c.client*masked|l7)

	switch l7 {
	case len16:
		b = binary.BigEndian.AppendUint16(b, uint16(len(p)))
	case len64:
		b = binary.BigEndian.AppendUint64(b, uint64(len(p)))
	}

	payload := len(b)
	b = append(b, p...)

	if c.client != 0 {
		var key [4]byte
		_, _ = rand.Read(key[:])

		maskBuf(p[payload:], key)
	}

	c.wbuf = b[:0]

	n, err := c.Conn.Write(b)
	n -= payload
	if err != nil {
		if n < 0 {
			n = 0
		}

		return n, err
	}

	return n, nil
}

func (c *Conn) Close() (err error) {
	defer func() {
		e := c.Conn.Close()
		if err == nil && e != nil {
			err = e
		}
	}()

	defer c.wmu.Unlock()
	c.wmu.Lock()

	c.wbuf = append(c.wbuf, FrameClose|finbit, c.client*masked)

	_, err = c.Conn.Write(c.wbuf[:2])
	if err != nil {
		return fmt.Errorf("write close frame")
	}

	return nil
}

func (c *Conn) processPing() error {
	defer c.wmu.Unlock()
	c.wmu.Lock()

	c.rbuf[c.st] = c.rbuf[c.st]&0xf0 | FramePong

	_, err := c.Conn.Write(c.rbuf[c.st : c.i+c.more])

	return err
}

func (c *Conn) processClose() error {
	if c.more == 0 {
		return io.EOF
	}

	err := c.bufferFrame(c.more)
	if err != nil {
		return err
	}
	if c.more == 1 {
		return fmt.Errorf("wtf close data: %x", c.rbuf[c.i])
	}

	status := binary.BigEndian.Uint16(c.rbuf[c.i:])
	if c.more == 2 {
		return Status(status)
	}

	text := c.rbuf[c.i+2 : c.i+c.more]

	return &StatusText{
		Status: Status(status),
		Text:   string(text),
	}
}

func csel[T any](c bool, x, y T) T {
	if c {
		return x
	}

	return y
}
