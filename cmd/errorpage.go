package cmd

import (
	"bytes"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

type ErrorPage struct {
	Code    int
	Status  string
	Message string
	Prefix  string
	T       *Locale
}

// wantsHTMLPage is true only for browser navigations. Every other client
// (curl, CI, API consumers) keeps the plain-text error body, and so do Range
// and precondition requests, which only download clients send.
func wantsHTMLPage(req *http.Request) bool {
	if req.Method != http.MethodGet {
		return false
	}
	for _, name := range []string{"Range", "If-Match", "If-Unmodified-Since"} {
		if req.Header.Get(name) != "" {
			return false
		}
	}
	for _, part := range strings.Split(req.Header.Get("Accept"), ",") {
		mediaType, params, err := mime.ParseMediaType(part)
		if err != nil || mediaType != "text/html" {
			continue
		}
		q := params["q"]
		if q == "" {
			return true
		}
		weight, err := strconv.ParseFloat(q, 64)
		return err == nil && weight > 0
	}
	return false
}

// writeError sends the themed HTML error page to browser navigations and the
// historical text body to everyone else.
func writeError(w http.ResponseWriter, req *http.Request, code int, text string) {
	if wantsHTMLPage(req) && renderErrorPage(w, req, code) {
		return
	}
	http.Error(w, text, code)
}

// renderErrorPage leaves the response headers alone until the page is rendered,
// so that a failure falls back on the text error exactly as it was emitted.
func renderErrorPage(w http.ResponseWriter, req *http.Request, code int) bool {
	t, err := template.ParseFiles(path.Join(configJson.TemplateDir, "index.tmpl"))
	if err != nil {
		return false
	}
	locale := negotiateLocale(req)
	text, ok := locale.Errors[code]
	if !ok {
		text = locale.DefaultError
	}
	var buf bytes.Buffer
	err = t.ExecuteTemplate(&buf, "error", ErrorPage{
		Code:    code,
		Status:  text.Status,
		Message: text.Message,
		Prefix:  configJson.ProxyPrefix,
		T:       locale,
	})
	if err != nil {
		log.Printf("Error rendering error page: %v\n", err)
		return false
	}
	h := w.Header()
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Add("Vary", "Accept")
	h.Add("Vary", "Accept-Language")
	w.WriteHeader(code)
	w.Write(buf.Bytes())
	return true
}

// errorPageWriter replaces the text errors emitted by http.FileServer with the
// HTML page when the client is a browser navigation.
type errorPageWriter struct {
	http.ResponseWriter
	req     *http.Request
	swapped bool
}

func (e *errorPageWriter) WriteHeader(code int) {
	if code >= 400 && !e.swapped && renderErrorPage(e.ResponseWriter, e.req, code) {
		e.swapped = true
		return
	}
	e.ResponseWriter.WriteHeader(code)
}

func (e *errorPageWriter) Write(b []byte) (int, error) {
	if e.swapped {
		return len(b), nil
	}
	return e.ResponseWriter.Write(b)
}

// ReadFrom keeps the sendfile fast path of the underlying writer for large
// downloads, which the embedded interface would otherwise hide.
func (e *errorPageWriter) ReadFrom(r io.Reader) (int64, error) {
	if e.swapped {
		return io.Copy(io.Discard, r)
	}
	return io.Copy(e.ResponseWriter, r)
}

func (e *errorPageWriter) Unwrap() http.ResponseWriter { return e.ResponseWriter }
