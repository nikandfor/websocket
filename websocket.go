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
	FlusherError interface {
		FlushError() error
	}

	Flusher interface {
		Flush()
	}

	flusher struct {
		Flusher
	}

	HeaderBits [2]byte

	FrameMeta struct {
		HeaderBits
		Status Status
		Key    [4]byte
		Length int
	}

	Status uint16
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
	// first byte
	fin        = 0x80
	opcodeMask = 0xf

	// second byte
	masked   = 0x80
	len7Mask = 0x7f

	len16 = 128 - 2
	len64 = 128 - 1

	maxLen7  = 128 - 3
	maxLen16 = 1<<16 - 1
	maxLen64 = 1<<62 - 1

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

func (f *FrameMeta) Parse(b []byte, st int) (i, end int) {
	f.Status = 0
	f.Key = [4]byte{}
	f.Length = 0

	i = f.HeaderBits.Parse(b, st)
	if i < 0 {
		return st, i
	}

	f.Length, i = f.HeaderBits.ParseLen(b, i)
	if i < 0 {
		return st, i
	}

	if f.Masked() {
		i = f.HeaderBits.ReadMaskingKey(b, i, f.Key[:])
		if i < 0 {
			return st, i
		}
	}

	if f.Opcode() == FrameClose && i+2 <= len(b) && f.Length >= 2 {
		f.Status = Status(binary.BigEndian.Uint16(b[i:]))
	}

	return i, i + f.Length
}

func (f *FrameMeta) Read(p, b []byte, st int) (n, i int) {
	if st+int(f.Length) > len(b) {
		return 0, -1
	}

	n = copy(p, b[st:st+int(f.Length)])

	if f.HeaderBits.Masked() {
		f.Mask(p[:n])
	}

	return n, st + n
}

func (f *FrameMeta) Mask(p []byte) {
	for i := range len(p) {
		p[i] ^= f.Key[i&3]
	}
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
		l = int(x)
		i += 8

		if uint64(l) != x {
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
	return f[0]&fin != 0
}

func (f HeaderBits) Opcode() int {
	return int(f[0] & opcodeMask)
}

func (f HeaderBits) Masked() bool {
	return f[1]&masked != 0
}

func (f HeaderBits) len7() int {
	return int(f[1] & len7Mask)
}

func (s Status) Error() string { return fmt.Sprintf("status:%d", int(s)) }

func (f flusher) FlushError() error { f.Flush(); return nil }

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
