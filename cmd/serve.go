package cmd

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/dustin/go-humanize"
	ga "github.com/jpillora/go-ogle-analytics"
	"github.com/urfave/cli"
)

var (
	configJson Config
)

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
	ProxyPrefix       string `json:"proxy_prefix"`
	RootFolder        string `json:"root_folder"`
	GoogleAnalyticsId string `json:"google_analytics_id"`
	Port              int    `json:"port"`
	TemplateDir       string `json:"template_dir"`
	RepoTool          string `json:"repo_tool"`

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

type FileItem struct {
	Icon         string
	Name         string
	Size         string
	ModifiedDate string
	Prefix       string
}

type Breadcrumb struct {
	Name string
	Path string
}

type DirListing struct {
	Name        string
	ShowParent  bool
	Prefix      string
	Folders     []FileItem
	Files       []FileItem
	Breadcrumbs []Breadcrumb
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

func serve(c *cli.Context) (err error) {
	jconf := c.String("config")
	cfile, err := ioutil.ReadFile(jconf)
	if err != nil {
		log.Printf("Reading config file error: %v\n", err)
		return err
	}

	if err = json.Unmarshal(cfile, &configJson); err != nil {
		log.Printf("Unmarshal config file error: %v\n", err)
		return err
	}

	if configJson.TemplateDir[0] == '.' {
		curr, err := os.Getwd()
		if err != nil {
			panic(err)
		}
		configJson.TemplateDir = path.Join(curr, configJson.TemplateDir)
	}

	if err = os.Chdir(configJson.RootFolder); err != nil {
		log.Printf("Can't chdir to root_folder: %v\n", err)
		return err
	}

	if configJson.Port == 0 {
		configJson.Port = 9696
	}

	ScanForReleases()

	fmt.Println(Arrow, " Starting HTTP server ( root: ", configJson.RootFolder, "), on port", configJson.Port)

	http.Handle("/", http.FileServer(http.Dir(configJson.RootFolder)))
	handler := buildHttpHandler()

	err = http.ListenAndServe(":"+strconv.Itoa(configJson.Port), handler)

	return err
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

func uploadHandler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {

		if req.Method != "POST" || !strings.HasPrefix(req.URL.Path, "/upload") {
			//Use default go serve handler
			handler.ServeHTTP(w, req)
			return
		}
		w.Header().Set("Server", serverUA)

		log.Printf("Handling file upload.")

		formKey := req.FormValue("upload_key")
		formSha256 := req.FormValue("upload_sha256")
		formFolder := req.FormValue("upload_folder")
		formReplace := req.FormValue("upload_replace")
		formUpdateRepo := req.FormValue("upload_update_repo")
		formRepo := req.FormValue("upload_repo")

		log.Printf("Checking key authorization...")

		found := false
		uploadPath := ""
		for _, k := range configJson.UploadConfig {
			if k.Key == formKey {
				found = true
				uploadPath = k.Subfolder
				break
			}
		}
		if !found {
			log.Printf("No autorized key (%v) found in config. Access refused.\n", formKey)
			http.Error(w, "403 Forbidden", http.StatusForbidden)
			return
		}

		log.Printf("Uploading info:\n\tkey: %v\n\tsha256: %v\n\tfolder: %v\n", formKey, formSha256, formFolder)

		req.ParseMultipartForm(32 << 20)
		file, h, err := req.FormFile("upload_file")
		if err != nil {
			http.Error(w, "500 Internal Error: Error while opening the file.", http.StatusInternalServerError)
			log.Printf("Error getting file %v\n", err)
			return
		}
		defer file.Close()

		if req.MultipartForm != nil {
			defer req.MultipartForm.RemoveAll()
		}

		filepath := path.Join(configJson.RootFolder, path.Clean(uploadPath), path.Clean(formFolder), h.Filename)
		log.Printf("Saving file to: %v\n", filepath)
		err = os.MkdirAll(path.Join(configJson.RootFolder, path.Clean(uploadPath), path.Clean(formFolder)), os.ModePerm)
		if err != nil {
			http.Error(w, "500 Internal Error: Error while creating folder.", http.StatusInternalServerError)
			log.Printf("Error creating folder %v\n", err)
			return
		}

		if _, err := os.Stat(filepath); err == nil {
			if formReplace != "true" {
				http.Error(w, "403 File exists.", http.StatusForbidden)
				log.Printf("Error file exists already. Not overwriting. %v\n", filepath)
				return
			} else {
				os.Remove(filepath)
			}
		}

		tmpfile, _ := ioutil.TempFile(os.TempDir(), "windex_upload")
		defer os.Remove(tmpfile.Name())
		io.Copy(tmpfile, file)
		tmpfile.Seek(0, 0)

		if formSha256 != "" {
			//Check SHA256
			hasher := sha256.New()
			io.Copy(hasher, tmpfile)
			sha := hex.EncodeToString(hasher.Sum(nil))

			if formSha256 != sha {
				http.Error(w, "400 Bad checksum: SHA256 failed.", http.StatusBadRequest)
				log.Printf("Wrong sha256 %v != %v\n", formSha256, sha)
				return
			}

			tmpfile.Seek(0, 0)
		}

		f, err := os.OpenFile(filepath, os.O_WRONLY|os.O_CREATE, 0666)
		if err != nil {
			http.Error(w, "500 Internal Error: Error while opening the file.", http.StatusInternalServerError)
			log.Printf("Error opening file %v\n", err)
			return
		}
		defer f.Close()
		io.Copy(f, tmpfile)

		//Save signature file if it exists
		_, hasSignature := req.MultipartForm.File["upload_file_sig"]
		if hasSignature {
			fileSig, hSig, err := req.FormFile("upload_file_sig")
			if err != nil {
				http.Error(w, "500 Internal Error: Error while opening the file.", http.StatusInternalServerError)
				log.Printf("Error getting file %v\n", err)
				return
			}
			defer fileSig.Close()

			filepath := path.Join(configJson.RootFolder, path.Clean(uploadPath), path.Clean(formFolder), hSig.Filename)
			log.Printf("Saving file to: %v\n", filepath)

			if _, err := os.Stat(filepath); err == nil {
				if formReplace != "true" {
					http.Error(w, "403 File exists.", http.StatusForbidden)
					log.Printf("Error file exists already. Not overwriting. %v\n", filepath)
					return
				} else {
					os.Remove(filepath)
				}
			}

			tmpfile, _ := ioutil.TempFile(os.TempDir(), "windex_upload_sig")
			defer os.Remove(tmpfile.Name())
			io.Copy(tmpfile, file)
			tmpfile.Seek(0, 0)

			f, err := os.OpenFile(filepath, os.O_WRONLY|os.O_CREATE, 0666)
			if err != nil {
				http.Error(w, "500 Internal Error: Error while opening the file.", http.StatusInternalServerError)
				log.Printf("Error opening file %v\n", err)
				return
			}
			defer f.Close()
			io.Copy(f, tmpfile)
		}

		if formUpdateRepo == "true" {
			err = startRepoTool(w, path.Join(configJson.RootFolder, path.Clean(uploadPath), path.Clean(formFolder)), h.Filename, formRepo)
			if err != nil {
				http.Error(w, "500 Internal Error: Error while adding package to repo.", http.StatusInternalServerError)
				log.Printf("Failed to add package to repo\n")
				return
			}
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintln(w, "File created")

		go ScanForReleases()
	})
}

func fileHandler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Server", serverUA)

		if strings.HasPrefix(req.URL.Path, "/static") {
			http.StripPrefix("/static/", http.FileServer(http.Dir(configJson.TemplateDir))).ServeHTTP(w, req)
			return
		}

		filepath := path.Join(configJson.RootFolder, path.Clean(req.URL.Path))

		f, err := os.Open(filepath)
		if err != nil {
			http.Error(w, "404 Not Found: Error while opening the file.", 404)
			log.Printf("Error opening file %v\n", err)
			return
		}

		// Checking if the opened handle is really a file
		statinfo, err := f.Stat()
		if err != nil {
			http.Error(w, "500 Internal Error : stat() failure.", 500)
			log.Printf("Error stat() file %v\n", err)
			return
		}

		if statinfo.IsDir() { // If it's a directory, open it !
			handleDirectory(f, w, req, handler)
			return
		}

		//Its a file, log to GA
		_, fname := path.Split(f.Name())
		go SendAnalyticsData(fname)

		log.Println("Serve file for URL", req.URL)

		//Use default go serve handler
		handler.ServeHTTP(w, req)

		defer f.Close()
	})
}

func handleDirectory(f *os.File, w http.ResponseWriter, req *http.Request, handler http.Handler) {
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
		Name:       req.URL.Path,
		ShowParent: true,
		Prefix:     configJson.ProxyPrefix,
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
	sort.Sort(ByCase(data.Files))

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

func SendAnalyticsData(filename string) {
	log.Println("Sending data to Analytics for file", filename)
	client, err := ga.NewClient(configJson.GoogleAnalyticsId)
	if err != nil {
		log.Println("ERROR, failed to create GA client!")
		return
	}

	err = client.Send(ga.NewEvent("Download", filename))
	if err != nil {
		log.Println("ERROR, failed to send event to GA!", err)
		return
	}

}

func startRepoTool(w io.Writer, folder string, pkgName string, repo string) (err error) {
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

	cmd.Stdout = w
	cmd.Stderr = w

	err = cmd.Run()
	if err != nil {
		log.Println("ERROR:", err)
	}

	return
}
