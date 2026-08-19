package websocket

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
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

	conn, resp, err := c.Handshake(ctx, req)
	if resp != nil {
		_ = resp.Body.Close()
	}
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
	case "ws", "http":
		u.Scheme = "http"
	case "wss", "https":
		u.Scheme = "https"
	default:
		return nil, fmt.Errorf("unsupported scheme: %v", u.Scheme)
	}

	key := make([]byte, 16)
	_, _ = rand.Read(key)
	key64 := base64.StdEncoding.EncodeToString(key)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	h := req.Header

	h.Set("Connection", "Upgrade")
	h.Set("Upgrade", "websocket")
	h.Set("Sec-WebSocket-Version", "13")
	h.Set("Sec-WebSocket-Key", key64)

	for k, v := range c.Header {
		h[http.CanonicalHeaderKey(k)] = v
	}

	return req, nil
}

func (cl *Client) Handshake(ctx context.Context, req *http.Request) (_ *Conn, _ *http.Response, err error) {
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
		port := csel(req.URL.Scheme == "http", "80", "443")
		host = net.JoinHostPort(host, port)
	}

	c, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, nil, fmt.Errorf("dial: %w", err)
	}

	defer closerOnErr(c, &err)
	defer Stopper(ctx, c.SetDeadline)()

	err = req.Write(c)
	if err != nil {
		return nil, nil, fmt.Errorf("write request: %w", err)
	}

	buf := bufio.NewReader(c)

	resp, err := http.ReadResponse(buf, req)
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, resp, fmt.Errorf("didn't switch protocol: %v (%d)", resp.Status, resp.StatusCode)
	}

	h := resp.Header
	accept := secKeyHash(req.Header.Get("Sec-WebSocket-Key"))

	if q := h.Values("Connection"); !hasToken(q, "Upgrade") {
		return nil, resp, fmt.Errorf("didn't upgrade: %v", q)
	}
	if q := h.Get("Upgrade"); !strings.EqualFold(q, "websocket") {
		return nil, resp, fmt.Errorf("upgraded protocol mismatch: %v", q)
	}
	if q := h.Get("Sec-WebSocket-Accept"); q == "" {
		return nil, resp, errors.New("no sec-accept in response")
	} else if q != accept {
		return nil, resp, errors.New("sec-accept mismatch")
	}

	wc := &Conn{
		Conn: c,

		client: 1,
	}

	copyBuffer(wc, buf)

	return wc, resp, nil
}
