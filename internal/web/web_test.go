package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HextetSystems/6paster-go/internal/config"
	"github.com/HextetSystems/6paster-go/internal/storage"
	"golang.org/x/image/bmp"
)

type fakeStore struct {
	created storage.Paste
	ipHash  string
	paste   storage.Paste
	err     error
	panic   any
	creates int
}

func (f *fakeStore) CreatePaste(_ context.Context, p storage.Paste, ipHash string, _ storage.Limits) error {
	f.creates++
	f.created = p
	f.ipHash = ipHash
	return f.err
}

func (f *fakeStore) GetPaste(_ context.Context, _ string, _ time.Time) (storage.Paste, error) {
	if f.panic != nil {
		panic(f.panic)
	}
	if f.err != nil {
		return storage.Paste{}, f.err
	}
	return f.paste, nil
}

func TestHandlerRecoversFromPanic(t *testing.T) {
	store := &fakeStore{panic: "unexpected failure"}
	h := newTestHandler(t, store)

	r := httptest.NewRequest(http.MethodGet, "/tools/paste/p/abcdefghijklmnopqrstuvwx", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "The request could not be completed.") {
		t.Fatalf("panic response: status=%d body=%q", w.Code, w.Body.String())
	}

	// A later request is still handled normally.
	r = httptest.NewRequest(http.MethodGet, "/outside", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("request after panic: status=%d body=%q", w.Code, w.Body.String())
	}
}

func testConfig() config.Config {
	return config.Config{
		Server:    config.ServerConfig{Listen: ":8080", PublicURL: "https://paste.example", BasePath: "/tools/paste", PageTitle: "Test Paster"},
		Paste:     config.PasteConfig{MinimumTTL: config.Duration{Duration: time.Hour}, MaximumTTL: config.Duration{Duration: 30 * 24 * time.Hour}, MaximumBytes: 300 * 1024, AllowImages: true},
		RateLimit: config.RateLimitConfig{PerHour: 60, PerDay: 100},
	}
}

func newTestHandler(t *testing.T, store *fakeStore) http.Handler {
	t.Helper()
	h, err := New(testConfig(), store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestFormAndBasePath(t *testing.T) {
	h := newTestHandler(t, &fakeStore{})
	for _, path := range []string{"/tools/paste", "/tools/paste/"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `action="/tools/paste/"`) || !strings.Contains(w.Body.String(), "Test Paster") {
			t.Fatalf("GET %s: status=%d body=%s", path, w.Code, w.Body.String())
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("GET %s Cache-Control=%q, want no-store", path, got)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/other", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("outside base path status = %d", w.Code)
	}
}

func TestHealthEndpoint(t *testing.T) {
	cfg := testConfig()
	cfg.Server.RequireHTTPS = true
	h, err := New(cfg, &fakeStore{panic: "health endpoint touched store"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := httptest.NewRequest(method, "/health", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("Content-Length") != "3" {
			t.Fatalf("%s /health: status=%d headers=%v body=%q", method, w.Code, w.Header(), w.Body.String())
		}
		if method == http.MethodGet && w.Body.String() != "ok\n" {
			t.Fatalf("GET /health body=%q", w.Body.String())
		}
		if method == http.MethodHead && w.Body.Len() != 0 {
			t.Fatalf("HEAD /health body=%q", w.Body.String())
		}
	}

	r := httptest.NewRequest(http.MethodPost, "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST /health: status=%d Allow=%q", w.Code, w.Header().Get("Allow"))
	}
}

func TestRoutingStatusesAndRobots(t *testing.T) {
	h := newTestHandler(t, &fakeStore{})
	tests := []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/tools/paste/robots.txt", http.StatusOK},
		{http.MethodDelete, "/tools/paste/", http.StatusMethodNotAllowed},
		{http.MethodPost, "/tools/paste/p/abcdefghijklmnopqrstuvwx", http.StatusMethodNotAllowed},
		{http.MethodGet, "/tools/paste/p/short", http.StatusNotFound},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(tt.method, tt.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tt.status {
			t.Errorf("%s %s status=%d, want %d", tt.method, tt.path, w.Code, tt.status)
		}
	}
}

func TestURLSubmissionAndRawRetrieval(t *testing.T) {
	store := &fakeStore{}
	h := newTestHandler(t, store)
	form := url.Values{"content": {"hello <world>"}, "ttl": {"3600"}}
	r := httptest.NewRequest(http.MethodPost, "/tools/paste/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Forwarded-For", "203.0.113.10")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/tools/paste/p/") {
		t.Fatalf("submission status=%d location=%q body=%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if string(store.created.Content) != "hello <world>" || store.created.MIMEType != "text/plain; charset=utf-8" {
		t.Fatalf("created paste = %+v", store.created)
	}
	if len(store.ipHash) != sha256.Size*2 {
		t.Fatalf("rate-limit IP hash length = %d", len(store.ipHash))
	}
	if got := store.created.ExpiresAt.Sub(store.created.CreatedAt); got != time.Hour {
		t.Fatalf("minimum TTL clamp = %s", got)
	}

	store.paste = store.created
	path := w.Header().Get("Location") + "?smart"
	r = httptest.NewRequest(http.MethodGet, path, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "hello <world>" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("retrieval status=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
	}

	r = httptest.NewRequest(http.MethodHead, path, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "13" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HEAD status=%d length=%q body=%q", w.Code, w.Header().Get("Content-Length"), w.Body.String())
	}
}

func TestMultipartCurlSubmission(t *testing.T) {
	store := &fakeStore{}
	h := newTestHandler(t, store)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("content", "from curl"); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("ttl", "604800"); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/tools/paste/", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || string(store.created.Content) != "from curl" {
		t.Fatalf("multipart status=%d paste=%+v body=%s", w.Code, store.created, w.Body.String())
	}
}

func TestSubmissionErrors(t *testing.T) {
	tests := []struct {
		name   string
		values url.Values
		status int
	}{
		{"empty", url.Values{"content": {""}, "ttl": {"3600"}}, http.StatusBadRequest},
		{"bad ttl", url.Values{"content": {"hello"}, "ttl": {"later"}}, http.StatusBadRequest},
		{"binary", url.Values{"content": {string([]byte{0xff, 0xfe, 0xfd})}, "ttl": {"3600"}}, http.StatusUnsupportedMediaType},
		{"large", url.Values{"content": {strings.Repeat("x", 300*1024+1)}, "ttl": {"3600"}}, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(t, &fakeStore{})
			r := httptest.NewRequest(http.MethodPost, "/tools/paste/", strings.NewReader(tt.values.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tt.status, w.Body.String())
			}
		})
	}
}

func TestOutOfRangeTTLRejected(t *testing.T) {
	for _, ttl := range []string{"1", "9223372036854775807"} {
		t.Run(ttl, func(t *testing.T) {
			h := newTestHandler(t, &fakeStore{})
			form := url.Values{"content": {"hello"}, "ttl": {ttl}}
			r := httptest.NewRequest(http.MethodPost, "/tools/paste/", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "TTL must be between 3600 and 2592000 seconds.") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestConfiguredTTLsStayWithinRange(t *testing.T) {
	cfg := testConfig()
	cfg.Paste.MinimumTTL = config.Duration{Duration: 2 * time.Hour}
	cfg.Paste.MaximumTTL = config.Duration{Duration: 6 * time.Hour}
	options := configuredTTLs(cfg)
	want := []ttlOption{
		{Seconds: 7200, Label: "2 hours"},
		{Seconds: 14400, Label: "4 hours"},
		{Seconds: 21600, Label: "6 hours", Selected: true},
	}
	if !reflect.DeepEqual(options, want) {
		t.Fatalf("configuredTTLs()=%+v, want %+v", options, want)
	}
}

func TestLegacyTextMIMEGetsUTF8Charset(t *testing.T) {
	store := &fakeStore{paste: storage.Paste{Content: []byte("legacy"), MIMEType: "text/plain"}}
	h := newTestHandler(t, store)
	r := httptest.NewRequest(http.MethodGet, "/tools/paste/p/abcdefghijklmnopqrstuvwx", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("status=%d content-type=%q", w.Code, w.Header().Get("Content-Type"))
	}
}

func TestRateLimitResponse(t *testing.T) {
	store := &fakeStore{err: &storage.RateLimitError{RetryAfter: 90 * time.Second}}
	h := newTestHandler(t, store)
	form := url.Values{"content": {"hello"}, "ttl": {"3600"}}
	r := httptest.NewRequest(http.MethodPost, "/tools/paste/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "90" {
		t.Fatalf("status=%d Retry-After=%q", w.Code, w.Header().Get("Retry-After"))
	}
}

func TestHTTPSPolicy(t *testing.T) {
	cfg := testConfig()
	cfg.Server.RequireHTTPS = true
	h, err := New(cfg, &fakeStore{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/tools/paste/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUpgradeRequired {
		t.Fatalf("plain HTTP status=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/tools/paste/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Strict-Transport-Security") == "" {
		t.Fatalf("forwarded HTTPS status=%d HSTS=%q", w.Code, w.Header().Get("Strict-Transport-Security"))
	}
}

func TestClientIPTrustBoundary(t *testing.T) {
	proxies, err := parseTrustedProxies([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{trustedProxies: proxies}
	tests := []struct {
		name       string
		remoteAddr string
		realIP     string
		forwarded  string
		want       string
	}{
		{"direct spoof ignored", "192.0.2.1:1234", "203.0.113.1", "203.0.113.2", "192.0.2.1"},
		{"trusted proxy real IP", "10.0.0.2:1234", "203.0.113.1", "198.51.100.1", "203.0.113.1"},
		{"trusted proxy appended chain", "10.0.0.2:1234", "", "203.0.113.2, 10.0.0.1", "203.0.113.2"},
		{"malformed chain fails closed", "10.0.0.2:1234", "", "203.0.113.2, bad", "10.0.0.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remoteAddr
			r.Header.Set("X-Real-IP", tt.realIP)
			r.Header.Set("X-Forwarded-For", tt.forwarded)
			if got := h.clientIP(r); got != tt.want {
				t.Fatalf("clientIP()=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseTrustedProxiesRejectsInvalidCIDR(t *testing.T) {
	if _, err := parseTrustedProxies([]string{"127.0.0.1"}); err == nil {
		t.Fatal("expected invalid CIDR error")
	}
}

func TestGenerateIDAndFailure(t *testing.T) {
	id, err := generateID(bytes.NewReader(bytes.Repeat([]byte{1}, 32)))
	if err != nil || len(id) != 24 || strings.Trim(id, identifierAlphabet) != "" {
		t.Fatalf("generateID()=%q, %v", id, err)
	}
	if _, err := generateID(errorReader{}); err == nil {
		t.Fatal("expected random source error")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("random failed") }

type testImageEncoder struct {
	mime   string
	encode func(io.Writer, image.Image) error
}

func testImageEncoders() []testImageEncoder {
	return []testImageEncoder{
		{"image/png", png.Encode},
		{"image/jpeg", func(w io.Writer, i image.Image) error { return jpeg.Encode(w, i, nil) }},
		{"image/gif", func(w io.Writer, i image.Image) error { return gif.Encode(w, i, nil) }},
		{"image/bmp", bmp.Encode},
	}
}

func encodedTestImage(t *testing.T, encode func(io.Writer, image.Image) error) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.White)
	var b bytes.Buffer
	if err := encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDetectMIMETextAndUnsupportedBinary(t *testing.T) {
	for _, allowImages := range []bool{true, false} {
		got, err := detectMIME([]byte("plain text"), allowImages)
		if err != nil || got != "text/plain; charset=utf-8" {
			t.Fatalf("allowImages=%t text detectMIME()=%q, %v", allowImages, got, err)
		}
		if _, err := detectMIME([]byte{0xff, 0xfe, 0xfd}, allowImages); err == nil {
			t.Fatalf("allowImages=%t unsupported binary was accepted", allowImages)
		}
	}
}

func TestDetectMIMEImages(t *testing.T) {
	for _, tt := range testImageEncoders() {
		t.Run(tt.mime, func(t *testing.T) {
			content := encodedTestImage(t, tt.encode)
			got, err := detectMIME(content, true)
			if err != nil || got != tt.mime {
				t.Fatalf("detectMIME()=%q, %v", got, err)
			}
			if _, err := detectMIME(content, false); err == nil || !strings.Contains(err.Error(), "Only UTF-8 text") {
				t.Fatalf("text-only detectMIME() error = %v", err)
			}
		})
	}
}

func TestImageSubmissionPolicy(t *testing.T) {
	for _, tt := range testImageEncoders() {
		t.Run(tt.mime, func(t *testing.T) {
			content := encodedTestImage(t, tt.encode)
			for _, allowImages := range []bool{true, false} {
				name := "disabled"
				wantStatus := http.StatusUnsupportedMediaType
				if allowImages {
					name = "enabled"
					wantStatus = http.StatusSeeOther
				}
				t.Run(name, func(t *testing.T) {
					cfg := testConfig()
					cfg.Paste.AllowImages = allowImages
					store := &fakeStore{}
					h, err := New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
					if err != nil {
						t.Fatal(err)
					}
					form := url.Values{"content": {string(content)}, "ttl": {"3600"}}
					r := httptest.NewRequest(http.MethodPost, "/tools/paste/", strings.NewReader(form.Encode()))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != wantStatus {
						t.Fatalf("status=%d want=%d body=%s", w.Code, wantStatus, w.Body.String())
					}
					if allowImages {
						if store.creates != 1 || store.created.MIMEType != tt.mime {
							t.Fatalf("creates=%d paste=%+v", store.creates, store.created)
						}
					} else if store.creates != 0 || !strings.Contains(w.Body.String(), "Only UTF-8 text is accepted.") {
						t.Fatalf("creates=%d body=%s", store.creates, w.Body.String())
					}
				})
			}
		})
	}
}

func TestImageRetrievalPolicy(t *testing.T) {
	const path = "/tools/paste/p/abcdefghijklmnopqrstuvwx"
	store := &fakeStore{paste: storage.Paste{Content: []byte("image"), MIMEType: "image/png"}}
	cfg := testConfig()
	cfg.Paste.AllowImages = false
	h, err := New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := httptest.NewRequest(method, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s image status=%d body=%q", method, w.Code, w.Body.String())
		}
	}

	store.paste = storage.Paste{Content: []byte("text"), MIMEType: "text/plain; charset=utf-8"}
	r := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "text" {
		t.Fatalf("text status=%d body=%q", w.Code, w.Body.String())
	}

	cfg.Paste.AllowImages = true
	h, err = New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	store.paste = storage.Paste{Content: []byte("image"), MIMEType: "image/png"}
	r = httptest.NewRequest(http.MethodGet, path, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("enabled image status=%d content-type=%q", w.Code, w.Header().Get("Content-Type"))
	}
}
