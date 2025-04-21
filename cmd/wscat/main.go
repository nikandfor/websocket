package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	"nikand.dev/go/cli"
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
				cli.NewFlag("listen,l", "localhost:8080", "listen http"),
				cli.NewFlag("handler,h", "echo", "echo|ticker"),
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

	if tlog.If("dump") {
		conn.Conn = &Dumper{conn.Conn}
	}

	tlog.Printw("connection established")

	loc2rem := func(ctx context.Context) (err error) {
		defer func() {
			e := conn.CloseWriter(websocket.StatusOK)
			if err == nil && e != nil {
				err = errors.Wrap(e, "close writer")
			}
		}()

		b := make([]byte, 32)

		for {
			//	n, err := hnet.Read(ctx, os.Stdin, b)
			n, err := os.Stdin.Read(b)
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return errors.Wrap(err, "read stdin")
			}

			for n != 0 && b[n-1] == '\n' {
				n--
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
		var b []byte

		for {
			op, fin, l, err := conn.ReadFrameHeader()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return errors.Wrap(err, "read header")
			}

			b, err = conn.AppendReadFrame(b[:0])
			if err != nil && !errors.Is(err, io.EOF) {
				return errors.Wrap(err, "read frame")
			}

			if len(b) != l {
				panic(len(b))
			}

			if len(b) != 0 && b[len(b)-1] != '\n' {
				b = append(b, '\n')
			}

			_, _ = op, fin

			os.Stdout.Write(b)
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
	l, err := net.Listen("tcp", c.String("listen"))
	if err != nil {
		return errors.Wrap(err, "listen")
	}

	defer closer(l, &err, "close listener")

	tlog.Printw("listening", "addr", l.Addr())

	var ws websocket.Server
	var h websocket.Handler

	switch c.String("handler") {
	case "echo":
		h = Echo
	case "ticker":
		h = Ticker
	}

	ws.Handler = func(ctx context.Context, c *websocket.Conn) (err error) {
		tr := tlog.SpanFromContext(ctx).Or(tlog.Root()).Spawn("connection", "raddr", c.Conn.RemoteAddr())
		defer tr.Finish("err", &err)

		ctx = tlog.ContextWithSpan(ctx, tr)

		if tlog.If("dump") {
			c.Conn = &Dumper{c.Conn}
		}

		return h(ctx, c)
	}

	err = http.Serve(l, &ws)
	if err != nil {
		return errors.Wrap(err, "serve")
	}

	return nil
}

func Echo(ctx context.Context, c *websocket.Conn) error {
	buf := make([]byte, 10)

	for {
		op, fin, l, err := c.ReadFrameHeader()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read frame header: %w", err)
		}

		tlog.Printw("frame header", "op", op, "fin", fin, "l", l)

		total := 0

		for total < l {
			n, err := c.ReadFrame(buf)
			if n != 0 {
				tlog.Printw("message", "op", op, "fin", fin, "i", total, "end", total+n, "of", l, "data", buf[:n])

				_, err = c.WriteFrame(buf[:n], op, fin && total+n == l)
				if err != nil {
					return fmt.Errorf("write: %w", err)
				}

				op = websocket.FrameContinue
				total += n
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("read: %w", err)
			}
		}
	}
}

func Ticker(ctx context.Context, c *websocket.Conn) error {
	t := time.NewTicker(time.Second)
	defer t.Stop()

	for {
		var now time.Time

		select {
		case now = <-t.C:
		case <-ctx.Done():
			return nil
		}

		tlog.Printw("tick", "now", now)

		_, err := fmt.Fprintf(c, "tick %v", now)
		if err != nil {
			return fmt.Errorf("write: %w", err)
		}
	}
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

func closer(c io.Closer, errp *error, msg string) {
	err := c.Close()
	if *errp == nil && err != nil {
		*errp = fmt.Errorf("%v: %w", msg, err)
	}
}
