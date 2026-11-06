// Command starwavatars serves Star Wars avatars: GET /avatars/<id>.svg or
// .png draws one of fourteen icons in colours picked from the md5 of <id>.
//
// On CARLOS the platform starts it as
//
//	starwavatars --socket <path> --db <path>
//
// and proxies HTTPS to the unix socket. --db is accepted and ignored: the
// service keeps no state. For a local run, use --addr 127.0.0.1:8080
// instead of --socket.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/teamtito/starwavatars/internal/web"
)

// build is the commit this binary was built from, stamped with
// -ldflags "-X main.build=<sha>". /up reports it.
var build = "dev"

func main() {
	socket := flag.String("socket", "", "unix socket to serve HTTP on (how CARLOS runs it)")
	addr := flag.String("addr", "", "loopback or specific address to serve on instead, such as 127.0.0.1:8080")
	flag.String("db", "", "database path; accepted and ignored, the service has no state")
	flag.Parse()

	l, err := listen(*socket, *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "starwavatars:", err)
		os.Exit(2)
	}
	// ReadTimeout covers a request whose body never arrives: the server
	// waits to drain it before answering, and nothing else times that out
	// (Astra review, 2026-11-06).
	srv := &http.Server{
		Handler:           web.Handler(build),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	log.Printf("starwavatars %s serving on %s", build, l.Addr())
	go func() { errc <- srv.Serve(l) }()

	// Serve returns the moment the listener closes, so main waits on
	// Shutdown, not on Serve: requests in flight get their five seconds.
	select {
	case err := <-errc:
		log.Fatal(err)
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// listen opens exactly one of the socket or the address.
func listen(socket, addr string) (net.Listener, error) {
	switch {
	case socket != "" && addr != "":
		return nil, errors.New("pass --socket or --addr, not both")
	case socket != "":
		if err := clearStaleSocket(socket); err != nil {
			return nil, err
		}
		return net.Listen("unix", socket)
	case addr != "":
		ap, err := checkAddr(addr)
		if err != nil {
			return nil, err
		}
		return net.Listen("tcp", ap.String())
	}
	return nil, errors.New("usage: starwavatars --socket <path> [--db <path>] | --addr <ip:port>")
}

// checkAddr accepts only a literal IP and port that name one interface:
// never ":8080", 0.0.0.0, ::, or an address with a zone, any of which would
// put the service on networks it has no business on.
func checkAddr(addr string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return ap, fmt.Errorf("--addr %q: want a literal ip:port such as 127.0.0.1:8080", addr)
	}
	ip := ap.Addr()
	switch {
	case ip.Unmap().IsUnspecified(): // ::ffff:0.0.0.0 is 0.0.0.0 too
		return ap, fmt.Errorf("--addr %q listens on every interface; name one, such as 127.0.0.1", addr)
	case ip.Zone() != "":
		return ap, fmt.Errorf("--addr %q has a zone; use an address without one", addr)
	case ap.Port() == 0:
		return ap, fmt.Errorf("--addr %q: pick a port", addr)
	}
	return ap, nil
}

// clearStaleSocket removes a socket left behind by a previous run (an app
// that hibernates leaves its socket file in place), but only when it is
// certainly stale: it is a socket and nothing answers on it. Anything else at
// the path is someone else's, and it is not ours to delete.
func clearStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode().Type() != os.ModeSocket {
		return fmt.Errorf("--socket %s exists and is not a socket; refusing to remove it", path)
	}
	// Only a refused dial proves nobody is there. A timeout can be a live
	// server with a full backlog, and a permission error says nothing, so
	// both are left alone (Astra review, 2026-11-06).
	c, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		c.Close()
		return fmt.Errorf("--socket %s is in use by a running server", path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("--socket %s exists and may be in use (%v); refusing to remove it", path, err)
	}
	return os.Remove(path)
}
