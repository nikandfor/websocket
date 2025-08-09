[![Documentation](https://pkg.go.dev/badge/nikand.dev/go/websocket)](https://pkg.go.dev/nikand.dev/go/websocket?tab=doc)
[![Go workflow](https://github.com/nikandfor/websocket/actions/workflows/go.yml/badge.svg)](https://github.com/nikandfor/websocket/actions/workflows/go.yml)
[![CircleCI](https://circleci.com/gh/nikandfor/websocket.svg?style=svg)](https://circleci.com/gh/nikandfor/websocket)
[![codecov](https://codecov.io/gh/nikandfor/websocket/branch/master/graph/badge.svg)](https://codecov.io/gh/nikandfor/websocket)
[![Go Report Card](https://goreportcard.com/badge/nikand.dev/go/websocket)](https://goreportcard.com/report/nikand.dev/go/websocket)
![GitHub tag (latest SemVer)](https://img.shields.io/github/v/tag/nikandfor/websocket?sort=semver)

# websocket

Lightweight efficient websocket library.

## Usage

### Server

```go
func server() error {
    s := websocket.Server{
        Handler: func(ctx context.Context, c *websocket.Conn) error {
            _, err := io.Copy(c, c)
            return err
        },
    }

    return http.ListenAndServe(":80", &s)
}

// or
func handler(w http.ResponseWriter, req *http.Request) error {
    var s websocket.Server

    // play with req: check auth, handle hacky case, etc...

    _, err := s.ServeHandler(w, req, func(ctx context.Context, c *websocket.Conn) error {
        _, err := io.Copy(c, c)
        return err
    })

    return err
}

// or
func withoutCallback(w http.ResponseWriter, req *http.Request) error {
    var s websocket.Server

    // play with req: check auth, handle hacky case, etc...

    c, err := s.Handshake(ctx, w, req)
    if err != nil {
        return errors.Wrap(err, "handshake")
    }

    defer c.Close()

    // all the following is connection handler essentially

    _, err = io.Copy(c, c)

    return err
}
```

### Client

```go
func dial() (*websocket.Conn, error){
    var cl websocket.Client

    return cl.DialContext(ctx, "ws://some-host/some/path?and=params")
}

// or
func manual() (*websocket.Conn, error){
    var cl websocket.Client

    req, err := cl.NewRequest(ctx, "ws://some-host/some/path?and=params")
    // if err != nil ...

    // set req.Header or something

    c, resp, err := cl.Handshake(ctx, req)
    // if err != nil ...

    // check resp.Header or something

    return c, nil
}
```
