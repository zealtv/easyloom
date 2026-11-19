package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// routed serves a registry with two looms and records what each request
// resolved to, which is the whole contract of routing.
func routed(t *testing.T) (http.Handler, *Registry, *[]Request) {
	t.Helper()
	registry := NewRegistry()
	registry.Add(stub("/home/someone/easyloom"))
	registry.Add(stub("/home/someone/bopos"))

	var seen []Request
	handler := NewRouter(RouterOptions{Registry: registry, View: func(w http.ResponseWriter, _ *http.Request, resolved Request) {
		seen = append(seen, resolved)
		w.WriteHeader(http.StatusOK)
	}})
	return handler, registry, &seen
}

func request(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestRoutesCarryTheLoomAndTheView(t *testing.T) {
	handler, _, seen := routed(t)

	for _, tc := range []struct {
		path   string
		slug   string
		view   View
		stitch string
	}{
		{"/l/easyloom/frontier", "easyloom", ViewFrontier, ""},
		{"/l/bopos/frontier", "bopos", ViewFrontier, ""},
		{"/l/easyloom/threads", "easyloom", ViewThreads, ""},
		{"/l/easyloom/queue", "easyloom", ViewQueue, ""},
		{"/l/easyloom/graph", "easyloom", ViewGraph, ""},
		{"/l/easyloom/impact", "easyloom", ViewImpact, ""},
		{"/l/bopos/stitch/http-server", "bopos", ViewStitch, "http-server"},
	} {
		if rec := request(t, handler, tc.path); rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, want 200", tc.path, rec.Code)
		}
		resolved := (*seen)[len(*seen)-1]
		if resolved.Entry.Slug != tc.slug || resolved.View != tc.view || resolved.StitchID != tc.stitch {
			t.Errorf("GET %s resolved to %s/%s/%s, want %s/%s/%s",
				tc.path, resolved.Entry.Slug, resolved.View, resolved.StitchID,
				tc.slug, tc.view, tc.stitch)
		}
	}
}

// The same URL resolves to the same place every time. That is what makes a
// reload land where it left off, and what a bookmark relies on.
func TestTheSameURLAlwaysResolvesTheSame(t *testing.T) {
	handler, _, seen := routed(t)

	request(t, handler, "/l/bopos/stitch/http-server")
	request(t, handler, "/l/easyloom/frontier")
	request(t, handler, "/l/bopos/stitch/http-server")

	first, last := (*seen)[0], (*seen)[2]
	if first.Entry != last.Entry || first.StitchID != last.StitchID || first.View != last.View {
		t.Error("one URL resolved two ways: the server is holding a current loom")
	}
}

func TestRootRedirectsToThePrimaryFrontier(t *testing.T) {
	handler, registry, _ := routed(t)
	primary, _ := registry.Primary()

	rec := request(t, handler, "/")
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", rec.Code)
	}
	if want := FrontierPath(primary.Slug); rec.Header().Get("Location") != want {
		t.Errorf("redirected to %q, want %q", rec.Header().Get("Location"), want)
	}
}

func TestALoomWithoutAViewRedirectsToItsFrontier(t *testing.T) {
	handler, _, _ := routed(t)

	rec := request(t, handler, "/l/bopos/")
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", rec.Code)
	}
	if want := FrontierPath("bopos"); rec.Header().Get("Location") != want {
		t.Errorf("redirected to %q, want %q", rec.Header().Get("Location"), want)
	}
}

// A stale bookmark should say what happened rather than render an empty page.
func TestAnUnknownSlugIs404AndNamesWhatExists(t *testing.T) {
	handler, _, _ := routed(t)

	rec := request(t, handler, "/l/gone/frontier")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"gone", "easyloom", "bopos"} {
		if !strings.Contains(body, want) {
			t.Errorf("404 body %q does not mention %q", body, want)
		}
	}
}

func TestAnUnknownViewIs404(t *testing.T) {
	handler, _, _ := routed(t)
	if rec := request(t, handler, "/l/easyloom/timeline"); rec.Code != http.StatusNotFound {
		t.Errorf("status %d for an unknown view, want 404", rec.Code)
	}
}

// The stitch view needs a stitch. The bare view is not a place.
func TestAStitchViewWithoutAnIDIs404(t *testing.T) {
	handler, _, _ := routed(t)
	if rec := request(t, handler, "/l/easyloom/stitch/"); rec.Code != http.StatusNotFound {
		t.Errorf("status %d for a stitch view with no stitch, want 404", rec.Code)
	}
}

// The default view says what the URL resolved to, which is all routing can
// honestly show before the views exist.
func TestTheDefaultViewDescribesTheResolvedURL(t *testing.T) {
	registry := NewRegistry()
	registry.Add(stub("/home/someone/easyloom"))
	handler := NewRouter(RouterOptions{Registry: registry})

	rec := request(t, handler, "/l/easyloom/stitch/http-server")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	for _, want := range []string{"easyloom", "stitch", "http-server"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body %q does not mention %q", rec.Body.String(), want)
		}
	}
}

func TestPathHelpers(t *testing.T) {
	if got := FrontierPath("bopos"); got != "/l/bopos/frontier" {
		t.Errorf("FrontierPath = %q", got)
	}
	if got := StitchPath("bopos", "http-server"); got != "/l/bopos/stitch/http-server" {
		t.Errorf("StitchPath = %q", got)
	}
}
