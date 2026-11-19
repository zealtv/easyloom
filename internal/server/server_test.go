package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is an output buffer a test can read while the server writes to it
// from the browser-launch goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newTest binds a server and closes it if the test never serves it. A test
// that supplies no opener gets no browser, so running the suite never opens
// one for real.
func newTest(t *testing.T, opts Options) *Server {
	t.Helper()
	if opts.Open == nil {
		opts.NoOpen = true
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// serve runs the server in the background and returns a cancel that waits for
// it to stop, so a test never leaks a listener into the next one.
func serve(t *testing.T, srv *Server) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after cancellation")
		}
	}
	t.Cleanup(stop)
	return stop
}

func TestNewBindsLoopbackOnAnEphemeralPort(t *testing.T) {
	srv := newTest(t, Options{})

	addr, ok := srv.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("Addr is %T, want *net.TCPAddr", srv.Addr())
	}
	if !addr.IP.IsLoopback() {
		t.Errorf("bound %s, want a loopback address", addr.IP)
	}
	if addr.Port == 0 {
		t.Error("bound port 0; the OS port must be resolved before Serve")
	}
	if want := fmt.Sprintf("http://127.0.0.1:%d", addr.Port); srv.URL() != want {
		t.Errorf("URL() = %q, want %q", srv.URL(), want)
	}
}

// Two looms open at once is the case --port 0 exists for.
func TestTwoServersBindWithoutCollision(t *testing.T) {
	first := newTest(t, Options{})
	second := newTest(t, Options{})
	if first.URL() == second.URL() {
		t.Fatalf("both servers bound %s", first.URL())
	}
}

func TestNewRejectsAnImpossiblePort(t *testing.T) {
	if _, err := New(Options{Port: 70000, NoOpen: true}); err == nil {
		t.Fatal("New(70000) succeeded, want an error")
	}
}

func TestServePrintsTheRealURL(t *testing.T) {
	var out syncBuffer
	srv := newTest(t, Options{Output: &out})
	stop := serve(t, srv)
	stop()

	if !strings.Contains(out.String(), srv.URL()) {
		t.Errorf("startup output %q does not name %s", out.String(), srv.URL())
	}
}

func TestServeServesTheHandler(t *testing.T) {
	srv := newTest(t, Options{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from ", r.URL.Path)
	})})
	serve(t, srv)

	body, status := get(t, srv.URL()+"/frontier")
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	if want := "hello from /frontier"; body != want {
		t.Errorf("body %q, want %q", body, want)
	}
}

func TestServeWithoutAHandlerSaysThereAreNoViews(t *testing.T) {
	srv := newTest(t, Options{})
	serve(t, srv)

	body, status := get(t, srv.URL()+"/")
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	if !strings.Contains(body, "no views yet") {
		t.Errorf("placeholder body %q does not explain itself", body)
	}
}

// Cancelling the context is what SIGINT does. The server must stop accepting
// and Serve must return without an error.
func TestCancellationStopsTheServer(t *testing.T) {
	srv := newTest(t, Options{})
	stop := serve(t, srv)

	if _, status := get(t, srv.URL()+"/"); status != http.StatusOK {
		t.Fatalf("status %d before shutdown, want 200", status)
	}
	stop()

	if _, err := http.Get(srv.URL() + "/"); err == nil {
		t.Error("the server still answers after shutdown")
	}
}

func TestServeOpensABrowserOnTheServedURL(t *testing.T) {
	opened := make(chan string, 1)
	srv := newTest(t, Options{Open: func(url string) error {
		opened <- url
		return nil
	}})
	serve(t, srv)

	select {
	case url := <-opened:
		if url != srv.URL() {
			t.Errorf("opened %q, want %q", url, srv.URL())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no browser was opened")
	}
}

func TestNoOpenSuppressesTheBrowser(t *testing.T) {
	opened := make(chan string, 1)
	srv := newTest(t, Options{NoOpen: true, Open: func(url string) error {
		opened <- url
		return nil
	}})
	stop := serve(t, srv)
	stop()

	select {
	case url := <-opened:
		t.Errorf("--no-open still opened %q", url)
	default:
	}
}

// A machine with no browser is a normal state of the world: it is reported,
// and the server keeps serving.
func TestAFailedBrowserLaunchIsNotFatal(t *testing.T) {
	var out syncBuffer
	srv := newTest(t, Options{
		Output: &out,
		Open:   func(string) error { return errors.New("no browser here") },
	})
	serve(t, srv)

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "no browser here") {
		if time.Now().After(deadline) {
			t.Fatalf("output %q never reported the failed launch", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, status := get(t, srv.URL()+"/"); status != http.StatusOK {
		t.Errorf("status %d, want 200: the server must survive a failed launch", status)
	}
}

func get(t *testing.T, url string) (string, int) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(body), resp.StatusCode
}
