package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/zealtv/easyloom/internal/desktop"
)

// shutdownGrace bounds how long a stopped server waits for in-flight requests
// before closing them. This is a local inspector: nothing it serves is worth
// making Ctrl-C feel broken.
const shutdownGrace = 2 * time.Second

// loopback is the only interface EasyLoom binds. The server exposes a
// repository's plan and the ability to mutate it; it is not an application
// anyone should be able to reach from another machine.
const loopback = "127.0.0.1"

// Options configure one run of the web shell.
type Options struct {
	// Port is the TCP port to bind on the loopback interface. Zero — the
	// default — takes a free port from the OS, so two looms open at once do
	// not fight over a fixed one.
	Port int
	// Path is appended to the base URL when the server announces itself and
	// opens a browser, so what a person lands on is a real view rather than a
	// redirect they then have to trust.
	Path string
	// Handler serves every request. A nil handler serves a placeholder page,
	// which is what the shell is until the views exist.
	Handler http.Handler
	// Output receives the startup line naming the real URL. A port the OS
	// chose is unusable until it is printed.
	Output io.Writer
	// NoOpen suppresses launching a browser.
	NoOpen bool
	// Open launches a browser on the URL. Nil uses the platform opener. It
	// runs in the background: a failure to open a browser is reported, never
	// fatal, because the URL has already been printed and works.
	Open func(url string) error
}

// Server is the bound web shell. Binding happens in New rather than in Serve,
// so the caller has the real URL before anything is served on it.
type Server struct {
	listener net.Listener
	http     *http.Server
	url      string
	start    string
	opts     Options
}

// New binds the loopback listener and prepares the server. It does not serve
// anything until Serve is called.
func New(opts Options) (*Server, error) {
	if opts.Port < 0 || opts.Port > 65535 {
		return nil, fmt.Errorf("serve: port %d is not a port", opts.Port)
	}
	handler := opts.Handler
	if handler == nil {
		handler = placeholder()
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", loopback, opts.Port))
	if err != nil {
		return nil, fmt.Errorf("serve: %w", err)
	}

	url := "http://" + listener.Addr().String()
	return &Server{
		listener: listener,
		http:     &http.Server{Handler: handler},
		url:      url,
		start:    url + opts.Path,
		opts:     opts,
	}, nil
}

// URL is where the server is listening, with the port the OS actually gave.
func (s *Server) URL() string { return s.url }

// StartURL is the URL the server announces and opens: the base URL and the
// starting path, if there is one.
func (s *Server) StartURL() string { return s.start }

// Addr is the bound address.
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Serve runs until ctx is cancelled — SIGINT, in the usual case — then stops
// accepting, gives in-flight requests a moment, and returns. A cancelled
// context is a clean stop, not an error.
//
// The caller closes whatever the handler reads from. Cancelling a snapshot
// cache kills the map subprocess in flight, so the deferred close in the
// caller is what stops loom itself.
func (s *Server) Serve(ctx context.Context) error {
	if s.opts.Output != nil {
		fmt.Fprintf(s.opts.Output, "\nserving  %s\n", s.start)
	}
	s.launchBrowser()

	errs := make(chan error, 1)
	go func() {
		err := s.http.Serve(s.listener)
		// A shutdown closes the listener, which is how Serve always ends here.
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errs <- err
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	if s.opts.Output != nil {
		fmt.Fprintln(s.opts.Output, "stopping")
	}
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := s.http.Shutdown(shutdown); err != nil {
		// The grace period expired with requests still open. Drop them: the
		// process is on its way out either way.
		_ = s.http.Close()
	}
	<-errs
	return nil
}

// Close stops the server without waiting for a context. It is for callers that
// bound a server and then failed before serving it.
func (s *Server) Close() error { return s.listener.Close() }

// launchBrowser opens the URL unless told not to. It is deliberately
// fire-and-forget: an opener that blocks — or a machine with no browser at all
// — must not delay or fail the server that is already listening.
func (s *Server) launchBrowser() {
	if s.opts.NoOpen {
		return
	}
	open := s.opts.Open
	if open == nil {
		open = desktop.Open
	}
	url := s.start
	go func() {
		if err := open(url); err != nil && s.opts.Output != nil {
			fmt.Fprintf(s.opts.Output, "warning: could not open a browser: %v\n", err)
		}
	}()
}

// placeholder is what the shell serves before it has any views. It says the
// server is real and the UI is not, which is more useful than a 404 from an
// empty mux.
func placeholder() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "easyloom is serving, but the web shell has no views yet.")
	})
}
