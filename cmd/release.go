package cmd

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type JSONTime time.Time

type ReleaseFile struct {
	Filename    string   `json:"-"`
	Url         string   `json:"url"`
	Machine     string   `json:"machine"`
	ReleaseType string   `json:"release_type"`
	Version     string   `json:"version"`
	Date        JSONTime `json:"release_date"`
}

type JSONMarshaler interface {
	MarshalJSON() ([]byte, error)
}

// MarshallJSON marshalls date time into formated string usable in JSON
func (t JSONTime) MarshalJSON() ([]byte, error) {
	stamp := fmt.Sprintf("\"%s\"", time.Time(t).Format(time.RFC3339))
	return []byte(stamp), nil
}

var (
	releaseCache []*ReleaseFile
	relMutex     = &sync.Mutex{}
)

func apiHandler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" &&
			!strings.HasPrefix(r.URL.Path, "/api") &&
			strings.ToLower(r.Header.Get("Content-Type")) != "application/json" {
			handler.ServeHTTP(w, r)
			return
		}

		log.Println("/api called")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		enc := json.NewEncoder(w)
		err := enc.Encode(releaseCache)

		if err != nil {
			log.Println("Failed to marshal json:", err)
		}
	})
}

//ScanForReleases walk through all folder, search calaos-os image files and populates releaseCache
func ScanForReleases() {
	log.Println("ScanForReleases...")
	var rel []*ReleaseFile

	for _, apiItem := range configJson.ApiConfig {
		d := filepath.Join(configJson.RootFolder, apiItem.Folder)

		files, err := ioutil.ReadDir(d)
		if err != nil {
			log.Println("Failed to read dir", d, err)
		}

		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".tar.xz") ||
				strings.HasSuffix(f.Name(), ".tar.gz") ||
				strings.HasSuffix(f.Name(), ".tar.zst") ||
				strings.HasSuffix(f.Name(), ".tar.bz2") ||
				strings.HasSuffix(f.Name(), ".hddimg") ||
				strings.HasSuffix(f.Name(), ".hddimg.xz") ||
				strings.HasSuffix(f.Name(), ".rpi-sdimg") ||
				strings.HasSuffix(f.Name(), "sdimg") ||
				strings.HasSuffix(f.Name(), ".rpi-sdimg.xz") {

				r := &ReleaseFile{
					Filename:    filepath.Join(d, f.Name()),
					Url:         fmt.Sprintf("https://calaos.fr/download/%s/%s", apiItem.Folder, f.Name()),
					Machine:     apiItem.Machine,
					ReleaseType: apiItem.ReleaseType,
					Date:        JSONTime(f.ModTime()),
					Version:     extractVersion(f.Name()),
				}

				rel = append(rel, r)
			}
		}
	}

	relMutex.Lock()
	releaseCache = nil
	releaseCache = append(releaseCache, rel...)
	relMutex.Unlock()
	log.Printf("Found %d images", len(releaseCache))
}

var (
	regStable = regexp.MustCompile(`.*(v\d.\d)`)
	regDev    = regexp.MustCompile(`.*(v\d.\d-[a-z0-9]+-\d{0,3}-[a-z0-9]{8})`)
)

func extractVersion(fname string) (vers string) {
	match := regDev.FindStringSubmatch(fname)
	if len(match) >= 2 {
		return match[1]
	}

	match = regStable.FindStringSubmatch(fname)
	if len(match) >= 2 {
		return match[1]
	}

	return "unknown"
}
