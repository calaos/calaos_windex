package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestNegotiateLocale(t *testing.T) {
	cases := []struct{ header, want string }{
		{"fr", "fr"},
		{"fr-FR", "fr"},
		{"FR", "fr"},
		{"fr-CA,fr;q=0.9,en;q=0.8", "fr"},
		{"en-US,fr;q=0.8", "en"},
		{"en;q=0.5,fr;q=0.9", "fr"},
		{"de", "en"},
		{"de,fr;q=0.5", "fr"},
		{"fr;q=0", "en"},
		{"", "en"},
		{"*", "en"},
		{";;;garbage", "en"},
	}
	for _, c := range cases {
		t.Run(c.header, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Accept-Language", c.header)
			if got := negotiateLocale(req).Lang; got != c.want {
				t.Errorf("got %q", got)
			}
		})
	}
}

func page(h http.Handler, target, lang string) *httptest.ResponseRecorder {
	return request(h, http.MethodGet, target, map[string]string{"Accept": browser, "Accept-Language": lang})
}

var hrefs = regexp.MustCompile(`<a [^>]*href="([^"]+)"`)

func hrefsIn(t *testing.T, body, start, end string) []string {
	t.Helper()
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("no %q", start)
	}
	j := strings.Index(body[i:], end)
	if j < 0 {
		t.Fatalf("no %q after %q", end, start)
	}
	var out []string
	for _, m := range hrefs.FindAllStringSubmatch(body[i:i+j], -1) {
		out = append(out, m[1])
	}
	return out
}

func TestMenuFollowsLanguage(t *testing.T) {
	want := map[string][]string{
		"fr": {
			"https://calaos.fr/fr/#comment", "https://calaos.fr/fr/#quotidien", "https://calaos.fr/fr/#interfaces",
			"https://calaos.fr/fr/#compat", "https://calaos.fr/fr/#communaute", "https://doc.calaos.fr",
			"https://github.com/calaos", "https://calaos.fr/download/",
		},
		"en": {
			"https://calaos.fr/en/#how-it-works", "https://calaos.fr/en/#day-to-day", "https://calaos.fr/en/#interfaces",
			"https://calaos.fr/en/#compatibility", "https://calaos.fr/en/#community", "https://doc.calaos.fr/en/",
			"https://github.com/calaos", "https://calaos.fr/download/",
		},
	}
	root, h := setup(t, nil)
	write(t, filepath.Join(root, "f.bin"), "x")
	for lang, links := range want {
		for _, target := range []string{"/", "/nope"} {
			body := page(h, target, lang).Body.String()
			if !strings.Contains(body, `<html lang="`+lang+`">`) {
				t.Errorf("%s %s: html lang", lang, target)
			}
			for _, zone := range [][2]string{
				{`<nav class="menu"`, `</nav>`},
				{`<nav class="drawer"`, `</nav>`},
			} {
				got := hrefsIn(t, body, zone[0], zone[1])
				if strings.Join(got, " ") != strings.Join(links, " ") {
					t.Errorf("%s %s %s: %v", lang, target, zone[0], got)
				}
			}
			if !strings.Contains(body, `href="https://calaos.fr/`+lang+`/#top"`) {
				t.Errorf("%s %s: logo link", lang, target)
			}
			current := `hreflang="` + lang + `" lang="` + lang + `" aria-current="true"`
			if !strings.Contains(body, current) || strings.Count(body, `aria-current="true"`) != 1 {
				t.Errorf("%s %s: language selector", lang, target)
			}
		}
	}
}

func TestPagesFollowLanguage(t *testing.T) {
	root, h := setup(t, nil)
	write(t, filepath.Join(root, "sub", "f.bin"), "x")
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		target, lang string
		want         []string
	}{
		{"/sub/", "fr", []string{"Dossier parent", "Nom", "Modifié", "Taille", "Racine", "Téléchargements"}},
		{"/sub/", "en", []string{"Parent directory", "Name", "Last modified", "Size", "Root", "Downloads"}},
		{"/empty/", "fr", []string{"Ce dossier est vide", "Retour au dossier parent"}},
		{"/empty/", "en", []string{"This folder is empty", "Back to parent folder"}},
		{"/nope", "fr", []string{"Introuvable", "n’existe pas", "Retour aux téléchargements"}},
		{"/nope", "en", []string{"Not Found", "does not exist", "Back to downloads"}},
		{"/nope", "de", []string{"Not Found", "does not exist", "Back to downloads"}},
	}
	for _, c := range cases {
		body := page(h, c.target, c.lang).Body.String()
		for _, want := range c.want {
			if !strings.Contains(body, want) {
				t.Errorf("%s (%s): missing %q", c.target, c.lang, want)
			}
		}
	}
}

func TestAgesAndSizesFollowLanguage(t *testing.T) {
	root, h := setup(t, nil)
	file := filepath.Join(root, "sub", "f.bin")
	write(t, file, "abcde")
	hour := time.Now().Add(-90 * time.Minute)
	if err := os.Chtimes(file, hour, hour); err != nil {
		t.Fatal(err)
	}
	for lang, want := range map[string][]string{
		"fr": {"il y a 1 heure", "5 o"},
		"en": {"1 hour ago", "5 B"},
	} {
		body := page(h, "/sub/", lang).Body.String()
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: missing %q", lang, w)
			}
		}
	}

	ages := map[time.Duration]string{
		0:                     "à l’instant",
		30 * time.Second:      "il y a 30 secondes",
		3 * time.Hour:         "il y a 3 heures",
		50 * 24 * time.Hour:   "il y a 1 mois",
		400 * 24 * time.Hour:  "il y a 1 an",
		1200 * 24 * time.Hour: "il y a 3 ans",
		-3 * time.Hour:        "dans 2 heures",
	}
	for d, want := range ages {
		if got := ageFr(time.Now().Add(-d)); got != want {
			t.Errorf("ageFr(%v) = %q, want %q", d, got, want)
		}
	}

	sizes := map[uint64]string{
		999:         "999 o",
		5000:        "5,0 ko",
		12_400_000:  "12 Mo",
		1500000000:  "1,5 Go",
		10000000000: "10 Go",
	}
	for n, want := range sizes {
		if got := sizeFr(n); got != want {
			t.Errorf("sizeFr(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestVary(t *testing.T) {
	root, h := setup(t, nil)
	write(t, filepath.Join(root, "f.bin"), "x")
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		target string
		accept string
		want   []string
	}{
		{"listing", "/", browser, []string{"Accept-Language"}},
		{"listing for curl", "/", "*/*", []string{"Accept-Language"}},
		{"empty", "/empty/", browser, []string{"Accept-Language"}},
		{"html error", "/nope", browser, []string{"Accept", "Accept-Language"}},
		{"text error", "/nope", "*/*", nil},
		{"download", "/f.bin", browser, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := get(h, c.target, c.accept).Header().Values("Vary")
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("Vary %v", got)
			}
		})
	}
}

// Text answers must not depend on the language of the client.
func TestTextAnswersIgnoreLanguage(t *testing.T) {
	root, h := setup(t, nil)
	write(t, filepath.Join(root, "f.bin"), "payload")
	for _, target := range []string{"/nope", "/f.bin", "/api"} {
		en := request(h, http.MethodGet, target, map[string]string{"Accept": "*/*"})
		fr := request(h, http.MethodGet, target, map[string]string{"Accept": "*/*", "Accept-Language": "fr"})
		if en.Code != fr.Code || en.Body.String() != fr.Body.String() {
			t.Errorf("%s: %d %q / %d %q", target, en.Code, en.Body.String(), fr.Code, fr.Body.String())
		}
	}
}

func TestLocalesHaveTheSameShape(t *testing.T) {
	if len(localeEn.Menu) != len(localeFr.Menu) || len(localeEn.Errors) != len(localeFr.Errors) {
		t.Fatal("locales differ")
	}
	for i, en := range localeEn.Menu {
		fr := localeFr.Menu[i]
		if en.Button != fr.Button || en.Icon != fr.Icon {
			t.Errorf("menu entry %d differs", i)
		}
	}
	for code := range localeEn.Errors {
		if _, ok := localeFr.Errors[code]; !ok {
			t.Errorf("no French text for %d", code)
		}
	}
}
