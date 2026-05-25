package reader

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// gzipMiddleware transparently compresses responses for clients that
// advertise `Accept-Encoding: gzip`. The big win is the JSON payload
// from stream/contents and stream/items/contents — HTML article bodies
// compress 5-10× and dominate Reeder's sync wire time.
//
// Responses that already carry a Content-Encoding (e.g. handler
// pre-encoded) pass through untouched, as do clients that omitted gzip
// from Accept-Encoding.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !clientAcceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		gz := gzipPool.Get().(*gzip.Writer)
		defer gzipPool.Put(gz)
		gz.Reset(w)
		gw := &gzipResponseWriter{ResponseWriter: w, gz: gz}
		defer gw.Close()
		next.ServeHTTP(gw, r)
	})
}

func clientAcceptsGzip(r *http.Request) bool {
	for _, v := range r.Header.Values("Accept-Encoding") {
		for _, tok := range strings.Split(v, ",") {
			name := strings.TrimSpace(tok)
			// Strip any q=... weight; we don't honour explicit q=0.
			if i := strings.IndexByte(name, ';'); i >= 0 {
				name = strings.TrimSpace(name[:i])
			}
			if strings.EqualFold(name, "gzip") {
				return true
			}
		}
	}
	return false
}

var gzipPool = sync.Pool{
	New: func() any { return gzip.NewWriter(io.Discard) },
}

// gzipResponseWriter wraps an http.ResponseWriter so writes are routed
// through a gzip.Writer. The first Write (or explicit WriteHeader)
// commits the Content-Encoding header and strips any Content-Length the
// handler may have set (it no longer matches the wire payload).
type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	headerDone  bool
	passthrough bool
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	g.commitHeader()
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	g.commitHeader()
	if g.passthrough {
		return g.ResponseWriter.Write(p)
	}
	return g.gz.Write(p)
}

func (g *gzipResponseWriter) commitHeader() {
	if g.headerDone {
		return
	}
	g.headerDone = true
	h := g.Header()
	// If the handler already set Content-Encoding (e.g. served a
	// pre-encoded body), bypass our gzip layer entirely.
	if h.Get("Content-Encoding") != "" {
		g.passthrough = true
		return
	}
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	h.Del("Content-Length")
}

func (g *gzipResponseWriter) Close() error {
	g.commitHeader()
	if g.passthrough {
		return nil
	}
	return g.gz.Close()
}

// Flush exposes the underlying flusher so streaming handlers continue
// to work; gzip framing is flushed first so bytes actually reach the
// client.
func (g *gzipResponseWriter) Flush() {
	g.commitHeader()
	if !g.passthrough {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
