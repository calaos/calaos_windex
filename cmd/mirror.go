package cmd

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// validateMirrorConfig checks the mirror-related fields of Config at startup.
func validateMirrorConfig(cfg *Config) error {
	if cfg.MirrorBaseURL == "" {
		return nil // feature disabled, nothing to check
	}
	u, err := url.Parse(cfg.MirrorBaseURL)
	if err != nil {
		return fmt.Errorf("mirror_base_url %q is not a valid URL: %w", cfg.MirrorBaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("mirror_base_url scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("mirror_base_url must contain a host")
	}
	if len(cfg.PrimaryHosts) == 0 {
		return fmt.Errorf("primary_hosts must list at least one hostname when mirror_base_url is set")
	}
	code := cfg.MirrorRedirectCode
	if code != 0 && (code < 301 || code > 308) {
		return fmt.Errorf("mirror_redirect_code must be 301–308, got %d", code)
	}
	return nil
}

// hostOnly returns the hostname from a host[:port] string.
func hostOnly(hostport string) string {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport // no port present
	}
	return h
}

// shouldMirrorRedirect returns true when the request should be transparently
// redirected to the mirror. Conditions:
//  1. mirror_base_url is configured
//  2. The request Host is in primary_hosts (prevents loops on the mirror itself)
//  3. Method is GET or HEAD
//  4. Path is not a static asset, upload, or API route
//  5. statinfo is a regular file
//  6. File size >= mirror_min_bytes (0 = always)
func shouldMirrorRedirect(req *http.Request, statinfo os.FileInfo) bool {
	if configJson.MirrorBaseURL == "" {
		return false
	}

	reqHost := strings.ToLower(hostOnly(req.Host))
	matched := false
	for _, h := range configJson.PrimaryHosts {
		if strings.EqualFold(h, reqHost) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}

	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}

	p := req.URL.Path
	if strings.HasPrefix(p, "/static/") ||
		strings.HasPrefix(p, "/static") ||
		strings.HasPrefix(p, "/upload/") ||
		strings.HasPrefix(p, "/api/") {
		return false
	}

	if statinfo.IsDir() {
		return false
	}

	if configJson.MirrorMinBytes > 0 && statinfo.Size() < configJson.MirrorMinBytes {
		return false
	}

	return true
}

// buildMirrorURL constructs the full redirect URL on the mirror.
func buildMirrorURL(req *http.Request) string {
	base := strings.TrimRight(configJson.MirrorBaseURL, "/")
	target := base + req.URL.Path
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	return target
}

// mirrorRedirectCode returns the configured HTTP redirect code, defaulting to 302.
func mirrorRedirectCode() int {
	if configJson.MirrorRedirectCode >= 301 && configJson.MirrorRedirectCode <= 308 {
		return configJson.MirrorRedirectCode
	}
	return http.StatusFound
}
