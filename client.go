package websocket

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
)

type (
	Client struct {
		Header http.Header
		Client http.Client
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

func (c *Client) Handshake(ctx context.Context, req *http.Request) (*Conn, *http.Response, error) {
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("do request: %w", err)
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, resp, fmt.Errorf("didn't switch protocol: %v (%d)", resp.Status, resp.StatusCode)
	}

	h := resp.Header
	accept := secKeyHash(req.Header.Get("Sec-WebSocket-Key"))

	if q := h.Get("Connection"); q != "Upgrade" {
		return nil, resp, fmt.Errorf("didn't upgrade: %v", q)
	}
	if q := h.Get("Upgrade"); q != "websocket" {
		return nil, resp, fmt.Errorf("upgraded protocol mismatch: %v", q)
	}
	if q := h.Get("Sec-WebSocket-Accept"); q == "" {
		return nil, resp, fmt.Errorf("no sec-accept in response")
	} else if q != accept {
		return nil, resp, fmt.Errorf("sec-accept mismatch")
	}

	rwc, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		return nil, resp, fmt.Errorf("body type is not usable: %T", resp.Body)
	}

	conn := &Conn{
		rwc: rwc,

		client: 1,
	}

	return conn, resp, nil
}
