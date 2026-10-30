package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	return root, fileHandler(http.FileServer(newSafeFileSystem(root)))
}

func get(h http.Handler, target, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
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
	for _, banned := range []string{"googleapis", "gstatic", "jquery", "<script"} {
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
	body := get(h, "/", browser).Body.String()
	for _, want := range []string{
		`href="/download/static/style.css"`, `src="/download/static/icons/deb.png"`,
		`data-website-id="test-id"`, `src="https://stats.example/script.js"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	// Only Umami may be external; the rest of the page references no third party.
	for _, u := range []string{"https://calaos.fr/", "https://doc.calaos.fr/en/"} {
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

func TestTemplateMissingFallsBackToText(t *testing.T) {
	_, h := setup(t, func(c *Config) { c.TemplateDir = t.TempDir() })
	rec := get(h, "/nope", browser)
	if rec.Code != 404 || rec.Body.String() != "404 Not Found\n" {
		t.Errorf("fallback: %d %q", rec.Code, rec.Body.String())
	}
}
