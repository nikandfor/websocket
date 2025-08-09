package websocket

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

type (
	Server struct {
		Handler Handler
	}

	Handler = func(ctx context.Context, c *Conn) error
)

func (s *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	hs, err := s.ServeHandler(w, req, s.Handler)
	if !hs && err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func (s *Server) ServeHandler(w http.ResponseWriter, req *http.Request, h Handler) (handshake bool, err error) {
	ctx := req.Context()

	c, err := s.Handshake(ctx, w, req)
	if err != nil {
		return false, fmt.Errorf("handshake: %w", err)
	}

	handshake = true

	defer func() {
		if err == nil {
			return
		}

		var s Status
		if errors.As(err, &s) && s.OK() {
			err = nil
		}
	}()

	defer closer(c, &err, "close conn")

	return handshake, h(ctx, c)
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
		return nil, ErrTrailingData
	}

	wc := &Conn{
		Conn: c,
	}

	return wc, nil
}
