package websocket

import (
	"context"
	"fmt"
	"net/http"
)

type (
	Server struct{}
)

func (s *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var err error
	ctx := req.Context()

	c, err := s.Handshake(ctx, w, req)
	if err != nil {
		http.Error(w, fmt.Sprintf("handshake: %v", err), http.StatusBadRequest)
		return
	}

	defer func() {
		if err == nil {
			return
		}

		_ = err
	}()

	defer closer(c, &err, "close conn")

	buf := make([]byte, 1024)

	for {
		n, err := c.Read(buf)
		if err != nil {
			err = fmt.Errorf("read: %w", err)
			return
		}

		_, err = c.Write(buf[:n])
		if err != nil {
			err = fmt.Errorf("write: %w", err)
			return
		}
	}
}

func (s *Server) Handshake(ctx context.Context, w http.ResponseWriter, req *http.Request) (*Conn, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, ErrNotHijacker
	}

	var key string
	h := req.Header

	if v := h.Get("Connection"); v != "Upgrade" {
		return nil, ErrNotWebsocket
	}
	if v := h.Get("Upgrade"); v != "websocket" {
		return nil, ErrNotWebsocket
	}
	if v := h.Get("Sec-WebSocket-Version"); v != "13" {
		return nil, ErrNotWebsocket
	}
	if v := h.Get("Sec-WebSocket-Key"); v == "" {
		return nil, ErrProtocol
	} else {
		key = v
	}

	h = w.Header()

	h.Set("Connection", "Upgrade")
	h.Set("Upgrade", "websocket")
	h.Set("Sec-WebSocket-Accept", secKeyHash(key))

	w.WriteHeader(http.StatusSwitchingProtocols)

	c, buf, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack: %w", err)
	}

	if buf.Reader.Buffered() != 0 || buf.Writer.Buffered() != 0 {
		return nil, ErrExtraData
	}

	wc := &Conn{
		rwc: c,
	}

	return wc, nil
}
