package websocket

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
)

type FakeConn struct {
	b []byte
	r int

	net.Conn
}

func FuzzWriteRead(f *testing.F) { //nolint:gocognit
	f.Add(32, 1, []byte("first."), []byte("second_second."), []byte("third_third_third"))
	f.Add(32, 256, []byte("first."), []byte("second_second_second_second."), make([]byte, 128))

	f.Fuzz(func(t *testing.T, cbuf, rbuf int, m0, m1, m2 []byte) {
		if cbuf < minReadBufSize || cbuf > 0x1000 {
			return
		}
		if rbuf < 1 || rbuf > 0x1000 {
			return
		}
		if len(m0)+len(m1)+len(m2) > 0x100000 {
			return
		}

		to := make([]byte, len(m0)+len(m1)+len(m2)+0x100)
		var ton [5]int

		var c FakeConn

		w := &Conn{
			Conn: &c,
		}

		r := &Conn{
			Conn: &c,
			rbuf: make([]byte, cbuf),
		}

		for i, m := range [][]byte{m0, m1, m2} {
			nw, err := w.Write(m)
			if err != nil || nw != len(m) {
				t.Errorf("write %d: %v/%v %v", i, nw, len(m), err)
			}

			nr, err := r.Read(to[ton[i]:min(ton[i]+rbuf, len(to))])
			ton[i+1] = ton[i] + nr
			if err != nil && !errors.Is(err, io.EOF) {
				t.Errorf("read  %d: %v", i, err)
			}
		}

		ton[4] = ton[3]

		for range 0x100000 {
			n, err := r.Read(to[ton[4]:min(ton[4]+rbuf, len(to))])
			ton[4] += n
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Errorf("read aft: %v", err)
			}
		}

		off := 0

		for i, m := range [][]byte{m0, m1, m2} {
			if !bytes.Equal(m, to[off:off+len(m)]) {
				t.Errorf("message %d is broken\nmessage: %q\nwanted:  %q", i, to[off:off+len(m)], m)
			}

			off += len(m)
		}

		if off != ton[4] {
			t.Errorf("length mismatch %v != %v", ton[4], off)
		}

		err := w.Close()
		if err != nil {
			t.Errorf("close writer: %v", err)
		}

		err = r.Close()
		if err != nil {
			t.Errorf("close reader: %v", err)
		}

		if t.Failed() {
			t.Logf("conn buffer %4x  read buffer %4x", cbuf, rbuf)

			for i, m := range [][]byte{m0, m1, m2} {
				t.Logf("message%d: %q (%d)", i, m, len(m))
			}
		}
	})
}

func (c *FakeConn) Read(p []byte) (n int, err error) {
	n = copy(p, c.b[c.r:])
	c.r += n

	if c.r == len(c.b) {
		err = io.EOF
	}

	return n, err
}

func (c *FakeConn) Write(p []byte) (n int, err error) {
	c.b = append(c.b, p...)

	return len(p), nil
}

func (c *FakeConn) Close() error {
	c.b = c.b[:0]
	c.r = 0

	return nil
}
