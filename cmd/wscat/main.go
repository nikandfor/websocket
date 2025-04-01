package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"

	"nikand.dev/go/cli"
	"nikand.dev/go/websocket"
	"tlog.app/go/errors"
	"tlog.app/go/tlog"
)

func main() {
	app := &cli.Command{
		Name: "wscat",

		Commands: []*cli.Command{{
			Name:   "client,c",
			Action: clientRun,

			Args: cli.Args{},
		}, {
			Name:   "server,s",
			Action: serverRun,

			Flags: []*cli.Flag{
				cli.NewFlag("http", "localhost:8080", "listen http"),
			},
		}},

		Flags: []*cli.Flag{
			cli.EnvfileFlag,
			cli.FlagfileFlag,
			cli.HelpFlag,
		},
	}

	cli.RunAndExit(app, os.Args, os.Environ())
}

func clientRun(c *cli.Command) (err error) {
	if c.Args.Len() != 1 {
		return errors.New("one argument expected")
	}

	ctx := context.Background()

	var wc websocket.Client

	conn, err := wc.DialContext(ctx, c.Args.First())
	if err != nil {
		return errors.Wrap(err, "dial")
	}

	defer closer(conn, &err, "close conn")

	tlog.Printw("connection established")

	return biproxy(ctx, os.Stdin, conn)
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

func biproxy(ctx context.Context, c, r io.ReadWriteCloser) error {
	errc := make(chan error, 2)

	proxy := func(name string, w, r io.ReadWriteCloser) {
		_, err := io.Copy(w, r)

		if c, ok := w.(interface {
			CloseWrite() error
		}); ok {
			e := c.CloseWrite()
			if err == nil && e != nil {
				err = fmt.Errorf("close writer: %w", e)
			}
		}
		if err != nil {
			err = fmt.Errorf("%v: %w", name, err)
		}

		errc <- err
	}

	go proxy("client-to-remote", r, c)
	go proxy("remote-to-client", c, r)

	err := <-errc
	e := <-errc
	if err == nil {
		err = e
	}

	return err
}

func closer(c io.Closer, errp *error, msg string) {
	err := c.Close()
	if *errp == nil && err != nil {
		*errp = fmt.Errorf("%v: %w", msg, err)
	}
}
