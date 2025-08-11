package websocket

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

type (
	HeaderBits [2]byte

	Opcode byte
	Status uint16

	StatusText struct {
		Status Status
		Text   string
	}
)

const (
	StatusOK Status = 1000 + iota
	StatusGoingAway
	StatusProtocol
	StatusCantAccept
	_

	_
	_
	StatusFormat
	StatusPolicy
	StatusTooBig

	StatusExtensions
	StatusInternal
	_
	_
	_

	_
)

const (
	// first byte.
	finbit     = 0x80
	opcodeMask = 0xf

	// second byte.
	masked   = 0x80
	len7Mask = 0x7f

	len16 = 128 - 2
	len64 = 128 - 1

	maxLen7  = 128 - 3
	maxLen16 = 1<<16 - 1
	maxLen64 = 1<<62 - 1

	// Non-control frames.
	FrameContinue Opcode = 0
	FrameText     Opcode = 1
	FrameBinary   Opcode = 2

	// Control Frames.
	FrameClose Opcode = 0x8
	FramePing  Opcode = 0x9
	FramePong  Opcode = 0xa
)

var (
	//	ErrClosed       = errors.New("attempt to write to closed connection")
	ErrNotHijacker  = errors.New("response is not hijacker")
	ErrNotWebsocket = errors.New("not websocket")
	ErrProtocol     = StatusProtocol
	ErrTrailingData = errors.New("trailing data in request")
)

func maskBuf(p []byte, key [4]byte, off int) {
	for i := range p {
		p[i] ^= key[(off+i)&3]
	}
}

func MakeHeaderBits(op int, final, masked bool) HeaderBits {
	var h HeaderBits

	h[0] = byte(op) & opcodeMask
	if final {
		h[0] |= finbit
	}

	return h
}

func (f *HeaderBits) Parse(b []byte, st int) int {
	if st+2 > len(b) {
		*f = HeaderBits{}
		return -1
	}

	f[0] = b[st]
	f[1] = b[st+1]

	return st + 2
}

func (f HeaderBits) ParseLen(b []byte, st int) (l, i int) {
	l = f.len7()
	i = st

	switch {
	case l <= maxLen7:
		// l is fine already
	case l == len16:
		if i+2 > len(b) {
			return l, -1
		}

		l = int(binary.BigEndian.Uint16(b[i:]))
		i += 2
	case l == len64:
		if i+8 > len(b) {
			return l, -1
		}

		x := binary.BigEndian.Uint64(b[i:])
		l = int(x) //nolint:gosec
		i += 8

		if uint64(l) != x { //nolint:gosec
			panic("too big frame")
		}
	default:
		panic(l)
	}

	return l, i
}

func (f HeaderBits) ReadMaskingKey(b []byte, st int, key []byte) (i int) {
	if st+4 > len(b) {
		return -1
	}

	return st + copy(key, b[st:st+4])
}

func (f HeaderBits) Fin() bool {
	return f[0]&finbit != 0
}

func (f HeaderBits) Opcode() Opcode {
	return Opcode(f[0] & opcodeMask)
}

func (f HeaderBits) IsDataFrame() bool {
	return f.Opcode() < 8
}

func (f HeaderBits) Masked() bool {
	return f[1]&masked != 0
}

func (f HeaderBits) len7() int {
	return int(f[1] & len7Mask)
}

func (s Status) OK() bool           { return s == StatusOK }
func (s Status) Error() string      { return fmt.Sprintf("status:%d", int(s)) }
func (s *StatusText) Error() string { return fmt.Sprintf("status:%d %v", int(s.Status), s.Text) }
func (s *StatusText) Unwrap() error { return s.Status }

func secKeyHash(key string) string {
	const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	h := sha1.New()

	_, _ = h.Write([]byte(key))
	_, _ = h.Write([]byte(guid))

	var sum [sha1.Size]byte

	h.Sum(sum[:0])

	return base64.StdEncoding.EncodeToString(sum[:])
}

func grow(b []byte, n int) []byte {
	if n > cap(b) {
		b = append(b, make([]byte, n-cap(b))...)
	}

	return b[:cap(b)]
}

func closer(c io.Closer, errp *error, msg string) {
	err := c.Close()
	if *errp == nil && err != nil {
		*errp = fmt.Errorf("%v: %w", msg, err)
	}
}
