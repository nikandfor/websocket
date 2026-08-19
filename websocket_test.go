package websocket

import (
	"bufio"
	"bytes"
	"errors"
	"testing"
)

func TestSecKey(t *testing.T) {
	exp := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	key64 := "dGhlIHNhbXBsZSBub25jZQ=="

	sum := secKeyHash(key64)

	if exp != sum {
		t.Errorf("expected [%v], got [%v]", exp, sum)
	}
}

func TestOpcodeString(t *testing.T) {
	for _, tc := range []struct {
		op   Opcode
		want string
	}{
		{op: FrameContinue, want: "continue"},
		{op: FrameText, want: "text"},
		{op: FrameBinary, want: "binary"},
		{op: FrameClose, want: "close"},
		{op: FramePing, want: "ping"},
		{op: FramePong, want: "pong"},
		{op: 0xf, want: "op:0xf"},
	} {
		if got := tc.op.String(); got != tc.want {
			t.Errorf("opcode %#x: wanted %q, got %q", int(tc.op), tc.want, got)
		}
	}
}

func TestStatusError(t *testing.T) {
	if got := StatusProtocol.Error(); got != "status:1002" {
		t.Errorf("wanted %q, got %q", "status:1002", got)
	}

	if !StatusOK.OK() || StatusProtocol.OK() {
		t.Errorf("OK is broken: %v %v", StatusOK.OK(), StatusProtocol.OK())
	}
}

func TestStatusTextError(t *testing.T) {
	s := &StatusText{Status: StatusInternal, Text: "bye"}

	if got := s.Error(); got != "status:1011 bye" {
		t.Errorf("wanted %q, got %q", "status:1011 bye", got)
	}

	if !errors.Is(s, StatusInternal) {
		t.Errorf("wanted unwrap to %v, got %v", int(StatusInternal), s.Unwrap())
	}
}

func TestMakeHeaderBits(t *testing.T) {
	for _, tc := range []struct {
		op     Opcode
		final  bool
		masked bool
		want   HeaderBits
	}{
		{op: FrameText, final: true, want: HeaderBits{0x81, 0x00}},
		{op: FrameBinary, want: HeaderBits{0x02, 0x00}},
		{op: FrameBinary, masked: true, want: HeaderBits{0x02, 0x80}},
		{op: FrameClose, final: true, masked: true, want: HeaderBits{0x88, 0x80}},
	} {
		h := MakeHeaderBits(int(tc.op), tc.final, tc.masked)

		if h != tc.want {
			t.Errorf("op %v final %v masked %v: wanted % x, got % x", tc.op, tc.final, tc.masked, tc.want, h)
		}

		if h.Opcode() != tc.op || h.Fin() != tc.final || h.Masked() != tc.masked {
			t.Errorf("op %v final %v masked %v: parsed back %v %v %v", tc.op, tc.final, tc.masked, h.Opcode(), h.Fin(), h.Masked())
		}

		if h.IsDataFrame() != (tc.op < FrameClose) {
			t.Errorf("op %v: IsDataFrame %v", tc.op, h.IsDataFrame())
		}
	}
}

func TestHeaderBitsParse(t *testing.T) {
	b := []byte{0xff, 0x82, 0x80}

	var h HeaderBits

	i := h.Parse(b, 1)
	if i != 3 || h != (HeaderBits{0x82, 0x80}) {
		t.Errorf("wanted 3, {82 80}; got %v, % x", i, h)
	}

	i = h.Parse(b, 2)
	if i != -1 || h != (HeaderBits{}) {
		t.Errorf("wanted -1, {00 00}; got %v, % x", i, h)
	}
}

func TestCopyBufferBig(t *testing.T) {
	payload := make([]byte, 2*defaultReadBufSize)
	for i := range payload {
		payload[i] = byte(i)
	}

	b := frame(FrameBinary, false, payload)

	r := bufio.NewReaderSize(bytes.NewReader(b), 3*defaultReadBufSize)

	_, err := r.Peek(1)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}

	if r.Buffered() != len(b) {
		t.Fatalf("wanted %d buffered, got %d", len(b), r.Buffered())
	}

	c := &Conn{Conn: &FakeConn{}}

	copyBuffer(c, r)

	if c.end != len(b) {
		t.Errorf("wanted %d carried over, got %d", len(b), c.end)
	}

	p := make([]byte, len(payload))

	n, err := readTimeout(t, c, p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if !bytes.Equal(p[:n], payload) {
		t.Errorf("payload is broken: got %d of %d bytes", n, len(payload))
	}
}
