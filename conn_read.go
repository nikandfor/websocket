package websocket

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"time"
)

type (
	Conn struct {
		net.Conn

		client byte

		writerClosed bool
		readerClosed bool

		wmu  sync.Mutex
		wbuf []byte

		rmu  sync.Mutex
		rbuf []byte

		st, i, end int // unparsed data in rbuf

		header HeaderBits
		key    [4]byte

		start, more int
	}
)

const minReadBuf = 0x1000

func (c *Conn) ReadContext(ctx context.Context, p []byte) (n int, err error) {
	if d, ok := c.Conn.(interface{ SetReadDeadline(time.Time) error }); ok {
		defer Stopper(ctx, d.SetReadDeadline)
	}

	n, err = c.Read(p)
	err = FixError(ctx, err)

	return n, err
}

func (c *Conn) Read(p []byte) (n int, err error) {
	defer c.rmu.Unlock()
	c.rmu.Lock()

	if c.more != 0 {
		n, err = c.readFrame(p)
		if errors.Is(err, io.EOF) {
			err = nil
		}

		return n, err
	}

	_, _, _, err = c.readDataFrameHeader()
	if err != nil {
		return 0, err
	}

	n, err = c.readFrame(p)
	if errors.Is(err, io.EOF) {
		err = nil
	}

	return n, err
}

func (c *Conn) ReadFrameHeader() (op byte, fin bool, l int, err error) {
	defer c.rmu.Unlock()
	c.rmu.Lock()

	return c.readDataFrameHeader()
}

func (c *Conn) ReadRawFrameHeader() (op byte, fin bool, l int, err error) {
	defer c.rmu.Unlock()
	c.rmu.Lock()

	return c.readFrameHeader()
}

func (c *Conn) readDataFrameHeader() (op byte, fin bool, l int, err error) {
	for {
		op, fin, l, err = c.readFrameHeader()
		if err != nil {
			return op, fin, l, err
		}

		switch op {
		case FrameContinue, FrameText, FrameBinary:
			return op, fin, l, nil
		case FramePing:
			c.processPing()
		case FramePong:
		case FrameClose:
			return op, false, 0, c.processClose()
		default:
			return op, false, 0, errors.New("invalid frame")
		}
	}
}

func (c *Conn) readFrameHeader() (op byte, fin bool, l int, err error) {
	if c.readerClosed {
		return 0, true, 0, io.EOF
	}

	if c.more != 0 {
		c.i += c.more
	}

	c.st = c.i

	for {
		h, l, i := c.parseFrameHeader(c.rbuf[:c.end], c.st, c.key[:])
		//	log.Printf("frame header h,l,i %x %x %x   c.st,i,end,len %x %x %x %x   data %x %x", h, l, i, c.st, c.i, c.end, len(c.rbuf), c.start, c.more)
		//	log.Printf("rbuf\n%s", hex.Dump(c.rbuf[:c.end]))
		if i > 0 {
			c.header = h
			c.start = i
			c.more = l
			c.i = i

			return h.Opcode(), h.Fin(), l, nil
		}

		err = c.read()
		if err != nil {
			return 0, false, l, err
		}
	}
}

func (c *Conn) ReadFrame(p []byte) (n int, err error) {
	defer c.rmu.Unlock()
	c.rmu.Lock()

	return c.readFrame(p)
}

func (c *Conn) AppendReadFrame(b []byte) ([]byte, error) {
	defer c.rmu.Unlock()
	c.rmu.Lock()

	return c.appendFrame(b, c.more)
}

func (c *Conn) AppendReadFrameLimit(b []byte, limit int) ([]byte, error) {
	defer c.rmu.Unlock()
	c.rmu.Lock()

	return c.appendFrame(b, min(c.more, limit-len(b)))
}

func (c *Conn) readFrame(p []byte) (int, error) {
	res, err := c.appendFrame(p[:0], len(p))
	return len(res), err
}

func (c *Conn) appendFrame(p []byte, more int) (_ []byte, err error) {
	if c.more == 0 {
		return p, io.EOF
	}

	n := len(p)
	more = min(more, c.more)
	p = slices.Grow(p, more)
	p = p[:len(p)+more]

	var m int

	for more != 0 {
		//	log.Printf("read %v %v %v  more %v %v  n/len %v %v", c.start, c.i, c.end, more, c.more, n, len(p))
		if c.i < c.end {
			end := min(c.end, c.i+more)
			m = copy(p[n:], c.rbuf[c.i:end])
		} else {
			m, err = c.Conn.Read(p[n : n+more])
			if err != nil {
				return p[:n], err
			}
		}

		maskBuf(p[n:n+m], c.key, c.i-c.start)
		n += m
		more -= m
		c.i += m
		c.more -= m
	}

	return p[:n], csel(c.more == 0, io.EOF, nil)
}

func (c *Conn) parseFrameHeader(b []byte, st int, key []byte) (h HeaderBits, l int, i int) {
	i = st
	if i+2 > len(b) {
		return h, 0, -1
	}

	i += copy(h[:], b[i:])

	l, i = h.ParseLen(c.rbuf, i)
	if i < 0 {
		return h, 0, -1
	}

	if h.Masked() {
		i = h.ReadMaskingKey(b, i, key)
		if i < 0 {
			return h, 0, -1
		}
	}

	return h, l, i
}

func (c *Conn) processClose() (err error) {
	c.readerClosed = true

	if c.more == 0 {
		return io.EOF
	}

	if c.more == 1 {
		return fmt.Errorf("wtf close data: %x", c.rbuf[c.i])
	}

	size := min(c.more, 128)

	c.rbuf, err = c.appendFrame(c.rbuf[:c.end], size)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if err != nil {
		return err
	}

	status := binary.BigEndian.Uint16(c.rbuf[c.end:])
	if len(c.rbuf[c.end:]) == 2 {
		if Status(status) == StatusOK {
			return io.EOF
		}

		return Status(status)
	}

	text := c.rbuf[c.end+2 : c.end+size]

	return &StatusText{
		Status: Status(status),
		Text:   string(text),
	}
}

func (c *Conn) read() error {
	if len(c.rbuf) == 0 {
		c.rbuf = make([]byte, minReadBuf)
	}

	if c.i >= c.end/2 {
		off := c.i

		copy(c.rbuf, c.rbuf[off:c.end])
		c.st -= off
		c.i -= off
		c.end -= off
	}

	if c.end == len(c.rbuf) {
		panic(c.end)
	}

	n, err := c.Conn.Read(c.rbuf[c.end:])
	c.end += n

	return err
}

func Stopper(ctx context.Context, dead func(time.Time) error) func() {
	donec := make(chan struct{})

	go func() {
		select {
		case <-ctx.Done():
		case <-donec:
			return
		}

		select {
		case <-donec:
			return
		default:
		}

		_ = dead(time.Unix(1, 0))
	}()

	return func() {
		close(donec)
	}
}

func FixError(ctx context.Context, err error) error {
	if isTimeout(err) {
		select {
		case <-ctx.Done():
			err = ctx.Err()
		default:
		}
	}

	return err
}

func isTimeout(err error) bool {
	to, ok := err.(interface{ Timeout() bool })

	return ok && to.Timeout()
}
