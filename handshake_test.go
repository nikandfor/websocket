package websocket

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServerHandshakeCarryOver(t *testing.T) {
	payloadc := make(chan string, 1)

	var s Server

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := s.Handshake(req.Context(), w, req)
		if err != nil {
			t.Errorf("handshake: %v", err)
			return
		}

		defer c.Close()

		p := make([]byte, 16)

		n, err := c.Read(p)
		if err != nil {
			t.Errorf("read: %v", err)
			return
		}

		payloadc <- string(p[:n])
	}))
	defer srv.Close()

	cl, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	defer cl.Close()

	req := "GET /ws HTTP/1.1\r\n" +
		"Host: " + srv.Listener.Addr().String() + "\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"\r\n"

	_, err = cl.Write(append([]byte(req), frame(FrameBinary, true, []byte("hello"))...))
	if err != nil {
		t.Fatalf("write request: %v", err)
	}

	select {
	case p := <-payloadc:
		if p != "hello" {
			t.Errorf("wanted %q, got %q", "hello", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no payload")
	}
}

func TestClientHandshakeCarryOver(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	donec := make(chan struct{})

	var wg sync.WaitGroup

	defer wg.Wait()
	defer close(donec)
	defer l.Close()

	wg.Add(1)

	go func() {
		defer wg.Done()

		c, err := l.Accept()
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}

		defer c.Close()

		req, err := http.ReadRequest(bufio.NewReader(c))
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}

		resp := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Connection: Upgrade\r\n" +
			"Upgrade: websocket\r\n" +
			"Sec-WebSocket-Accept: " + secKeyHash(req.Header.Get("Sec-WebSocket-Key")) + "\r\n" +
			"\r\n"

		_, err = c.Write(append([]byte(resp), frame(FrameBinary, false, []byte("hello"))...))
		if err != nil {
			t.Errorf("write response: %v", err)
			return
		}

		<-donec
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var cl Client

	c, err := cl.DialContext(ctx, "ws://"+l.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	defer c.Close()

	p := make([]byte, 16)

	n, err := readTimeout(t, c, p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if string(p[:n]) != "hello" {
		t.Errorf("wanted %q, got %q", "hello", p[:n])
	}
}

func TestServerHandshakeConnectionTokens(t *testing.T) {
	for _, tc := range []struct {
		name string
		conn string
		err  error
	}{
		{name: "keep_alive_upgrade", conn: "Connection: keep-alive, Upgrade\r\n"},
		{name: "repeated_header", conn: "Connection: keep-alive\r\nConnection: Upgrade\r\n"},
		{name: "no_token", conn: "Connection: keep-alive\r\n", err: ErrNotWebsocket},
		{name: "no_header", err: ErrNotWebsocket},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := handshakeServer(t, tc.conn+wsHeaders)
			if !errors.Is(err, tc.err) {
				t.Errorf("wanted %v, got %v (%T)", tc.err, err, err)
			}
		})
	}
}

func TestClientHandshakeConnectionTokens(t *testing.T) {
	for _, tc := range []struct {
		name string
		conn string
	}{
		{name: "keep_alive_upgrade", conn: "Connection: keep-alive, Upgrade\r\n"},
		{name: "repeated_header", conn: "Connection: keep-alive\r\nConnection: Upgrade\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := handshakeClient(t, tc.conn)
			if err != nil {
				t.Errorf("handshake: %v", err)
			}
		})
	}
}

func TestServerHandshakeRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		hdr  string
		err  error
	}{
		{name: "ok", hdr: wsHeaders},
		{name: "no_version", hdr: "Upgrade: websocket\r\n" + wsKey, err: ErrNotWebsocket},
		{name: "old_version", hdr: "Upgrade: websocket\r\nSec-WebSocket-Version: 8\r\n" + wsKey, err: ErrNotWebsocket},
		{name: "no_key", hdr: "Upgrade: websocket\r\nSec-WebSocket-Version: 13\r\n", err: ErrProtocol},
		{name: "wrong_upgrade", hdr: "Upgrade: h2c\r\nSec-WebSocket-Version: 13\r\n" + wsKey, err: ErrNotWebsocket},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := handshakeServer(t, "Connection: Upgrade\r\n"+tc.hdr)
			if !errors.Is(err, tc.err) {
				t.Errorf("wanted %v, got %v (%T)", tc.err, err, err)
			}
		})
	}
}

func TestServerHandshakeMethod(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodHead} {
		err := serveRequest(t, method, "Content-Length: 0\r\nConnection: Upgrade\r\n"+wsHeaders,
			func(w http.ResponseWriter, req *http.Request) error {
				var s Server

				_, err := s.Handshake(req.Context(), w, req)

				return err
			})

		if !errors.Is(err, ErrNotWebsocket) {
			t.Errorf("%v: wanted %v, got %v (%T)", method, ErrNotWebsocket, err, err)
		}
	}
}

func TestServerHandshakeNotHijacker(t *testing.T) {
	var s Server

	req := httptest.NewRequest(http.MethodGet, "/ws", http.NoBody)

	_, err := s.Handshake(req.Context(), httptest.NewRecorder(), req)
	if !errors.Is(err, ErrNotHijacker) {
		t.Errorf("wanted %v, got %v (%T)", ErrNotHijacker, err, err)
	}
}

func TestServeHandler(t *testing.T) {
	errPlain := errors.New("handler failed")

	for _, tc := range []struct {
		name string
		ret  error
		err  error
	}{
		{name: "nil"},
		{name: "error", ret: errPlain, err: errPlain},
		{name: "status_ok", ret: StatusOK},
		{name: "status_protocol", ret: StatusProtocol, err: StatusProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s Server

			var handshake bool

			err := serveRequest(t, http.MethodGet, "Connection: Upgrade\r\n"+wsHeaders, func(w http.ResponseWriter, req *http.Request) error {
				var err error

				handshake, err = s.ServeHandler(w, req, func(ctx context.Context, c *Conn) error { return tc.ret })

				return err
			})

			if !errors.Is(err, tc.err) {
				t.Errorf("wanted %v, got %v (%T)", tc.err, err, err)
			}

			if !handshake {
				t.Errorf("wanted handshake")
			}
		})
	}
}

func TestServeHandlerNoHandshake(t *testing.T) {
	var s Server

	var handshake bool

	err := serveRequest(t, http.MethodGet, "Connection: keep-alive\r\n"+wsHeaders, func(w http.ResponseWriter, req *http.Request) error {
		var err error

		handshake, err = s.ServeHandler(w, req, func(ctx context.Context, c *Conn) error { return nil })

		return err
	})

	if !errors.Is(err, ErrNotWebsocket) {
		t.Errorf("wanted %v, got %v (%T)", ErrNotWebsocket, err, err)
	}

	if handshake {
		t.Errorf("wanted no handshake")
	}
}

func TestServeHTTPBadRequest(t *testing.T) {
	s := &Server{Handler: func(ctx context.Context, c *Conn) error { return nil }}

	srv := httptest.NewServer(s)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("wanted %v, got %v", http.StatusBadRequest, resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), ErrNotWebsocket.Error()) {
		t.Errorf("wanted %q in body, got %q", ErrNotWebsocket, body)
	}
}

func TestServeHTTPHandshake(t *testing.T) {
	donec := make(chan struct{})

	s := &Server{Handler: func(ctx context.Context, c *Conn) error {
		close(donec)

		return nil
	}}

	err := serveRequest(t, http.MethodGet, "Connection: Upgrade\r\n"+wsHeaders, func(w http.ResponseWriter, req *http.Request) error {
		s.ServeHTTP(w, req)

		return nil
	})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}

	select {
	case <-donec:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not called")
	}
}

func TestClientNewRequest(t *testing.T) {
	for _, tc := range []struct {
		url    string
		scheme string
	}{
		{url: "ws://host/ws", scheme: "http"},
		{url: "http://host/ws", scheme: "http"},
		{url: "wss://host/ws", scheme: "https"},
		{url: "https://host/ws", scheme: "https"},
		{url: "ftp://host/ws"},
	} {
		cl := Client{Header: http.Header{"origin": []string{"tests"}}}

		req, err := cl.NewRequest(context.Background(), tc.url)
		if tc.scheme == "" {
			if err == nil {
				t.Errorf("%v: wanted error, got %v", tc.url, req.URL)
			}

			continue
		}

		if err != nil {
			t.Fatalf("%v: new request: %v", tc.url, err)
		}

		if req.URL.Scheme != tc.scheme {
			t.Errorf("%v: wanted scheme %v, got %v", tc.url, tc.scheme, req.URL.Scheme)
		}

		h := req.Header

		if h.Get("Origin") != "tests" {
			t.Errorf("%v: header is not merged and canonicalized: %v", tc.url, h)
		}

		if h.Get("Upgrade") != "websocket" || h.Get("Connection") != "Upgrade" || h.Get("Sec-WebSocket-Version") != "13" {
			t.Errorf("%v: bad handshake headers: %v", tc.url, h)
		}

		key, err := base64.StdEncoding.DecodeString(h.Get("Sec-WebSocket-Key"))
		if err != nil || len(key) != 16 {
			t.Errorf("%v: bad key %q: %v", tc.url, h.Get("Sec-WebSocket-Key"), err)
		}
	}
}

const (
	wsKey     = "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"
	wsHeaders = "Upgrade: websocket\r\nSec-WebSocket-Version: 13\r\n" + wsKey
)

func handshakeServer(tb testing.TB, hdr string) error {
	tb.Helper()

	var s Server

	return serveRequest(tb, http.MethodGet, hdr, func(w http.ResponseWriter, req *http.Request) error {
		c, err := s.Handshake(req.Context(), w, req)
		if err == nil {
			c.Close()
		}

		return err
	})
}

func serveRequest(tb testing.TB, method, hdr string, h func(w http.ResponseWriter, req *http.Request) error) error {
	tb.Helper()

	errc := make(chan error, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		errc <- h(w, req)
	}))
	defer srv.Close()

	cl, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		tb.Fatalf("dial: %v", err)
	}

	defer cl.Close()

	req := method + " /ws HTTP/1.1\r\n" +
		"Host: " + srv.Listener.Addr().String() + "\r\n" +
		hdr +
		"\r\n"

	_, err = cl.Write([]byte(req))
	if err != nil {
		tb.Fatalf("write request: %v", err)
	}

	select {
	case err := <-errc:
		return err
	case <-time.After(2 * time.Second):
		tb.Fatal("no handshake")
	}

	return nil
}

func handshakeClient(tb testing.TB, conn string) error {
	tb.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listen: %v", err)
	}

	donec := make(chan struct{})

	var wg sync.WaitGroup

	tb.Cleanup(func() {
		close(donec)
		l.Close()
		wg.Wait()
	})

	wg.Add(1)

	go func() {
		defer wg.Done()

		c, err := l.Accept()
		if err != nil {
			return
		}

		defer c.Close()

		req, err := http.ReadRequest(bufio.NewReader(c))
		if err != nil {
			tb.Errorf("read request: %v", err)
			return
		}

		resp := "HTTP/1.1 101 Switching Protocols\r\n" +
			conn +
			"Upgrade: websocket\r\n" +
			"Sec-WebSocket-Accept: " + secKeyHash(req.Header.Get("Sec-WebSocket-Key")) + "\r\n" +
			"\r\n"

		_, err = c.Write([]byte(resp))
		if err != nil {
			tb.Errorf("write response: %v", err)
			return
		}

		<-donec
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var cl Client

	c, err := cl.DialContext(ctx, "ws://"+l.Addr().String())
	if err != nil {
		return err
	}

	tb.Cleanup(func() { c.Close() })

	return nil
}

// server accepts and never answers: the ctx deadline must cut the handshake short
func TestClientDialContextDeadline(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	donec := make(chan struct{})

	var wg sync.WaitGroup

	t.Cleanup(func() {
		close(donec)
		l.Close()
		wg.Wait()
	})

	wg.Add(1)

	go func() {
		defer wg.Done()

		c, err := l.Accept()
		if err != nil {
			return
		}

		defer c.Close()

		<-donec
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	errc := make(chan error, 1)

	go func() {
		var cl Client

		c, err := cl.DialContext(ctx, "ws://"+l.Addr().String())
		if err == nil {
			c.Close()
		}

		errc <- err
	}()

	select {
	case err := <-errc:
		if err == nil {
			t.Errorf("wanted an error, got a connection")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dial ignored the ctx deadline")
	}
}
