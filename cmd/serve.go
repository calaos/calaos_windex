package cmd

import (
	"container/list"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/jpillora/go-ogle-analytics"
	"github.com/urfave/cli"
)

var (
	root_folder string
	tmpl_folder string
	prefix      string
	ga_id       string
)

const (
	serverUA = "Calaos-WIndex/1.0"

	Arrow = "\u2012\u25b6"
	Star  = "\u2737"
)

var CmdServe = cli.Command{
	Name:        "serve",
	Usage:       "Serve HTTP",
	Description: "This command serves an http index from a folder",
	Action:      serve,
	Flags: []cli.Flag{
		stringFlag("prefix", "", "Subfolder when using a HTTP proxy"),
		stringFlag("root", "/var/www", "The folder to serve"),
		stringFlag("ga_id", "UA-XXXXXXXX-Y", "Google analytics ID"),
		intFlag("port", 9696, "HTTP port to listen to"),
		stringFlag("template_dir", "./html", "The folder to look for index.tmpl"),
	},
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

func serve(c *cli.Context) (err error) {
	root_folder = c.String("root")
	tmpl_folder = c.String("template_dir")
	prefix = c.String("prefix")
	ga_id = c.String("ga_id")
	if tmpl_folder[0] == '.' {
		curr, err := os.Getwd()
		if err != nil {
			panic(err)
		}
		tmpl_folder = path.Join(curr, tmpl_folder)
	}

	if err = os.Chdir(root_folder); err != nil {
		return err
	}

	port := c.Int("port")

	fmt.Println(Arrow, " Starting HTTP server, on port", port)

	http.Handle("/", http.FileServer(http.Dir(root_folder)))
	handler := buildHttpHandler()

	err = http.ListenAndServe(":"+strconv.Itoa(port), handler)

	return err
}

func buildHttpHandler() http.Handler {
	var handler http.Handler

	handler = http.DefaultServeMux
	handler = fileHandler(handler)
	handler = proxyPrefix(handler)
	handler = logHandler(handler)

	return handler
}

func proxyPrefix(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if prefix != "" {
			if r.URL.Path == "/"+prefix {
				http.Redirect(w, r, "/"+prefix+"/", http.StatusFound)
			} else {
				http.StripPrefix("/"+prefix, handler).ServeHTTP(w, r)
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

func fileHandler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Server", serverUA)

		if strings.HasPrefix(req.URL.Path, "/static") {
			http.StripPrefix("/static/", http.FileServer(http.Dir(tmpl_folder))).ServeHTTP(w, req)
			return
		}

		filepath := path.Join(root_folder, path.Clean(req.URL.Path))

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
		Prefix:     prefix,
	}
	if f.Name() == root_folder {
		data.ShowParent = false
	}

	//Fill breadcrumbs
	var p, bpath string
	if prefix != "" {
		data.Breadcrumbs = append(data.Breadcrumbs, Breadcrumb{
			Name: prefix,
			Path: "/" + prefix + "/",
		})
		p = strings.TrimPrefix(req.URL.Path, "/"+prefix+"/")
		p = strings.Trim(req.URL.Path, "/")
		bpath = "/" + prefix
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
			Prefix: prefix,
		}
	}

	//prepare file info
	data.Files = make([]FileItem, files_tmp.Len())
	for i, e := 0, files_tmp.Front(); e != nil; i, e = i+1, e.Next() {
		data.Files[i] = createFileItem(f.Name(), e.Value.(string))
	}

	t, err := template.ParseFiles(path.Join(tmpl_folder, "index.tmpl"))
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
		Prefix: prefix,
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
	client, err := ga.NewClient(ga_id)
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
