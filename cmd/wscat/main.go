package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"

	"nikand.dev/go/cli"
	"nikand.dev/go/hacked/hnet"
	"nikand.dev/go/websocket"
	"tlog.app/go/errors"
	"tlog.app/go/tlog"
)

func main() {
	app := &cli.Command{
		Name:   "wscat",
		Before: before,

		Commands: []*cli.Command{{
			Name:   "client,c",
			Action: clientRun,

			Flags: []*cli.Flag{
				cli.NewFlag("address,addr", "", "remote host"),
			},
		}, {
			Name:   "server,s",
			Action: serverRun,

			Flags: []*cli.Flag{
				cli.NewFlag("http", "localhost:8080", "listen http"),
			},
		}},

		Flags: []*cli.Flag{
			cli.NewFlag("debug", "", "listen debug"),
			cli.NewFlag("v", "", "verbosity"),

			cli.EnvfileFlag,
			cli.FlagfileFlag,
			cli.HelpFlag,
		},
	}

	cli.RunAndExit(app, os.Args, os.Environ())
}

func before(c *cli.Command) error {
	tlog.SetVerbosity(c.String("v"))

	if q := c.String("debug"); q != "" {
		go func() {
			err := http.ListenAndServe(q, nil)
			panic(err)
		}()
	}

	return nil
}

func clientRun(c *cli.Command) (err error) {
	ctx := context.Background()

	var wc websocket.Client

	req, err := wc.NewRequest(ctx, c.String("address"))
	if err != nil {
		return errors.Wrap(err, "new request")
	}

	conn, resp, err := wc.Handshake(ctx, req)
	if err != nil && resp != nil {
		tlog.Printw("response", "status", resp.Status, "status_code", resp.StatusCode, "err", err)
		for n, v := range resp.Header {
			tlog.Printw("resp header", "name", n, "val", v)
		}
	}
	if err != nil {
		return errors.Wrap(err, "dial")
	}

	defer closer(conn, &err, "close conn")

	orig := conn.Conn

	if tlog.If("dump") {
		conn.Conn = &Dumper{conn.Conn}
	}

	tlog.Printw("connection established")

	loc2rem := func(ctx context.Context) (err error) {
		defer DeferredCloseWriter(orig, &err, "close writer")

		b := make([]byte, 1024)

		for {
			n, err := hnet.Read(ctx, os.Stdin, b)
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return errors.Wrap(err, "read stdin")
			}

			m, err := conn.WriteFrame(b[:n], websocket.FrameText, true)
			if err != nil {
				return errors.Wrap(err, "write remote")
			}
			if m != n {
				return errors.New("write %d of %d", m, n)
			}
		}
	}

	rem2loc := func(ctx context.Context) error {
		b := make([]byte, 1024)

		for {
			op, fin, l, err := conn.ReadFrameHeader()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return errors.Wrap(err, "read header")
			}
			if l+10 > len(b) {
				b = append(b, make([]byte, l+10-cap(b))...)
				b = b[:cap(b)]
			}

			n := 0

			for {
				m, err := conn.ReadFrame(b[n:])
				n += m
				if errors.Is(err, io.EOF) {
					err = nil
					break
				}
				if err != nil {
					return errors.Wrap(err, "read frame")
				}
			}
			if n != l {
				return errors.New("read frame %d of %d", n, l)
			}

			if n == 0 || b[n-1] != '\n' {
				b[n] = '\n'
				n++
			}

			_, _ = op, fin

			os.Stdout.Write(b[:n])
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errc := make(chan error, 2)

	go func() {
		err := loc2rem(ctx)
		if err != nil {
			err = errors.Wrap(err, "loc to rem")
		}

		errc <- err
	}()

	go func() {
		err := rem2loc(ctx)
		if err != nil {
			err = errors.Wrap(err, "rem to loc")
		}

		errc <- err
	}()

	err = <-errc
	cancel()

	err2 := <-errc
	if err == nil {
		err = err2
	}

	return err
}

func serverRun(c *cli.Command) (err error) {
	l, err := net.Listen("tcp", c.String("http"))
	if err != nil {
		return errors.Wrap(err, "listen http")
	}

	defer closer(l, &err, "close listener")

	tlog.Printw("listening http", "addr", l.Addr())

	var ws websocket.Server

	err = http.Serve(l, &ws)
	if err != nil {
		return errors.Wrap(err, "serve")
	}

	return nil
}

type Dumper struct {
	net.Conn
}

func (d *Dumper) Read(p []byte) (n int, err error) {
	n, err = d.Conn.Read(p)
	tlog.Printw("read conn", "n", tlog.NextAsHex, n, "n", n, "err", err, "p", p[:n])

	return n, err
}

func (d *Dumper) Write(p []byte) (n int, err error) {
	n, err = d.Conn.Write(p)
	tlog.Printw("write conn", "n", tlog.NextAsHex, n, "n", n, "err", err, "p", p[:n])

	return n, err
}

func (d *Dumper) Close() (err error) {
	err = d.Conn.Close()
	tlog.Printw("close conn", "err", err)

	return err
}

func DeferredCloseWriter(c any, errp *error, msg string) {
	err := CloseWriter(c)
	if *errp == nil && err != nil {
		*errp = fmt.Errorf("%v: %w", msg, err)
	}
}

func CloseWriter(c any) error {
	cw, ok := c.(interface {
		CloseWrite() error
	})
	if !ok {
		return nil
	}

	return cw.CloseWrite()
}

func closer(c io.Closer, errp *error, msg string) {
	err := c.Close()
	if *errp == nil && err != nil {
		*errp = fmt.Errorf("%v: %w", msg, err)
	}
}
