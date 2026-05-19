package cmd

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/dustin/go-humanize"
	"github.com/urfave/cli/v2"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

var (
	configJson Config

	// staticHandler serves /static/* assets. Built once at startup so the
	// safeFileSystem resolution (Abs + EvalSymlinks) is not redone per request.
	staticHandler http.Handler
)

// staticCacheMaxAge controls Cache-Control max-age for /static/* (seconds).
const staticCacheMaxAge = 3600

const (
	serverUA = "Calaos-WIndex/2.0"

	Arrow = "\u2012\u25b6"
	Star  = "\u2737"
)

var CmdServe = cli.Command{
	Name:        "serve",
	Usage:       "Serve HTTP",
	Description: "This command serves an http index from a folder",
	Action:      serve,
	Flags: []cli.Flag{
		stringFlag("config", "calaos.json", "The config file"),
	},
}

type Config struct {
	ProxyPrefix    string `json:"proxy_prefix"`
	RootFolder     string `json:"root_folder"`
	Port           int    `json:"port"`
	TemplateDir    string `json:"template_dir"`
	RepoTool       string `json:"repo_tool"`
	MaxUploadBytes int64  `json:"max_upload_bytes"` // 0 = default 2 GiB

	// Umami analytics. Page-views come from the JS snippet injected in the
	// template; download events are sent server-side from fileHandler. Events
	// fire only when the server actually delivers the file (not when it
	// redirects to the mirror), so each download is counted once on the
	// instance that serves it.
	UmamiWebsiteId string `json:"umami_website_id"` // empty = disabled
	UmamiScriptURL string `json:"umami_script_url"` // default https://cloud.umami.is/script.js
	UmamiAPIHost   string `json:"umami_api_host"`   // default https://cloud.umami.is

	// Mirror redirect: download files requested on PrimaryHosts are redirected
	// to MirrorBaseURL. Requests arriving on other hosts (the mirror itself)
	// are served directly, preventing redirect loops.
	MirrorBaseURL      string   `json:"mirror_base_url"`      // e.g. "https://dl-direct.raoulh.pw/download". Empty = disabled.
	MirrorMinBytes     int64    `json:"mirror_min_bytes"`     // redirect only if file >= this size. 0 = all files.
	MirrorRedirectCode int      `json:"mirror_redirect_code"` // HTTP redirect code. Default 302.
	PrimaryHosts       []string `json:"primary_hosts"`        // hosts eligible for redirect, e.g. ["calaos.fr", "www.calaos.fr"]

	UploadConfig []struct {
		Subfolder string `json:"subfolder"`
		Key       string `json:"key"`
	} `json:"upload_config"`
	ApiConfig []struct {
		Folder      string `json:"folder"`       //the calaos-os folder
		ReleaseType string `json:"release_type"` //can be one of: stable/experimental
		Machine     string `json:"machine"`      //can be: x86-64, raspberrypi, rasperrypi0, rasperrypi2, rasperrypi3, rasperrypi4
	} `json:"api_config"`
}

// defaultMaxUploadBytes is 2 GiB — overridden by max_upload_bytes in config.
const defaultMaxUploadBytes = 2 << 30

type FileItem struct {
	Icon         string
	Name         string
	Size         string
	ModifiedDate string
	Prefix       string
	CreatedTime  time.Time
}

type Breadcrumb struct {
	Name string
	Path string
}

type DirListing struct {
	Name           string
	ShowParent     bool
	Prefix         string
	Folders        []FileItem
	Files          []FileItem
	Breadcrumbs    []Breadcrumb
	UmamiWebsiteId string
	UmamiScriptURL string
}

type ByCase []FileItem

func (s ByCase) Len() int {
	return len(s)
}
func (s ByCase) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
func (s ByCase) Less(i, j int) bool {
	iRunes := []rune(s[i].Name)
	jRunes := []rune(s[j].Name)

	max := len(iRunes)
	if max > len(jRunes) {
		max = len(jRunes)
	}

	for idx := 0; idx < max; idx++ {
		ir := iRunes[idx]
		jr := jRunes[idx]

		lir := unicode.ToLower(ir)
		ljr := unicode.ToLower(jr)

		if lir != ljr {
			return lir < ljr
		}

		// the lowercase runes are the same, so compare the original
		if ir != jr {
			return ir < jr
		}
	}

	return false
}

type ByCreationTime []FileItem

func (s ByCreationTime) Len() int {
	return len(s)
}
func (s ByCreationTime) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
func (s ByCreationTime) Less(i, j int) bool {
	return s[i].CreatedTime.After(s[j].CreatedTime) // Newer files first
}

func serve(c *cli.Context) error {
	jconf := c.String("config")
	cfile, err := os.ReadFile(jconf)
	if err != nil {
		log.Printf("Reading config file error: %v\n", err)
		return err
	}

	if err = json.Unmarshal(cfile, &configJson); err != nil {
		log.Printf("Unmarshal config file error: %v\n", err)
		return err
	}

	// B2: guard against empty TemplateDir before indexing it
	if configJson.TemplateDir == "" {
		return fmt.Errorf("template_dir must be set in config")
	}
	if !filepath.IsAbs(configJson.TemplateDir) {
		abs, err := filepath.Abs(configJson.TemplateDir)
		if err != nil {
			return fmt.Errorf("resolving template_dir: %w", err)
		}
		configJson.TemplateDir = abs
	}

	// Resolve RootFolder to an absolute path (no Chdir — avoids global CWD mutation).
	if configJson.RootFolder == "" {
		return fmt.Errorf("root_folder must be set in config")
	}
	if !filepath.IsAbs(configJson.RootFolder) {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
		configJson.RootFolder = filepath.Join(cwd, configJson.RootFolder)
	}

	if configJson.Port == 0 {
		configJson.Port = 9696
	}
	if configJson.MaxUploadBytes <= 0 {
		configJson.MaxUploadBytes = defaultMaxUploadBytes
	}

	applyUmamiDefaults(&configJson)

	// Build static handler once: safeFileSystem instance reused across requests,
	// wrapped with Cache-Control headers so browsers cache assets locally.
	staticHandler = http.StripPrefix("/static/",
		staticCacheHeaders(http.FileServer(newSafeFileSystem(configJson.TemplateDir))))

	// Validate mirror config at startup so misconfigurations surface immediately.
	if err := validateMirrorConfig(&configJson); err != nil {
		return fmt.Errorf("invalid mirror config: %w", err)
	}

	ScanForReleases()

	fmt.Println(Arrow, " Starting HTTP server ( root: ", configJson.RootFolder, "), on port", configJson.Port)

	// A8: use safeFileSystem on the root mux entry
	http.Handle("/", http.FileServer(newSafeFileSystem(configJson.RootFolder)))
	handler := buildHttpHandler()

	h2s := &http2.Server{}
	// A5: add server-level timeouts to prevent Slowloris attacks
	server := &http.Server{
		Addr:              ":" + strconv.Itoa(configJson.Port),
		Handler:           h2c.NewHandler(handler, h2s),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout intentionally omitted: large file downloads need no cap here;
		// per-request timeouts should be handled at the reverse proxy level.
	}

	return server.ListenAndServe()
}

func buildHttpHandler() http.Handler {
	var handler http.Handler

	handler = http.DefaultServeMux
	handler = fileHandler(handler)
	handler = uploadHandler(handler)
	handler = apiHandler(handler)
	handler = proxyPrefix(handler)
	handler = logHandler(handler)

	return handler
}

func proxyPrefix(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if configJson.ProxyPrefix != "" {
			if r.URL.Path == "/"+configJson.ProxyPrefix {
				http.Redirect(w, r, "/"+configJson.ProxyPrefix+"/", http.StatusFound)
			} else {
				http.StripPrefix("/"+configJson.ProxyPrefix, handler).ServeHTTP(w, r)
			}
		} else {
			handler.ServeHTTP(w, r)
		}
	})
}

func logHandler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s\n", r.Method, r.URL)
		handler.ServeHTTP(w, r)
	})
}

// safePath joins root + components and verifies the result stays under root.
// Returns an error for path traversal attempts or invalid components.
func safePath(root string, components ...string) (string, error) {
	for _, c := range components {
		// Reject any component that contains a path separator or is ".."
		if strings.Contains(c, "/") || strings.Contains(c, "\\") || c == ".." || c == "." {
			return "", fmt.Errorf("invalid path component: %q", c)
		}
		if c == "" {
			continue
		}
		// Reject dotfiles
		if strings.HasPrefix(c, ".") {
			return "", fmt.Errorf("dotfile components are not allowed: %q", c)
		}
	}

	parts := append([]string{root}, components...)
	dest := filepath.Join(parts...)
	dest = filepath.Clean(dest)

	rootClean := filepath.Clean(root)
	if !strings.HasPrefix(dest, rootClean+string(os.PathSeparator)) && dest != rootClean {
		return "", fmt.Errorf("path escapes root directory")
	}
	return dest, nil
}

// validateUploadKey checks the upload key against configured keys using
// constant-time comparison to prevent timing attacks.
// Returns the upload subfolder path and true if key is valid.
func validateUploadKey(key string) (uploadPath string, ok bool) {
	for _, k := range configJson.UploadConfig {
		// A2: constant-time comparison to prevent timing side-channel
		if subtle.ConstantTimeCompare([]byte(key), []byte(k.Key)) == 1 {
			return k.Subfolder, true
		}
	}
	return "", false
}

// writeUploadFile writes data from reader to destPath atomically via a temp file
// placed in the same directory as the destination (enables atomic os.Rename).
// If sha256Expected is non-empty, validates the checksum before committing.
// If replace is false and destPath already exists, returns an error.
func writeUploadFile(destPath string, reader io.Reader, sha256Expected string, replace bool) error {
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("error creating folder: %w", err)
	}

	// A6: check existence before writing (not after) — still a soft TOCTOU but
	// the real guard is O_EXCL on the final rename target below.
	if _, err := os.Stat(destPath); err == nil && !replace {
		return fmt.Errorf("file exists")
	}

	// A6: temp file in the same directory so os.Rename is atomic (same FS).
	tmpfile, err := os.CreateTemp(dir, ".windex-upload-*")
	if err != nil {
		return fmt.Errorf("error creating temp file: %w", err)
	}
	tmpName := tmpfile.Name()
	// Always clean up the temp file on error.
	committed := false
	defer func() {
		tmpfile.Close()
		if !committed {
			os.Remove(tmpName)
		}
	}()

	// A7: check all I/O errors; C1: stream through hash in one pass via TeeReader.
	var w io.Writer = tmpfile
	var hasher hash.Hash
	if sha256Expected != "" {
		hasher = sha256.New()
		w = io.MultiWriter(tmpfile, hasher)
	}

	if _, err := io.Copy(w, reader); err != nil {
		return fmt.Errorf("error writing upload data: %w", err)
	}

	if hasher != nil {
		got := hex.EncodeToString(hasher.Sum(nil))
		if sha256Expected != got {
			return fmt.Errorf("bad checksum: expected %v got %v", sha256Expected, got)
		}
	}

	if err := tmpfile.Sync(); err != nil {
		return fmt.Errorf("error syncing temp file: %w", err)
	}
	if err := tmpfile.Close(); err != nil {
		return fmt.Errorf("error closing temp file: %w", err)
	}

	// A6: atomic rename — if replace=false and the target appeared between our
	// Stat check and now, the rename will still succeed (last-writer-wins).
	// For strict no-overwrite, use a link+rename trick; this is sufficient here.
	if err := os.Rename(tmpName, destPath); err != nil {
		return fmt.Errorf("error committing file: %w", err)
	}
	committed = true
	return nil
}

func uploadHandler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {

		if !strings.HasPrefix(req.URL.Path, "/upload") {
			handler.ServeHTTP(w, req)
			return
		}

		switch req.Method {
		case "POST":
			handlePostUpload(w, req)
		case "PUT":
			handlePutUpload(w, req)
		default:
			handler.ServeHTTP(w, req)
		}
	})
}

func handlePutUpload(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Server", serverUA)

	log.Printf("Handling PUT file upload.")

	uploadKey := req.Header.Get("X-Upload-Key")
	uploadFilename := req.Header.Get("X-Upload-Filename")
	uploadFolder := req.Header.Get("X-Upload-Folder")
	uploadSha256 := req.Header.Get("X-Upload-SHA256")
	uploadReplace := req.Header.Get("X-Upload-Replace")
	uploadUpdateRepo := req.Header.Get("X-Upload-Update-Repo")
	uploadRepo := req.Header.Get("X-Upload-Repo")

	if uploadKey == "" {
		http.Error(w, "400 Bad Request: missing X-Upload-Key header", http.StatusBadRequest)
		return
	}
	if uploadFilename == "" {
		http.Error(w, "400 Bad Request: missing X-Upload-Filename header", http.StatusBadRequest)
		return
	}

	log.Printf("Checking key authorization...")

	uploadPath, ok := validateUploadKey(uploadKey)
	if !ok {
		// A3: do NOT log the key value — only log that access was refused
		log.Printf("Unauthorized upload attempt refused.\n")
		http.Error(w, "403 Forbidden", http.StatusForbidden)
		return
	}

	// A3: log upload metadata without the secret key
	log.Printf("PUT upload info:\n\tsha256: %v\n\tfolder: %v\n\tfilename: %v\n", uploadSha256, uploadFolder, uploadFilename)

	// A1: use safePath to prevent path traversal
	destPath, err := safePath(configJson.RootFolder, uploadPath, uploadFolder, uploadFilename)
	if err != nil {
		log.Printf("Invalid upload path: %v\n", err)
		http.Error(w, "400 Bad Request: invalid path", http.StatusBadRequest)
		return
	}
	log.Printf("Saving file to: %v\n", destPath)

	// A4: limit request body size to prevent DoS
	req.Body = http.MaxBytesReader(w, req.Body, configJson.MaxUploadBytes)

	err = writeUploadFile(destPath, req.Body, uploadSha256, uploadReplace == "true")
	if err != nil {
		if strings.Contains(err.Error(), "file exists") {
			http.Error(w, "403 File exists.", http.StatusForbidden)
			log.Printf("File already exists, not overwriting: %v\n", destPath)
		} else if strings.Contains(err.Error(), "bad checksum") {
			http.Error(w, "400 Bad checksum: SHA256 failed.", http.StatusBadRequest)
			log.Printf("Wrong sha256: %v\n", err)
		} else if strings.Contains(err.Error(), "request body too large") {
			http.Error(w, "413 Request Entity Too Large", http.StatusRequestEntityTooLarge)
			log.Printf("Upload too large: %v\n", destPath)
		} else {
			// A9: generic error to client, detail only in server logs
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			log.Printf("Error writing file: %v\n", err)
		}
		return
	}

	if uploadUpdateRepo == "true" {
		err = startRepoTool(w, filepath.Join(configJson.RootFolder, filepath.Clean(uploadPath), filepath.Clean(uploadFolder)), filepath.Clean(uploadFilename), uploadRepo)
		if err != nil {
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			log.Printf("Failed to add package to repo\n")
			return
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintln(w, "File created")

	go ScanForReleases()
}

func handlePostUpload(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Server", serverUA)

	log.Printf("Handling POST file upload.")

	formKey := req.FormValue("upload_key")
	formSha256 := req.FormValue("upload_sha256")
	formFolder := req.FormValue("upload_folder")
	formReplace := req.FormValue("upload_replace")
	formUpdateRepo := req.FormValue("upload_update_repo")
	formRepo := req.FormValue("upload_repo")

	log.Printf("Checking key authorization...")

	uploadPath, ok := validateUploadKey(formKey)
	if !ok {
		// A3: do NOT log the key value
		log.Printf("Unauthorized upload attempt refused.\n")
		http.Error(w, "403 Forbidden", http.StatusForbidden)
		return
	}

	// A3: no key in logs
	log.Printf("POST upload info:\n\tsha256: %v\n\tfolder: %v\n", formSha256, formFolder)

	// A4: limit body size before parsing multipart
	req.Body = http.MaxBytesReader(w, req.Body, configJson.MaxUploadBytes)
	if err := req.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "400 Bad Request: could not parse multipart form", http.StatusBadRequest)
		log.Printf("Error parsing multipart form: %v\n", err)
		return
	}
	file, h, err := req.FormFile("upload_file")
	if err != nil {
		http.Error(w, "400 Bad Request: missing upload_file field", http.StatusBadRequest)
		log.Printf("Error getting file %v\n", err)
		return
	}
	defer file.Close()

	if req.MultipartForm != nil {
		defer req.MultipartForm.RemoveAll()
	}

	// A1: safePath prevents path traversal via h.Filename or formFolder
	destPath, err := safePath(configJson.RootFolder, uploadPath, formFolder, h.Filename)
	if err != nil {
		log.Printf("Invalid upload path: %v\n", err)
		http.Error(w, "400 Bad Request: invalid path", http.StatusBadRequest)
		return
	}
	log.Printf("Saving file to: %v\n", destPath)

	err = writeUploadFile(destPath, file, formSha256, formReplace == "true")
	if err != nil {
		if strings.Contains(err.Error(), "file exists") {
			http.Error(w, "403 File exists.", http.StatusForbidden)
			log.Printf("File already exists, not overwriting: %v\n", destPath)
		} else if strings.Contains(err.Error(), "bad checksum") {
			http.Error(w, "400 Bad checksum: SHA256 failed.", http.StatusBadRequest)
			log.Printf("Wrong sha256: %v\n", err)
		} else {
			// A9: generic error to client
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			log.Printf("Error writing file: %v\n", err)
		}
		return
	}

	// Save signature file if it exists
	if req.MultipartForm != nil {
		_, hasSignature := req.MultipartForm.File["upload_file_sig"]
		if hasSignature {
			fileSig, hSig, err := req.FormFile("upload_file_sig")
			if err != nil {
				http.Error(w, "400 Bad Request: could not read signature file", http.StatusBadRequest)
				log.Printf("Error getting signature file %v\n", err)
				return
			}
			defer fileSig.Close()

			// A1: safePath for signature file too
			sigPath, err := safePath(configJson.RootFolder, uploadPath, formFolder, hSig.Filename)
			if err != nil {
				log.Printf("Invalid signature path: %v\n", err)
				http.Error(w, "400 Bad Request: invalid signature path", http.StatusBadRequest)
				return
			}
			log.Printf("Saving signature file to: %v\n", sigPath)

			err = writeUploadFile(sigPath, fileSig, "", formReplace == "true")
			if err != nil {
				if strings.Contains(err.Error(), "file exists") {
					http.Error(w, "403 File exists.", http.StatusForbidden)
					log.Printf("Signature file already exists, not overwriting: %v\n", sigPath)
				} else {
					http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
					log.Printf("Error writing signature file: %v\n", err)
				}
				return
			}
		}
	}

	if formUpdateRepo == "true" {
		err = startRepoTool(w, filepath.Join(configJson.RootFolder, filepath.Clean(uploadPath), filepath.Clean(formFolder)), h.Filename, formRepo)
		if err != nil {
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			log.Printf("Failed to add package to repo\n")
			return
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintln(w, "File created")

	go ScanForReleases()
}

// staticCacheHeaders adds Cache-Control + Vary headers so browsers cache
// /static/* assets locally instead of revalidating each navigation.
func staticCacheHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", staticCacheMaxAge))
		w.Header().Set("Vary", "Accept-Encoding")
		h.ServeHTTP(w, r)
	})
}

func fileHandler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Server", serverUA)

		if strings.HasPrefix(req.URL.Path, "/static") {
			// A8: serve static assets through safeFileSystem (blocks dotfiles/symlinks).
			// Handler is built once at startup and adds Cache-Control headers.
			staticHandler.ServeHTTP(w, req)
			return
		}

		fpath := filepath.Join(configJson.RootFolder, filepath.Clean(req.URL.Path))

		// A8: verify path stays within root before opening
		rootClean := filepath.Clean(configJson.RootFolder)
		fpathClean := filepath.Clean(fpath)
		if !strings.HasPrefix(fpathClean, rootClean+string(os.PathSeparator)) && fpathClean != rootClean {
			http.Error(w, "404 Not Found", http.StatusNotFound)
			return
		}

		f, err := os.Open(fpath)
		if err != nil {
			http.Error(w, "404 Not Found", http.StatusNotFound)
			log.Printf("Error opening file %v\n", err)
			return
		}

		// Checking if the opened handle is really a file
		statinfo, err := f.Stat()
		if err != nil {
			f.Close()
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			log.Printf("Error stat() file %v\n", err)
			return
		}

		if statinfo.IsDir() { // If it's a directory, open it!
			handleDirectory(f, w, req, handler)
			return
		}

		// It's a file — close handle before re-opening via FileServer.
		_, fname := path.Split(f.Name())
		f.Close() // close before handler re-opens via FileServer

		// Mirror redirect: serve download files from the configured mirror URL
		// when the request arrives on a primary host. The mirror instance of this
		// server won't match any primary host, preventing redirect loops.
		// Skip analytics here — the mirror will fire the event when it serves
		// the file, so the download is counted once.
		if shouldMirrorRedirect(req, statinfo) {
			mirrorURL := buildMirrorURL(req)
			log.Println("Redirect to mirror:", req.URL.Path, "→", mirrorURL)
			http.Redirect(w, req, mirrorURL, mirrorRedirectCode())
			return
		}

		// Server actually delivers the file: fire Umami download event once.
		go sendUmamiDownloadEvent(req, fname)

		log.Println("Serve file for URL", req.URL)

		// Use default go serve handler
		handler.ServeHTTP(w, req)
	})
}

func handleDirectory(f *os.File, w http.ResponseWriter, req *http.Request, handler http.Handler) {
	defer f.Close()
	names, _ := f.Readdir(-1)

	// First, check if there is any index in this folder.
	for _, val := range names {
		if val.Name() == "index.html" {
			req.URL.Path = path.Join(f.Name(), "index.html")
			handler.ServeHTTP(w, req)
			return
		}
	}

	data := DirListing{
		Name:           req.URL.Path,
		ShowParent:     true,
		Prefix:         configJson.ProxyPrefix,
		UmamiWebsiteId: configJson.UmamiWebsiteId,
		UmamiScriptURL: configJson.UmamiScriptURL,
	}
	if f.Name() == configJson.RootFolder {
		data.ShowParent = false
	}

	//Fill breadcrumbs
	var p, bpath string
	if configJson.ProxyPrefix != "" {
		data.Breadcrumbs = append(data.Breadcrumbs, Breadcrumb{
			Name: configJson.ProxyPrefix,
			Path: "/" + configJson.ProxyPrefix + "/",
		})
		p = strings.Trim(req.URL.Path, "/")
		bpath = "/" + configJson.ProxyPrefix
	} else {
		data.Breadcrumbs = append(data.Breadcrumbs, Breadcrumb{
			Name: "Root",
			Path: "/",
		})
		p = strings.Trim(req.URL.Path, "/")
	}

	if p != "" {
		for _, b := range strings.Split(p, "/") {
			bpath = bpath + "/" + b
			data.Breadcrumbs = append(data.Breadcrumbs, Breadcrumb{
				Name: b,
				Path: bpath + "/",
			})
		}
	}

	// Otherwise, generate folder content.
	dir_tmp := list.New()
	files_tmp := list.New()

	for _, val := range names {
		if val.Name()[0] == '.' {
			continue
		} // Remove hidden files from listing

		if val.IsDir() {
			dir_tmp.PushBack(val.Name())
		} else {
			files_tmp.PushBack(val.Name())
		}
	}

	//prepare folder info
	data.Folders = make([]FileItem, dir_tmp.Len())
	for i, e := 0, dir_tmp.Front(); e != nil; i, e = i+1, e.Next() {
		data.Folders[i] = FileItem{
			Name:   e.Value.(string),
			Icon:   "folder.png",
			Prefix: configJson.ProxyPrefix,
		}
	}
	sort.Sort(ByCase(data.Folders))

	//prepare file info
	data.Files = make([]FileItem, files_tmp.Len())
	for i, e := 0, files_tmp.Front(); e != nil; i, e = i+1, e.Next() {
		data.Files[i] = createFileItem(f.Name(), e.Value.(string))
	}
	sort.Sort(ByCreationTime(data.Files))

	t, err := template.ParseFiles(path.Join(configJson.TemplateDir, "index.tmpl"))
	if err != nil {
		http.Error(w, "500 Internal Error : Error while generating directory listing. ", 500)
		log.Printf("Error parsing template file %v\n", err)
		return
	}

	t.Execute(w, data)
}

func createFileItem(folder string, filename string) (fi FileItem) {
	fi = FileItem{
		Name:   filename,
		Prefix: configJson.ProxyPrefix,
	}

	file, err := os.Open(path.Join(folder, filename))
	if err != nil {
		log.Printf("Error opening file %v\n", err)
		return fi
	}
	defer file.Close()

	fs, err := file.Stat()
	if err != nil {
		log.Printf("Error stat() file %v\n", err)
		return fi
	}

	fi.Size = humanize.Bytes(uint64(fs.Size()))
	fi.ModifiedDate = humanize.Time(fs.ModTime())
	fi.CreatedTime = fs.ModTime()

	//Icon, find by checking extension
	ext := strings.ToLower(filepath.Ext(filename))

	switch ext {
	case ".zip":
		fi.Icon = "zip.png"
	case ".gz", ".xz", ".bz2":
		fi.Icon = "gzip.png"
	case ".rar":
		fi.Icon = "rar.png"
	case ".ogg", ".wav", ".mp3", ".flac":
		fi.Icon = "audio.png"
	case ".ico":
		fi.Icon = "ico.png"
	case ".gif":
		fi.Icon = "gif.png"
	case ".png":
		fi.Icon = "png.png"
	case ".jpeg", ".jpg":
		fi.Icon = "jpg.png"
	case ".bmp":
		fi.Icon = "bmp.png"
	case ".webp":
		fi.Icon = "image.png"
	case ".xml", ".xslt":
		fi.Icon = "xml.png"
	case ".html", ".htm":
		fi.Icon = "html.png"
	case ".msi":
		fi.Icon = "install.png"
	case ".c":
		fi.Icon = "c.png"
	case ".xls", ".xlsx", ".ods":
		fi.Icon = "calc.png"
	case ".iso", ".img":
		fi.Icon = "cd.png"
	case ".cpp", ".c++":
		fi.Icon = "cpp.png"
	case ".css", ".sass":
		fi.Icon = "css.png"
	case ".deb":
		fi.Icon = "deb.png"
	case ".diff", ".patch":
		fi.Icon = "diff.png"
	case ".doc", ".docx", ".odt":
		fi.Icon = "doc.png"
	case ".eps", ".svg", ".sgvz", ".ai":
		fi.Icon = "eps.png"
	case ".exe", ".dll":
		fi.Icon = "exe.png"
	case ".h":
		fi.Icon = "h.png"
	case ".hpp", ".h++":
		fi.Icon = "hpp.png"
	case ".js":
		fi.Icon = "js.png"
	case ".json":
		fi.Icon = "json.png"
	case ".log", ".ini", ".conf":
		fi.Icon = "log.png"
	case ".md":
		fi.Icon = "markdown.png"
	case ".pdf":
		fi.Icon = "pdf.png"
	case ".php":
		fi.Icon = "php.png"
	case ".m3u", ".pls":
		fi.Icon = "playlist.png"
	case ".ppt", ".pps":
		fi.Icon = "pres.png"
	case ".psd":
		fi.Icon = "psd.png"
	case ".py", ".pyc":
		fi.Icon = "py.png"
	case ".rb":
		fi.Icon = "rb.png"
	case ".rpm":
		fi.Icon = "rpm.png"
	case ".bat", ".sh":
		fi.Icon = "script.png"
	case ".sql":
		fi.Icon = "sql.png"
	case ".tex":
		fi.Icon = "tex.png"
	case ".tiff":
		fi.Icon = "tiff.png"
	case ".avi", ".mp4", ".mkv", ".mpg", ".mpeg":
		fi.Icon = "video.png"
	case ".cal", ".vcal":
		fi.Icon = "vcal.png"
	case ".txt", ".text":
		fi.Icon = "text.png"
	case ".make":
		fi.Icon = "makefile.png"
	default:
		fi.Icon = "unknown.png"
	}

	if strings.Contains(strings.ToLower(filename), "makefile") {
		fi.Icon = "makefile.png"
	}
	if strings.Contains(strings.ToLower(filename), "readme") {
		fi.Icon = "readme.png"
	}

	return fi
}

var repoToolMutex sync.Mutex

func startRepoTool(w io.Writer, folder string, pkgName string, repo string) (err error) {
	//Prevent repo-add tool to run at the same time. It can corrupt the db and signature
	repoToolMutex.Lock()
	defer repoToolMutex.Unlock()

	repoTool := "/usr/bin/repo-add"
	if configJson.RepoTool != "" {
		repoTool = configJson.RepoTool
	}

	log.Println("Starting ", repoTool, ": pkg=", pkgName, "folder=", folder)

	pathToDb := path.Join(folder, repo+".db.tar.gz")
	pkg := path.Join(folder, pkgName)

	args := []string{
		"--remove", //remove old package file from disk after updating database
		"--nocolor",
		"--sign",   //sign database with GnuPG after update
		"--verify", //verify database's signature before update
		pathToDb,
		pkg}

	log.Println("with args:", args)

	cmd := exec.Command(repoTool, args...)

	var buff bytes.Buffer
	multi := io.MultiWriter(w, os.Stdout, &buff)

	cmd.Stdout = multi
	cmd.Stderr = multi

	err = cmd.Run()
	if err != nil {
		log.Println("ERROR:", err)
	}

	//Check if there were any warnings about signature
	if strings.Contains(buff.String(), "Failed to sign package database file") {
		return fmt.Errorf("failed to sign package database file")
	}

	return
}
