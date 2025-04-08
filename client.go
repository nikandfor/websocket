package websocket

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type (
	Client struct {
		Header http.Header

		Dialer    net.Dialer
		TLSDialer tls.Dialer
	}

	DialerContext interface {
		DialContext(ctx context.Context, net, addr string) (net.Conn, error)
	}
)

func (c *Client) DialContext(ctx context.Context, rurl string) (*Conn, error) {
	req, err := c.NewRequest(ctx, rurl)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	conn, _, err := c.Handshake(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("handshake: %w", err)
	}

	return conn, nil
}

func (c *Client) NewRequest(ctx context.Context, rurl string) (*http.Request, error) {
	u, err := url.Parse(rurl)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}

	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	default:
		return nil, fmt.Errorf("unsupported scheme: %v", u.Scheme)
	}

	key := make([]byte, 16)
	_, _ = rand.Read(key)
	key64 := base64.StdEncoding.EncodeToString(key)

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	h := req.Header

	h.Set("Connection", "Upgrade")
	h.Set("Upgrade", "websocket")
	h.Set("Sec-WebSocket-Version", "13")
	h.Set("Sec-WebSocket-Key", key64)

	maps.Copy(h, c.Header)

	return req, nil
}

func (cl *Client) Handshake(ctx context.Context, req *http.Request) (conn *Conn, resp *http.Response, err error) {
	var d DialerContext

	switch req.URL.Scheme {
	case "http":
		d = &cl.Dialer
	case "https":
		d = &cl.TLSDialer
	default:
		return nil, nil, fmt.Errorf("unsupported scheme: %v", req.URL.Scheme)
	}

	host := req.URL.Host
	if req.URL.Port() == "" {
		host = net.JoinHostPort(host, req.URL.Scheme)
	}

	c, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, nil, fmt.Errorf("dial: %w", err)
	}

	defer closerOnErr(c, &err)

	err = req.Write(c)
	if err != nil {
		return nil, nil, fmt.Errorf("write request: %w", err)
	}

	r := bufio.NewReader(c)

	resp, err = http.ReadResponse(r, req)
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, resp, fmt.Errorf("didn't switch protocol: %v (%d)", resp.Status, resp.StatusCode)
	}

	h := resp.Header
	accept := secKeyHash(req.Header.Get("Sec-WebSocket-Key"))

	if q := h.Get("Connection"); strings.ToLower(q) != "upgrade" {
		return nil, resp, fmt.Errorf("didn't upgrade: %v", q)
	}
	if q := h.Get("Upgrade"); strings.ToLower(q) != "websocket" {
		return nil, resp, fmt.Errorf("upgraded protocol mismatch: %v", q)
	}
	if q := h.Get("Sec-WebSocket-Accept"); q == "" {
		return nil, resp, fmt.Errorf("no sec-accept in response")
	} else if q != accept {
		return nil, resp, fmt.Errorf("sec-accept mismatch")
	}

	conn = &Conn{
		Conn: c,

		client: 1,
	}

	if n := r.Buffered(); n != 0 {
		conn.rbuf = grow(conn.rbuf, min(n, minReadBuf))

		m, err := r.Read(conn.rbuf[:n])
		conn.end = m
		if err != nil {
			return nil, resp, fmt.Errorf("flush buffer")
		}
		if m != n {
			return nil, resp, fmt.Errorf("flush buffer: read %d of %d", m, n)
		}
	}

	return conn, resp, nil
}

func closerOnErr(c io.Closer, errp *error) {
	if *errp == nil {
		return
	}

	_ = c.Close()
}
