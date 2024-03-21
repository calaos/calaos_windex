package cmd

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/blake2b"
)

type JSONTime time.Time

type ReleaseFile struct {
	Filename    string   `json:"-"`
	Url         string   `json:"url"`
	Machine     string   `json:"machine"`
	ReleaseType string   `json:"release_type"`
	Version     string   `json:"version"`
	Date        JSONTime `json:"release_date"`
	Filesize    int64    `json:"filesize"`
	Checksum    string   `json:"hash_blake2b"`
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
		if r.Method != "GET" ||
			!strings.HasPrefix(r.URL.Path, "/api") ||
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

// ScanForReleases walk through all folder, search calaos-os image files and populates releaseCache
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
				strings.HasSuffix(f.Name(), ".hddimg.zst") ||
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
					Filesize:    f.Size(),
					Checksum:    computeBlakeHash(filepath.Join(d, f.Name())),
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
	regVer = regexp.MustCompile(`.*v((?:\d\.\d\.\d|\d\.\d)(?:-(?:alpha|rc)[\.]{0,1}\d+)?(?:-\d+)?(?:-g[a-f0-9]+)?(?:-\d{8})?)`)
)

func extractVersion(fname string) (vers string) {
	match := regVer.FindStringSubmatch(fname)
	if len(match) >= 2 {
		return match[1]
	}

	return "unknown"
}

func computeBlakeHash(fname string) string {
	hasher, _ := blake2b.New256(nil)

	f, err := os.Open(fname)
	if err != nil {
		log.Println("checksum: Failed to open file", err)
		return ""
	}
	defer f.Close()

	if _, err := io.Copy(hasher, f); err != nil {
		log.Println("checksum: Failed to hash file", err)
		return ""
	}

	hash := hasher.Sum(nil)
	return hex.EncodeToString(hash[:])
}
