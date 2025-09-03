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

		//	rmu  sync.Mutex

		rbuf []byte

		st, i, end int // unparsed data in rbuf

		header HeaderBits
		key    [4]byte

		start int // start of the frame in rbuf, needed for masking offset calculation
		more  int // more bytes to read in frame

		// end of rmu
	}

	Frame struct {
		Opcode Opcode
		Length int
		Final  bool

		c *Conn
	}
)

const (
	defaultReadBufSize = 0x1000
	minReadBufSize     = 0x20
)

func (c *Conn) Read(p []byte) (n int, err error) {
	return c.ReadContext(nil, p)
}

func (c *Conn) ReadContext(ctx context.Context, p []byte) (n int, err error) {
	//	defer c.rmu.Unlock()
	//	c.rmu.Lock()

	//	defer func(f dbgfn) {
	//		f(n, err)
	//	}(c.debug("Read"))

	err = c.waitForDataFrame(ctx)
	if err != nil {
		return 0, err
	}

	n, err = c.readFrame(ctx, p)
	if errors.Is(err, io.EOF) {
		err = nil
	}

	return n, err
}

func (c *Conn) waitForDataFrame(ctx context.Context) error {
	if c.more != 0 {
		return nil
	}

	_, _, _, err := c.readDataFrameHeader(ctx)
	if err != nil {
		return err
	}

	return nil
}

func (c *Conn) NextFrame(ctx context.Context) (Frame, error) {
	//	defer c.rmu.Unlock()
	//	c.rmu.Lock()

	op, l, fin, err := c.readDataFrameHeader(ctx)
	if err != nil {
		return Frame{}, err
	}

	f := Frame{
		Opcode: op,
		Length: l,
		Final:  fin,

		c: c,
	}

	return f, nil
}

func (c *Conn) NextRawFrame(ctx context.Context) (Frame, error) {
	//	defer c.rmu.Unlock()
	//	c.rmu.Lock()

	op, l, fin, err := c.readFrameHeader(ctx)
	if err != nil {
		return Frame{}, err
	}

	f := Frame{
		Opcode: op,
		Length: l,
		Final:  fin,

		c: c,
	}

	return f, nil
}

func (c *Conn) readDataFrameHeader(ctx context.Context) (op Opcode, l int, fin bool, err error) {
	for {
		op, l, fin, err = c.readFrameHeader(ctx)
		if err != nil {
			return op, l, fin, err
		}

		switch op {
		case FrameContinue, FrameText, FrameBinary:
			return op, l, fin, nil
		case FramePing:
			err = c.processPing()
			if err != nil {
				return op, 0, false, err
			}
		case FramePong:
		case FrameClose:
			return op, 0, false, c.processClose(ctx)
		default:
			return op, 0, false, errors.New("invalid frame")
		}
	}
}

func (c *Conn) readFrameHeader(ctx context.Context) (op Opcode, l int, fin bool, err error) {
	if c.readerClosed {
		return 0, 0, true, io.EOF
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

			return h.Opcode(), l, h.Fin(), nil
		}

		n, err := c.read(ctx)
		if n != 0 && errors.Is(err, io.EOF) {
			continue
		}
		if err != nil {
			return 0, l, false, err
		}
	}
}

func (f Frame) Read(p []byte) (n int, err error) {
	//	defer f.c.rmu.Unlock()
	//	f.c.rmu.Lock()

	return f.c.readFrame(nil, p)
}

func (f Frame) ReadContext(ctx context.Context, p []byte) (n int, err error) {
	//	defer f.c.rmu.Unlock()
	//	f.c.rmu.Lock()

	return f.c.readFrame(nil, p)
}

func (f Frame) ReadAppendTo(ctx context.Context, b []byte) ([]byte, error) {
	//	defer f.c.rmu.Unlock()
	//	f.c.rmu.Lock()

	return f.c.appendFrame(ctx, b, f.c.more)
}

func (f Frame) ReadAppendToLimit(ctx context.Context, b []byte, limit int) ([]byte, error) {
	//	defer f.c.rmu.Unlock()
	//	f.c.rmu.Lock()

	return f.c.appendFrame(ctx, b, min(f.c.more, limit-len(b)))
}

func (f Frame) More() int {
	//	defer f.c.rmu.Unlock()
	//	f.c.rmu.Lock()

	return f.c.more
}

func (c *Conn) readFrame(ctx context.Context, p []byte) (int, error) {
	res, err := c.appendFrame(ctx, p[:0], len(p))
	return len(res), err
}

func (c *Conn) appendFrame(ctx context.Context, p []byte, more int) (p0 []byte, err error) {
	//	defer func(f dbgfn) {
	//		f(len(p0), err)
	//	}(c.debug("appendFrame"))

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
		switch {
		case c.i < c.end:
			end := min(c.end, c.i+more)
			m = copy(p[n:], c.rbuf[c.i:end])
		case more >= len(c.rbuf)-0x10:
			m, err = c.Conn.Read(p[n : n+more])
			if err != nil && !errors.Is(err, io.EOF) {
				return p[:n], err
			}
		default:
			nread, err := c.read(ctx)
			if err != nil && !errors.Is(err, io.EOF) || nread == 0 {
				return p[:n], err
			}

			continue
		}

		maskBuf(p[n:n+m], c.key, c.i-c.start)
		n += m
		more -= m
		c.i += m
		c.more -= m
	}

	return p[:n], csel(c.more == 0, io.EOF, nil)
}

func (c *Conn) parseFrameHeader(b []byte, st int, key []byte) (h HeaderBits, l, i int) {
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

func (c *Conn) processClose(ctx context.Context) (err error) {
	c.readerClosed = true

	if c.more == 0 {
		return io.EOF
	}

	if c.more == 1 {
		return fmt.Errorf("wtf close data: %x", c.rbuf[c.i])
	}

	size := min(c.more, 128)

	c.rbuf, err = c.appendFrame(ctx, c.rbuf[:c.end], size)
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

func (c *Conn) read(ctx context.Context) (n int, err error) {
	//	defer func(f dbgfn) {
	//		f(n, err)
	//	}(c.debug("read"))

	if len(c.rbuf) < minReadBufSize {
		c.rbuf = make([]byte, defaultReadBufSize)
	}

	if c.i >= c.end/2 {
		off := c.i

		if off < c.end {
			copy(c.rbuf, c.rbuf[off:c.end])
		}

		c.st -= off
		c.i -= off
		c.end -= off
		c.start -= off
	}

	if c.end < 0 {
		c.end = 0
	}
	if c.end >= len(c.rbuf) {
		panic(c.end)
	}

	if d, ok := c.Conn.(interface{ SetReadDeadline(time.Time) error }); ctx != nil && ok {
		defer Stopper(ctx, d.SetReadDeadline)()
	}

	n, err = c.Conn.Read(c.rbuf[c.end:])
	c.end += n
	err = FixError(ctx, err)

	return n, err
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

/*
type dbgfn func(args ...any)

func (c *Conn) debug(name string) dbgfn { //nolint:unused
	log.Printf("%-14v >  st/i/end: %3x %3x %3x  rbuf %3x  start/more: %3x %3x", name, c.st, c.i, c.end, len(c.rbuf), c.start, c.more)

	return func(args ...any) {
		log.Printf("%-14v <  st/i/end: %3x %3x %3x  rbuf %3x  start/more: %3x %3x  ret %v  from %v", name, c.st, c.i, c.end, len(c.rbuf), c.start, c.more, args, caller(2))
	}
}

func caller(d int) string {
	_, file, line, _ := runtime.Caller(d + 1)

	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}
*/
