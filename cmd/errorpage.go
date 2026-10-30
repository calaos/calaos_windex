package cmd

import (
	"bytes"
	"html/template"
	"io"
	"log"
	"net/http"
	"path"
	"strings"
)

type ErrorPage struct {
	Code    int
	Status  string
	Message string
	Prefix  string
}

var errorMessages = map[int]string{
	http.StatusNotFound:            "The file or folder you are looking for does not exist or is not available.",
	http.StatusForbidden:           "You are not allowed to access this resource.",
	http.StatusInternalServerError: "Something went wrong on our side. Please try again later.",
}

// wantsHTMLPage is true only for browser navigations. Every other client
// (curl, CI, API consumers) keeps the plain-text error body.
func wantsHTMLPage(req *http.Request) bool {
	return req.Method == http.MethodGet && strings.Contains(req.Header.Get("Accept"), "text/html")
}

// writeError sends the themed HTML error page to browser navigations and the
// historical text body to everyone else.
func writeError(w http.ResponseWriter, req *http.Request, code int, text string) {
	if wantsHTMLPage(req) && renderErrorPage(w, code) {
		return
	}
	http.Error(w, text, code)
}

func renderErrorPage(w http.ResponseWriter, code int) bool {
	t, err := template.ParseFiles(path.Join(configJson.TemplateDir, "index.tmpl"))
	if err != nil {
		return false
	}
	msg, ok := errorMessages[code]
	if !ok {
		msg = "The request could not be completed."
	}
	var buf bytes.Buffer
	err = t.ExecuteTemplate(&buf, "error", ErrorPage{
		Code:    code,
		Status:  http.StatusText(code),
		Message: msg,
		Prefix:  configJson.ProxyPrefix,
	})
	if err != nil {
		log.Printf("Error rendering error page: %v\n", err)
		return false
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	w.Write(buf.Bytes())
	return true
}

// errorPageWriter replaces the text errors emitted by http.FileServer with the
// HTML page when the client is a browser navigation.
type errorPageWriter struct {
	http.ResponseWriter
	swapped bool
}

func (e *errorPageWriter) WriteHeader(code int) {
	if code >= 400 && !e.swapped {
		h := e.ResponseWriter.Header()
		for _, k := range []string{"Content-Type", "Content-Length", "X-Content-Type-Options"} {
			h.Del(k)
		}
		if renderErrorPage(e.ResponseWriter, code) {
			e.swapped = true
			return
		}
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
