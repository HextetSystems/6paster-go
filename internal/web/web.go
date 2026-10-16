package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HextetSystems/6paster-go/internal/config"
	"github.com/HextetSystems/6paster-go/internal/storage"
	_ "golang.org/x/image/bmp"
)

const identifierAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

//go:embed assets/templates/*.html
var templateFiles embed.FS

type pasteStore interface {
	CreatePaste(context.Context, storage.Paste, string, storage.Limits) error
	GetPaste(context.Context, string, time.Time) (storage.Paste, error)
}

type ttlOption struct {
	Seconds  int64
	Label    string
	Selected bool
}

type Handler struct {
	cfg            config.Config
	store          pasteStore
	templates      *template.Template
	logger         *slog.Logger
	trustedProxies []*net.IPNet
	now            func() time.Time
	random         io.Reader
}

func New(cfg config.Config, store pasteStore, logger *slog.Logger) (http.Handler, error) {
	templates, err := template.ParseFS(templateFiles, "assets/templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	trustedProxies, err := parseTrustedProxies(cfg.Server.TrustedProxies)
	if err != nil {
		return nil, err
	}
	h := &Handler{cfg: cfg, store: store, templates: templates, logger: logger, trustedProxies: trustedProxies, now: time.Now, random: rand.Reader}
	return h.logging(h.recoverPanics(h)), nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	secure := isHTTPS(r)
	if secure {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	h.setSecurityHeaders(w)
	if h.cfg.Server.RequireHTTPS && !secure {
		h.renderError(w, http.StatusUpgradeRequired, "HTTPS is required for this service.")
		return
	}

	rel, ok := h.relativePath(r.URL.Path)
	if !ok {
		h.renderError(w, http.StatusNotFound, "Paste not found.")
		return
	}
	switch {
	case rel == "/" && r.Method == http.MethodGet:
		h.renderForm(w, http.StatusOK, "", "")
	case rel == "/" && r.Method == http.MethodPost:
		h.createPaste(w, r)
	case rel == "/robots.txt" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "User-agent: *\nDisallow: /\n")
	case pasteID(rel) != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.showPaste(w, r, pasteID(rel))
	case rel == "/" || rel == "/robots.txt" || pasteID(rel) != "":
		w.Header().Set("Allow", allowedMethods(rel))
		h.renderError(w, http.StatusMethodNotAllowed, "Method not allowed.")
	default:
		h.renderError(w, http.StatusNotFound, "Paste not found.")
	}
}

func (h *Handler) relativePath(requestPath string) (string, bool) {
	base := h.cfg.Server.BasePath
	if base == "/" {
		return requestPath, true
	}
	if requestPath == base || requestPath == base+"/" {
		return "/", true
	}
	if !strings.HasPrefix(requestPath, base+"/") {
		return "", false
	}
	return strings.TrimPrefix(requestPath, base), true
}

func pasteID(path string) string {
	trimmed := strings.TrimSuffix(path, "/")
	if !strings.HasPrefix(trimmed, "/p/") {
		return ""
	}
	id := strings.TrimPrefix(trimmed, "/p/")
	if len(id) != 24 {
		return ""
	}
	for _, r := range id {
		if !strings.ContainsRune(identifierAlphabet, r) {
			return ""
		}
	}
	return id
}

func allowedMethods(path string) string {
	if pasteID(path) != "" {
		return "GET, HEAD"
	}
	if path == "/robots.txt" {
		return "GET"
	}
	return "GET, POST"
}

func (h *Handler) createPaste(w http.ResponseWriter, r *http.Request) {
	content, ttlText, err := h.parseSubmission(w, r)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) || errors.Is(err, errContentTooLarge) {
			h.renderForm(w, http.StatusRequestEntityTooLarge, "Paste exceeds the configured size limit.", "")
			return
		}
		h.renderForm(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	if len(content) == 0 {
		h.renderForm(w, http.StatusBadRequest, "Paste content is required.", "")
		return
	}
	ttlSeconds, err := strconv.ParseInt(ttlText, 10, 64)
	if err != nil || ttlSeconds <= 0 {
		h.renderForm(w, http.StatusBadRequest, "TTL must be a positive number of seconds.", string(content))
		return
	}
	minimumSeconds, maximumSeconds := ttlBounds(h.cfg)
	if ttlSeconds < minimumSeconds || ttlSeconds > maximumSeconds {
		message := fmt.Sprintf("TTL must be between %d and %d seconds.", minimumSeconds, maximumSeconds)
		h.renderForm(w, http.StatusBadRequest, message, string(content))
		return
	}
	ttl := time.Duration(ttlSeconds) * time.Second
	mimeType, err := detectMIME(content, h.cfg.Paste.AllowImages)
	if err != nil {
		h.renderForm(w, http.StatusUnsupportedMediaType, err.Error(), "")
		return
	}
	now := h.now().UTC()
	ipHash := hashIP(h.clientIP(r))
	limits := storage.Limits{PerHour: h.cfg.RateLimit.PerHour, PerDay: h.cfg.RateLimit.PerDay}
	for attempts := 0; attempts < 5; attempts++ {
		id, err := generateID(h.random)
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		err = h.store.CreatePaste(r.Context(), storage.Paste{
			ID: id, CreatedAt: now, Content: content, MIMEType: mimeType, ExpiresAt: now.Add(ttl),
		}, ipHash, limits)
		if errors.Is(err, storage.ErrIDCollision) {
			continue
		}
		if errors.Is(err, storage.ErrRateLimit) {
			var limitErr *storage.RateLimitError
			if errors.As(err, &limitErr) {
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(limitErr.RetryAfter.Seconds()))))
			}
			h.renderForm(w, http.StatusTooManyRequests, "You have reached the submission limit. Try again later.", "")
			return
		}
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		http.Redirect(w, r, h.pastePath(id), http.StatusSeeOther)
		return
	}
	h.internalError(w, r, errors.New("could not allocate a unique paste identifier"))
}

var errContentTooLarge = errors.New("content too large")

func (h *Handler) parseSubmission(w http.ResponseWriter, r *http.Request) ([]byte, string, error) {
	limit := h.cfg.Paste.MaximumBytes
	requestLimit := limit*4 + 1<<20
	r.Body = http.MaxBytesReader(w, r.Body, requestLimit)
	if err := r.ParseMultipartForm(limit + 64<<10); err != nil {
		if !strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err := r.ParseForm(); err != nil {
				return nil, "", fmt.Errorf("could not parse submission: %w", err)
			}
		} else {
			return nil, "", fmt.Errorf("could not parse submission: %w", err)
		}
	}
	content := []byte(r.FormValue("content"))
	if int64(len(content)) > limit {
		return nil, "", errContentTooLarge
	}
	return content, r.FormValue("ttl"), nil
}

func (h *Handler) showPaste(w http.ResponseWriter, r *http.Request, id string) {
	p, err := h.store.GetPaste(r.Context(), id, h.now().UTC())
	if errors.Is(err, storage.ErrNotFound) {
		h.renderError(w, http.StatusNotFound, "Paste not found.")
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if p.MIMEType == "text/plain" { // Compatibility with the original schema.
		p.MIMEType = "text/plain; charset=utf-8"
	}
	if strings.ContainsAny(p.MIMEType, "\r\n") {
		h.internalError(w, r, errors.New("invalid stored MIME type"))
		return
	}
	if !h.cfg.Paste.AllowImages && strings.HasPrefix(p.MIMEType, "image/") {
		h.renderError(w, http.StatusNotFound, "Paste not found.")
		return
	}
	if p.MIMEType != "text/plain; charset=utf-8" && !strings.HasPrefix(p.MIMEType, "image/") {
		h.internalError(w, r, errors.New("invalid stored MIME type"))
		return
	}
	w.Header().Set("Content-Type", p.MIMEType)
	w.Header().Set("Content-Length", strconv.Itoa(len(p.Content)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodHead {
		_, _ = w.Write(p.Content)
	}
}

func (h *Handler) renderForm(w http.ResponseWriter, status int, message, content string) {
	data := struct {
		Title, Action, PublicURL, Error, Content string
		MaximumKiB                               int64
		TTLs                                     []ttlOption
	}{
		Title: h.cfg.Server.PageTitle, Action: h.homePath(), PublicURL: h.cfg.PublicBaseURL(), Error: message, Content: content,
		MaximumKiB: h.cfg.Paste.MaximumBytes / 1024,
		TTLs:       configuredTTLs(h.cfg),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.templates.ExecuteTemplate(w, "form", data); err != nil {
		h.logger.Error("render form", "error", err)
	}
}

func ttlBounds(cfg config.Config) (int64, int64) {
	minimum := int64(cfg.Paste.MinimumTTL.Duration / time.Second)
	if cfg.Paste.MinimumTTL.Duration%time.Second != 0 {
		minimum++
	}
	maximum := int64(cfg.Paste.MaximumTTL.Duration / time.Second)
	return minimum, maximum
}

func configuredTTLs(cfg config.Config) []ttlOption {
	minimum, maximum := ttlBounds(cfg)
	defaultSeconds := int64((24 * time.Hour) / time.Second)
	if defaultSeconds < minimum {
		defaultSeconds = minimum
	}
	if defaultSeconds > maximum {
		defaultSeconds = maximum
	}

	candidates := []int64{minimum, 3600, 14400, 86400, 604800, 2592000, defaultSeconds, maximum}
	slices.Sort(candidates)
	candidates = slices.Compact(candidates)
	options := make([]ttlOption, 0, len(candidates))
	for _, seconds := range candidates {
		if seconds < minimum || seconds > maximum {
			continue
		}
		options = append(options, ttlOption{Seconds: seconds, Label: ttlLabel(seconds), Selected: seconds == defaultSeconds})
	}
	return options
}

func ttlLabel(seconds int64) string {
	switch seconds {
	case 604800:
		return "1 week"
	}
	if seconds%86400 == 0 {
		return pluralLabel(seconds/86400, "day")
	}
	if seconds%3600 == 0 {
		return pluralLabel(seconds/3600, "hour")
	}
	if seconds%60 == 0 {
		return pluralLabel(seconds/60, "minute")
	}
	return pluralLabel(seconds, "second")
}

func pluralLabel(value int64, unit string) string {
	if value != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s", value, unit)
}

func (h *Handler) renderError(w http.ResponseWriter, status int, message string) {
	data := struct{ Title, Status, Message, Home string }{h.cfg.Server.PageTitle, http.StatusText(status), message, h.homePath()}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.templates.ExecuteTemplate(w, "error", data); err != nil {
		h.logger.Error("render error", "error", err)
	}
}

func (h *Handler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	h.renderError(w, http.StatusInternalServerError, "The request could not be completed.")
}

func (h *Handler) setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
}

func (h *Handler) homePath() string {
	if h.cfg.Server.BasePath == "/" {
		return "/"
	}
	return h.cfg.Server.BasePath + "/"
}

func (h *Handler) pastePath(id string) string {
	if h.cfg.Server.BasePath == "/" {
		return "/p/" + id
	}
	return h.cfg.Server.BasePath + "/p/" + id
}

func generateID(source io.Reader) (string, error) {
	result := make([]byte, 24)
	buf := make([]byte, 32)
	for i := 0; i < len(result); {
		if _, err := io.ReadFull(source, buf); err != nil {
			return "", fmt.Errorf("read random data: %w", err)
		}
		for _, b := range buf {
			if b >= 248 { // 248 is the largest multiple of 62 below 256.
				continue
			}
			result[i] = identifierAlphabet[int(b)%len(identifierAlphabet)]
			i++
			if i == len(result) {
				break
			}
		}
	}
	return string(result), nil
}

func detectMIME(content []byte, allowImages bool) (string, error) {
	if utf8.Valid(content) {
		return "text/plain; charset=utf-8", nil
	}
	if !allowImages {
		return "", errors.New("Only UTF-8 text is accepted.")
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return "", errors.New("Only UTF-8 text and GIF, JPEG, PNG, or BMP images are accepted.")
	}
	switch format {
	case "gif":
		return "image/gif", nil
	case "jpeg":
		return "image/jpeg", nil
	case "png":
		return "image/png", nil
	case "bmp":
		return "image/bmp", nil
	default:
		return "", errors.New("Only UTF-8 text and GIF, JPEG, PNG, or BMP images are accepted.")
	}
}

func parseTrustedProxies(cidrs []string) ([]*net.IPNet, error) {
	result := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("parse trusted proxy CIDR %q: %w", cidr, err)
		}
		result = append(result, network)
	}
	return result, nil
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(r.RemoteAddr)
}

func (h *Handler) isTrustedProxy(ip net.IP) bool {
	for _, network := range h.trustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func (h *Handler) clientIP(r *http.Request) string {
	peer := remoteIP(r)
	if peer == nil {
		return "unknown"
	}
	if !h.isTrustedProxy(peer) {
		return peer.String()
	}

	// X-Real-IP is authoritative when a trusted edge proxy replaces it.
	if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
		return ip.String()
	}

	// In an appended X-Forwarded-For chain, discard trusted hops from the
	// right and use the first untrusted address as the client.
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(forwarded) - 1; i >= 0; i-- {
		item := strings.TrimSpace(forwarded[i])
		if item == "" {
			continue
		}
		ip := net.ParseIP(item)
		if ip == nil {
			return peer.String()
		}
		if !h.isTrustedProxy(ip) {
			return ip.String()
		}
	}
	return peer.String()
}

func hashIP(ip string) string {
	sum := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(sum[:])
}

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	for _, proto := range strings.Split(r.Header.Get("X-Forwarded-Proto"), ",") {
		return strings.EqualFold(strings.TrimSpace(proto), "https")
	}
	return false
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(p)
}

func (h *Handler) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		h.logger.Info("request", "method", r.Method, "path", r.URL.Path, "status", status, "duration", time.Since(started))
	})
}

func (h *Handler) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				h.logger.Error("request panic", "method", r.Method, "path", r.URL.Path, "panic", recovered, "stack", string(debug.Stack()))
				h.renderError(w, http.StatusInternalServerError, "The request could not be completed.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
