package main

import (
	"bufio"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var (
	download string
	modules  sync.Map
	locks    sync.Map
)

func loadGoMods() error {
	f := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if filepath.Base(path) != "@v" {
			return nil
		}

		rel, err := filepath.Rel(download, path)
		if err != nil {
			return err
		}

		list := filepath.Join(path, "list")
		if _, err = os.Stat(list); err != nil {
			if os.IsNotExist(err) {
				err = nil
			}
			return err
		}

		vers, err := readModList(list)
		if err != nil {
			return err
		}

		modules.Store(strings.Replace(filepath.Dir(rel), string(filepath.Separator), "/", -1), vers)
		return filepath.SkipDir
	}
	return filepath.Walk(download, f)
}

func readModList(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var vers []string
	br := bufio.NewReader(f)
	for {
		var line []byte
		line, _, err = br.ReadLine()
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return vers, err
		}
		vers = append(vers, string(line))
	}
}

func hexVer(ver string) bool {
	for i := 0; i < len(ver); i++ {
		if c := ver[i]; '0' <= c && c <= '9' || 'a' <= c && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

func findMod(mod, ver string) (string, bool) {
	val, ok := modules.Load(mod)
	if !ok {
		return ver, false
	}

	b := hexVer(ver)
	for _, v := range val.([]string) {
		if !b {
			if ver == v {
				return ver, true
			}
			continue
		}

		i := strings.LastIndex(v, "-")
		if i == -1 {
			continue
		}

		if strings.HasPrefix(v[i+1:], ver) {
			return v, true
		}
	}
	return ver, false
}

func fetchMod(mod, ver string) (string, error) {
	path := mod + "@" + ver
	val, ok := locks.Load(path)
	if !ok {
		val, _ = locks.LoadOrStore(path, &sync.Mutex{})
	}
	mu := val.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	err := exec.Command("go", "get", "-d", path).Run()
	if err != nil {
		return "", err
	}

	vers, err := readModList(modPath(mod + "/@v/list"))
	if err != nil {
		return "", err
	}

	modules.Store(mod, vers)
	val, _ = modules.LoadOrStore(mod, []string{})
	if len(vers) != len(val.([]string)) {
		for i := len(vers) - 1; i >= 0; i-- {
			var found bool
			for _, v := range val.([]string) {
				if vers[i] == v {
					found = true
					break
				}
			}
			if !found {
				return vers[i], nil
			}
		}
	}
	return ver, nil
}

func modPath(mod string) string {
	return filepath.Join(download, strings.Replace(mod, "/", string(filepath.Separator), -1))
}

var (
	addr = flag.String("addr", ":6633", "mod server address")
)

func main() {
	flag.Parse()

	list := filepath.SplitList(os.Getenv("GOPATH"))
	if len(list) == 0 || list[0] == "" {
		log.Fatalf("missing $GOPATH")
	}

	download = filepath.Join(list[0], "pkg", "mod", "cache", "download")
	os.MkdirAll(download, 0777)

	err := loadGoMods()
	if err != nil {
		log.Fatalf("load modules failed, err=%v", err)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}

		log.Println(r.URL.Path)
		path := strings.TrimLeft(r.URL.Path, "/")
		i := strings.Index(path, "/@v/")
		if i < 0 {
			http.NotFound(w, r)
			return
		}

		mod, file := path[:i], path[i+len("/@v/"):]
		if file == "list" {
			_, err := fetchMod(mod, "latest")
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		} else {
			i = strings.LastIndex(file, ".")
			if i < 0 {
				http.NotFound(w, r)
				return
			}

			ver, ext := file[:i], file[i:]
			if ext != ".info" && ext != ".mod" && ext != ".zip" {
				http.NotFound(w, r)
				return
			}

			best, ok := findMod(mod, ver)
			if !ok {
				ver, err := fetchMod(mod, best)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				best = ver
			}
			path = mod + "/@v/" + best + ext
		}
		http.ServeFile(w, r, modPath(path))
	})
	log.Fatal(http.ListenAndServe(*addr, nil))
}
