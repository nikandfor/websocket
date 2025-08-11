package websocket

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
)

func (c *Conn) Write(p []byte) (int, error) {
	return c.WriteFrame(p, FrameBinary, true)
}

func (c *Conn) WriteFrame(p []byte, op Opcode, final bool) (int, error) {
	defer c.wmu.Unlock()
	c.wmu.Lock()

	return c.writeFrame(p, op, final)
}

func (c *Conn) writeFrame(p []byte, op Opcode, final bool) (int, error) {
	b := c.wbuf
	finb := csel[Opcode](final, finbit, 0)

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

	b = append(b, byte(op&opcodeMask|finb), c.client*masked|l7)

	switch l7 {
	case len16:
		b = binary.BigEndian.AppendUint16(b, uint16(len(p))) //nolint:gosec
	case len64:
		b = binary.BigEndian.AppendUint64(b, uint64(len(p)))
	}

	if c.client != 0 {
		b = append(b, 0, 0, 0, 0)
		_, _ = rand.Read(b[len(b)-4:])
	}

	payload := len(b)
	b = append(b, p...)

	if c.client != 0 {
		maskBuf(b[payload:], [4]byte(b[payload-4:payload]), 0)
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
	defer c.wmu.Unlock()
	c.wmu.Lock()

	defer func() {
		e := c.Conn.Close()
		if err == nil && e != nil {
			err = e
		}
	}()

	if c.writerClosed {
		return nil
	}

	c.writerClosed = true

	c.wbuf = append(c.wbuf, byte(FrameClose|finbit), c.client*masked)

	_, err = c.Conn.Write(c.wbuf[:2])
	if err != nil {
		return fmt.Errorf("write close frame: %w", err)
	}

	return nil
}

func (c *Conn) CloseWriter(status Status) (err error) {
	defer c.wmu.Unlock()
	c.wmu.Lock()

	return c.closeWriter(status)
}

func (c *Conn) closeWriter(status Status) (err error) {
	if c.writerClosed {
		return nil
	}

	c.writerClosed = true

	if status == 0 {
		status = 1000
	}

	body := []byte{byte(status >> 8), byte(status)}

	//	log.Printf("close writer %x (%[1]d)  % x", int(status), body)

	_, err = c.writeFrame(body, FrameClose, true)

	return err
}

func (c *Conn) processPing() error {
	defer c.wmu.Unlock()
	c.wmu.Lock()

	c.rbuf[c.st] = c.rbuf[c.st]&^opcodeMask | byte(FramePong)

	_, err := c.Conn.Write(c.rbuf[c.st : c.i+c.more])

	return err
}

func csel[T any](c bool, x, y T) T {
	if c {
		return x
	}

	return y
}
