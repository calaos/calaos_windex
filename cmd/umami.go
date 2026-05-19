package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// defaultUmamiScriptURL is used when umami_script_url is not set.
const defaultUmamiScriptURL = "https://cloud.umami.is/script.js"

// defaultUmamiAPIHost is used when umami_api_host is not set.
const defaultUmamiAPIHost = "https://cloud.umami.is"

// umamiClient is reused across events to keep connections alive.
var umamiClient = &http.Client{Timeout: 5 * time.Second}

// umamiPayload mirrors the Umami /api/send schema.
type umamiPayload struct {
	Hostname string            `json:"hostname"`
	Language string            `json:"language,omitempty"`
	Referrer string            `json:"referrer,omitempty"`
	Screen   string            `json:"screen,omitempty"`
	Title    string            `json:"title,omitempty"`
	URL      string            `json:"url"`
	Website  string            `json:"website"`
	Name     string            `json:"name,omitempty"`
	Data     map[string]string `json:"data,omitempty"`
}

type umamiRequest struct {
	Type    string       `json:"type"`
	Payload umamiPayload `json:"payload"`
}

// applyUmamiDefaults fills in missing optional fields.
func applyUmamiDefaults(cfg *Config) {
	if cfg.UmamiScriptURL == "" {
		cfg.UmamiScriptURL = defaultUmamiScriptURL
	}
	if cfg.UmamiAPIHost == "" {
		cfg.UmamiAPIHost = defaultUmamiAPIHost
	}
}

// clientIP extracts the originating client IP from X-Forwarded-For, X-Real-IP,
// or req.RemoteAddr in that order. The first hop in X-Forwarded-For wins.
func clientIP(req *http.Request) string {
	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := req.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}

// sendUmamiDownloadEvent posts a "download" event to the Umami /api/send
// endpoint. No-op if umami_website_id is empty. Must be called as a goroutine.
func sendUmamiDownloadEvent(req *http.Request, filename string) {
	if configJson.UmamiWebsiteId == "" {
		return
	}

	apiHost := configJson.UmamiAPIHost
	if apiHost == "" {
		apiHost = defaultUmamiAPIHost
	}

	body := umamiRequest{
		Type: "event",
		Payload: umamiPayload{
			Hostname: hostOnly(req.Host),
			Language: req.Header.Get("Accept-Language"),
			Referrer: req.Header.Get("Referer"),
			URL:      req.URL.Path,
			Website:  configJson.UmamiWebsiteId,
			Name:     "download",
			Data: map[string]string{
				"file": filename,
			},
		},
	}

	buf, err := json.Marshal(body)
	if err != nil {
		log.Println("Umami: marshal error:", err)
		return
	}

	endpoint := strings.TrimRight(apiHost, "/") + "/api/send"
	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		log.Println("Umami: new request error:", err)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if ua := req.Header.Get("User-Agent"); ua != "" {
		httpReq.Header.Set("User-Agent", ua)
	} else {
		httpReq.Header.Set("User-Agent", serverUA)
	}
	if ip := clientIP(req); ip != "" {
		httpReq.Header.Set("X-Forwarded-For", ip)
	}

	log.Printf("Umami: sending download event file=%q url=%q ip=%s", filename, req.URL.Path, clientIP(req))
	start := time.Now()
	resp, err := umamiClient.Do(httpReq)
	if err != nil {
		log.Println("Umami: send error:", err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		log.Printf("Umami: unexpected status %d for file=%q (%s)", resp.StatusCode, filename, time.Since(start))
		return
	}
	log.Printf("Umami: event sent file=%q status=%d (%s)", filename, resp.StatusCode, time.Since(start))
}
