package websocket

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

type (
	Conn struct {
		net.Conn

		client byte

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

loop:
	for {
		op, _, _, err := c.readFrameHeader()
		if err != nil {
			return 0, err
		}

		switch op {
		case FrameContinue,
			FrameBinary,
			FrameText:
			break loop
		case FramePing:
			c.processPing()
		case FramePong:
		case FrameClose:
			return 0, c.processClose()
		default:
			panic(op)
		}
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

	return c.readFrameHeader()
}

func (c *Conn) readFrameHeader() (op byte, fin bool, l int, err error) {
	if c.more != 0 {
		c.i += c.more
	}

	c.st = c.i

	for {
		h, l, i := c.parseFrameHeader(c.rbuf[:c.end], c.st, c.key[:])
		//	log.Printf("frame header %x %v %v  c %v %v %v %v  data %v %v", h, l, i, c.st, c.i, c.end, len(c.rbuf), c.start, c.more)
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

func (c *Conn) readFrame(p []byte) (n int, err error) {
	if c.more == 0 {
		return 0, io.EOF
	}

	for {
		//	log.Printf("read frame %v %v %v %v  data %v %v", c.st, c.i, c.end, len(c.rbuf), c.start, c.more)
		if c.i < c.end {
			end := min(c.end, c.i+c.more)

			m := copy(p[n:], c.rbuf[c.i:end])
			maskBuf(p[n:n+m], c.key, c.i-c.start)
			n += m
			c.i += m
			c.more -= m

			return n, csel(c.more == 0, io.EOF, nil)
		}

		if len(p[n:]) < 0x400 {
			err = c.read()
		} else {
			var m int
			m, err = c.Conn.Read(p[n : n+c.more])
			n += m
			c.more -= m
		}
		if err != nil {
			return n, err
		}
	}
}

func (c *Conn) parseFrameHeader(b []byte, st int, key []byte) (h HeaderBits, l int, i int) {
	i = st
	if i+2 > len(b) {
		return h, 0, -1
	}

	i += copy(h[:], b[i:])

	l, i = h.ParseLen(c.rbuf[st:], i)
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
