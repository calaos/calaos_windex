package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var registerRootOnce sync.Once

// The real root handler lives on DefaultServeMux, which refuses a second
// registration: this one follows the configuration of the running test.
func registerRoot() {
	registerRootOnce.Do(func() {
		http.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.FileServer(newSafeFileSystem(configJson.RootFolder)).ServeHTTP(w, r)
		}))
	})
}

func setup(t *testing.T, mutate func(c *Config)) (root string, h http.Handler) {
	t.Helper()
	root = t.TempDir()
	tpl, err := filepath.Abs("../html")
	if err != nil {
		t.Fatal(err)
	}
	old := configJson
	t.Cleanup(func() { configJson = old })
	configJson = Config{RootFolder: root, TemplateDir: tpl}
	if mutate != nil {
		mutate(&configJson)
	}
	staticHandler = http.StripPrefix("/static/",
		staticCacheHeaders(http.FileServer(newSafeFileSystem(tpl))))
	downloadEvent = func(*http.Request, string) {}
	registerRoot()
	return root, buildHttpHandler()
}

func request(h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func get(h http.Handler, target, accept string) *httptest.ResponseRecorder {
	headers := map[string]string{}
	if accept != "" {
		headers["Accept"] = accept
	}
	return request(h, http.MethodGet, target, headers)
}

const browser = "text/html,application/xhtml+xml,*/*;q=0.8"

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListing(t *testing.T) {
	root, h := setup(t, nil)
	long := strings.Repeat("a1b2c3d4", 20) + ".tar.xz"
	write(t, filepath.Join(root, "sub", "x.txt"), "x")
	write(t, filepath.Join(root, "calaos-os.img.xz"), "data")
	write(t, filepath.Join(root, long), "data")
	write(t, filepath.Join(root, "a<b>&c.zip"), "data")

	rec := get(h, "/", browser)
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	for _, want := range []string{"calaos-os.img.xz", long, "a&lt;b&gt;&amp;c.zip", "sub/", `aria-current="page"`, "/static/style.css"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, "a<b>") {
		t.Error("unescaped file name")
	}
	if strings.Contains(body, "Parent directory") {
		t.Error("parent link shown at root")
	}
	if strings.Contains(body, "This folder is empty") {
		t.Error("empty state on non-empty folder")
	}
	for _, banned := range []string{"googleapis", "gstatic", "jquery", "data-website-id"} {
		if strings.Contains(body, banned) {
			t.Errorf("unexpected %q (no Umami configured)", banned)
		}
	}
	if strings.Index(body, "sub/") > strings.Index(body, "calaos-os.img.xz") {
		t.Error("folders must come before files")
	}

	sub := get(h, "/sub/", browser).Body.String()
	if !strings.Contains(sub, "Parent directory") {
		t.Error("no parent link in subfolder")
	}
}

func TestEmptyFolder(t *testing.T) {
	root, h := setup(t, nil)
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := get(h, "/empty/", browser).Body.String()
	if !strings.Contains(body, "This folder is empty") || !strings.Contains(body, `href="../"`) {
		t.Errorf("empty state missing:\n%s", body)
	}
	if strings.Contains(body, `class="list"`) {
		t.Error("list rendered for an empty folder")
	}
}

func TestPrefixAndUmami(t *testing.T) {
	root, h := setup(t, func(c *Config) {
		c.ProxyPrefix = "download"
		c.UmamiWebsiteId = "test-id"
		c.UmamiScriptURL = "https://stats.example/script.js"
	})
	write(t, filepath.Join(root, "f.deb"), "x")
	body := get(h, "/download/", browser).Body.String()
	for _, want := range []string{
		`href="/download/static/style.css"`, `src="/download/static/icons/deb.png"`,
		`src="/download/static/menu.js"`,
		`data-website-id="test-id"`, `src="https://stats.example/script.js"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	// Only Umami loads from elsewhere; the menu links stay plain navigations.
	for _, u := range []string{"https://calaos.fr/", "https://doc.calaos.fr", "https://github.com/calaos", "http://www.w3.org/2000/svg"} {
		body = strings.ReplaceAll(body, u, "")
	}
	body = strings.ReplaceAll(body, "https://stats.example/script.js", "")
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") || strings.Contains(body, "//fonts") {
		t.Error("third-party reference outside Umami")
	}
}

func TestErrors(t *testing.T) {
	_, h := setup(t, nil)

	rec := get(h, "/nope", browser)
	if rec.Code != 404 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(rec.Body.String(), "Not Found") {
		t.Errorf("html 404: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	for _, accept := range []string{"", "*/*", "application/json"} {
		rec = get(h, "/nope", accept)
		if rec.Code != 404 || rec.Body.String() != "404 Not Found\n" ||
			!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
			t.Errorf("text 404 (Accept %q): %d %q", accept, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodHead, "/nope", nil)
	req.Header.Set("Accept", browser)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Error("HEAD must keep the text error")
	}
}

func TestDotfileErrorFromFileServer(t *testing.T) {
	root, h := setup(t, nil)
	write(t, filepath.Join(root, ".secret"), "x")
	if rec := get(h, "/.secret", browser); rec.Code != 404 || !strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("html expected for browser: %d", rec.Code)
	}
	if rec := get(h, "/.secret", ""); rec.Code != 404 || strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("text expected for other clients: %d", rec.Code)
	}
}

func TestDownloadUnchanged(t *testing.T) {
	root, h := setup(t, nil)
	write(t, filepath.Join(root, "f.bin"), "payload")
	for _, accept := range []string{"", browser} {
		rec := get(h, "/f.bin", accept)
		if rec.Code != 200 || rec.Body.String() != "payload" {
			t.Errorf("download (Accept %q): %d %q", accept, rec.Code, rec.Body.String())
		}
	}
}

func TestDownloadFiresOneEvent(t *testing.T) {
	root, h := setup(t, nil)
	write(t, filepath.Join(root, "f.bin"), "payload")
	events := make(chan string, 2)
	downloadEvent = func(_ *http.Request, name string) { events <- name }
	get(h, "/f.bin", browser)
	if name := <-events; name != "f.bin" {
		t.Errorf("event for %q", name)
	}
	get(h, "/", browser)
	if len(events) != 0 {
		t.Error("event for a listing")
	}
}

func TestTemplateMissingFallsBackToText(t *testing.T) {
	root, h := setup(t, func(c *Config) { c.TemplateDir = t.TempDir() })
	write(t, filepath.Join(root, ".secret"), "x")
	// /nope goes through writeError, /.secret through the FileServer wrapper.
	for target, text := range map[string]string{"/nope": "404 Not Found\n", "/.secret": "404 page not found\n"} {
		rec := get(h, target, browser)
		if rec.Code != 404 || rec.Body.String() != text {
			t.Errorf("%s fallback: %d %q", target, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s fallback lost nosniff: %q", target, got)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
			t.Errorf("%s fallback type: %q", target, got)
		}
	}
}

func TestWantsHTMLPage(t *testing.T) {
	cases := []struct {
		name   string
		method string
		accept string
		rng    string
		want   bool
	}{
		{"browser", "GET", browser, "", true},
		{"html only", "GET", "text/html", "", true},
		{"weighted", "GET", "text/html;q=0.5,*/*;q=0.1", "", true},
		{"spaces and case", "GET", "application/json, TEXT/HTML ; q=0.9", "", true},
		{"refused", "GET", "text/html;q=0", "", false},
		{"refused with decimals", "GET", "text/html;q=0.000", "", false},
		{"bad weight", "GET", "text/html;q=abc", "", false},
		{"wildcard", "GET", "*/*", "", false},
		{"no accept", "GET", "", "", false},
		{"json", "GET", "application/json", "", false},
		{"xhtml only", "GET", "application/xhtml+xml", "", false},
		{"range", "GET", browser, "bytes=0-1", false},
		{"head", "HEAD", browser, "", false},
		{"post", "POST", browser, "", false},
		{"put", "PUT", browser, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, "/", nil)
			req.Header.Set("Accept", c.accept)
			req.Header.Set("Range", c.rng)
			if got := wantsHTMLPage(req); got != c.want {
				t.Errorf("wantsHTMLPage = %v", got)
			}
		})
	}
}

func TestRangeKeepsTextError(t *testing.T) {
	root, h := setup(t, nil)
	write(t, filepath.Join(root, "f.bin"), "payload")
	rec := request(h, http.MethodGet, "/f.bin", map[string]string{"Accept": browser, "Range": "bytes=100-200"})
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("code %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") || rec.Header().Get("Content-Range") == "" {
		t.Errorf("416 changed: %q %q", rec.Header().Get("Content-Type"), rec.Header().Get("Content-Range"))
	}
	rec = request(h, http.MethodGet, "/f.bin", map[string]string{"Accept": browser, "Range": "bytes=1-3"})
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "ayl" {
		t.Errorf("206: %d %q", rec.Code, rec.Body.String())
	}
}

func TestPreconditionKeepsEmptyResponse(t *testing.T) {
	root, h := setup(t, nil)
	write(t, filepath.Join(root, "f.bin"), "payload")
	for name, value := range map[string]string{
		"If-Unmodified-Since": "Mon, 01 Jan 2001 00:00:00 GMT",
		"If-Match":            `"nope"`,
	} {
		rec := request(h, http.MethodGet, "/f.bin", map[string]string{"Accept": browser, name: value})
		if rec.Code != http.StatusPreconditionFailed {
			t.Fatalf("%s: code %d", name, rec.Code)
		}
		if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
			t.Errorf("%s: 412 changed: %q %q", name, rec.Header().Get("Content-Type"), rec.Body.String())
		}
	}
}

func TestErrorPageHasContentLength(t *testing.T) {
	_, h := setup(t, nil)
	rec := get(h, "/nope", browser)
	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(rec.Body.Len()); got != want {
		t.Errorf("Content-Length %q, body %s", got, want)
	}
}

func TestAPIAndUploadIgnoreBrowserAccept(t *testing.T) {
	_, h := setup(t, nil)
	var bodies []string
	for _, accept := range []string{"", "*/*", browser} {
		rec := get(h, "/api", accept)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" || strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("/api (Accept %q): %d %q", accept, rec.Code, rec.Header().Get("Content-Type"))
		}
		if rec.Header().Get("Vary") != "" {
			t.Errorf("/api got Vary %q", rec.Header().Get("Vary"))
		}
		bodies = append(bodies, rec.Body.String())
	}
	if bodies[0] != bodies[1] || bodies[0] != bodies[2] {
		t.Error("/api depends on Accept")
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		rec := request(h, method, "/upload", map[string]string{"Accept": browser, "Accept-Language": "fr"})
		if rec.Code < 400 || strings.Contains(rec.Body.String(), "<html") || rec.Header().Get("Vary") != "" {
			t.Errorf("%s /upload: %d %q", method, rec.Code, rec.Body.String())
		}
	}
}
