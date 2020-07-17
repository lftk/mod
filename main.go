package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/module"
)

var (
	download string // ${GOPATH}/pkg/mod/cache/download
	addr     = flag.String("addr", ":6633", "mod server address")
	ttl      = flag.Duration("ttl", 3*time.Minute, "get mod timeout duration")
)

func init() {
	list := filepath.SplitList(os.Getenv("GOPATH"))
	if len(list) == 0 || list[0] == "" {
		log.Fatal("missing $GOPATH")
	}

	download = filepath.Join(list[0], "pkg", "mod", "cache", "download")
	if isNotExist(download) {
		err := os.MkdirAll(download, os.ModePerm)
		if err != nil {
			log.Fatal(err)
		}
	}

	if dir, err := os.Getwd(); err != nil {
		log.Fatal(err)
	} else {
		path := filepath.Join(dir, "go.mod")
		if isNotExist(path) {
			err = ioutil.WriteFile(path, []byte("module mod"), os.ModePerm)
			if err != nil {
				log.Fatal(err)
			}
		}
	}
}

func main() {
	flag.Parse()
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}

		path := r.URL.Path[len("/"):]
		i := strings.Index(path, "/@v/")
		if i < 0 {
			http.NotFound(w, r)
			return
		}

		enc, file := path[:i], path[i+len("/@v/"):]
		mod, err := module.UnescapePath(enc)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		if file == "list" {
			serveMod(w, r, mod, "", "list")
			return
		}

		i = strings.LastIndex(file, ".")
		if i < 0 {
			http.NotFound(w, r)
			return
		}

		encVers, ext := file[:i], file[i:]
		vers, err := module.UnescapeVersion(encVers)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		if ext != ".info" && ext != ".mod" && ext != ".zip" {
			http.NotFound(w, r)
			return
		}
		serveMod(w, r, mod, vers, ext)
	})
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func serveMod(w http.ResponseWriter, r *http.Request, mod, ver, ext string) {
	path, err := fetchPath(mod, ver, ext)
	if err != nil {
		log.Println("[ERR]", r.URL.Path, "->", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		log.Println("[OK]", r.URL.Path, "->", path)
		http.ServeFile(w, r, path)
	}
}

func fetchPath(mod, ver, ext string) (string, error) {
	path, err := modPath(mod, ver, ext)
	if err != nil {
		return "", err
	}

	if ext == "list" {
		err = fetchMod(mod, "latest")
		if err != nil {
			return "", err
		}
		return path, nil
	}

	if isExist(path) {
		return path, nil
	}

	m, err := modInfo(mod, ver)
	if err != nil {
		return "", err
	}

	if strings.HasPrefix(m.Error, "unknown revision") {
		return "", errors.New(m.Error)
	}

	path, ok := map[string]string{
		".info": m.Info,
		".mod":  m.GoMod,
		".zip":  m.Zip,
	}[ext]
	if ok && isExist(path) {
		return path, nil
	}

	path, err = modPath(mod, m.Version, ext)
	if err != nil {
		return "", err
	}

	if isNotExist(path) {
		err = fetchMod(mod, m.Version)
		if err != nil {
			return "", err
		}
	}
	return path, nil
}

var fetchLock sync.Map

func fetchMod(mod, ver string) error {
	v, ok := fetchLock.Load(mod)
	if !ok {
		v, _ = fetchLock.LoadOrStore(mod, &sync.Mutex{})
	}
	v.(*sync.Mutex).Lock()
	defer v.(*sync.Mutex).Unlock()

	_, err := runCmd("go", "get", "-d", mod+"@"+ver)
	return err
}

type moduleJSON struct {
	Path     string `json:",omitempty"`
	Version  string `json:",omitempty"`
	Error    string `json:",omitempty"`
	Info     string `json:",omitempty"`
	GoMod    string `json:",omitempty"`
	Zip      string `json:",omitempty"`
	Dir      string `json:",omitempty"`
	Sum      string `json:",omitempty"`
	GoModSum string `json:",omitempty"`
}

func modInfo(mod, ver string) (*moduleJSON, error) {
	b, err := runCmd("go", "mod", "download", "-json", mod+"@"+ver)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(b); i++ {
		if b[i] == '{' {
			var mod moduleJSON
			err = json.Unmarshal(b[i:], &mod)
			if err != nil {
				return nil, err
			}
			return &mod, nil
		}
	}
	return nil, errors.New("unexpected")
}

func modPath(mod, ver, ext string) (string, error) {
	path := filepath.Join(strings.Replace(mod, "/", string(filepath.Separator), -1))
	path, err := module.EscapePath(path)
	if err != nil {
		return "", err
	}
	if ver != "" {
		ver, err := module.EscapeVersion(ver)
		if err != nil {
			return "", err
		}
		return filepath.Join(download, path, "@v", ver) + ext, nil
	}
	return filepath.Join(download, path, "@v", ext), nil
}

type runError struct {
	Cmd    string
	Err    error
	Stderr []byte
}

func (e *runError) Error() string {
	text := e.Cmd + ": " + e.Err.Error()
	stderr := bytes.TrimRight(e.Stderr, "\n")
	if len(stderr) > 0 {
		text += ":\n\t" + strings.Replace(string(stderr), "\n", "\n\t", -1)
	}
	return text
}

func runCmd(cmd ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), *ttl)
	defer cancel()
	b, err := exec.CommandContext(ctx, cmd[0], cmd[1:]...).CombinedOutput()
	if err != nil {
		return nil, &runError{Cmd: strings.Join(cmd, " "), Err: err, Stderr: b}
	}
	return b, nil
}

func isExist(path string) bool {
	return !isNotExist(path)
}

func isNotExist(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return os.IsNotExist(err)
	}
	return false
}
