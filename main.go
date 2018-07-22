package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type verset struct {
	sync.RWMutex
	Versions []string
}

func loadGoMods(dir string) (mods sync.Map, err error) {
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if filepath.Base(path) != "@v" {
			return nil
		}

		rel, err := filepath.Rel(dir, path)
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

		mods.Store(filepath.Dir(rel), &verset{Versions: vers})
		return filepath.SkipDir
	})
	return
}

func readModList(path string) (vers []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	br := bufio.NewReader(f)
	for {
		var line []byte
		line, _, err = br.ReadLine()
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return
		}
		vers = append(vers, string(line))
	}
}

func findMod(mod, ver string) bool {
	val, ok := modules.Load(mod)
	if !ok {
		return false
	}

	vs := val.(*verset)
	vs.RLock()
	defer vs.RUnlock()

	for _, v := range vs.Versions {
		if ver == v {
			return true
		}
	}
	return false
}

func fetchMod(mod, ver string) error {
	err := downloadMod(mod, ver)
	if err != nil {
		return err
	}

	val, ok := modules.Load(mod)
	if !ok {
		val, _ = modules.LoadOrStore(mod, &verset{})
	}

	vs := val.(*verset)
	vs.Lock()
	defer vs.Unlock()

	for _, v := range vs.Versions {
		if ver == v {
			return nil
		}
	}
	vs.Versions = append(vs.Versions, ver)
	return nil
}

var modLocks sync.Map

func downloadMod(mod, ver string) error {
	path := mod + "@" + ver
	val, ok := modLocks.Load(path)
	if !ok {
		val, _ = modLocks.LoadOrStore(path, &sync.Mutex{})
	}
	mu := val.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	return exec.Command("go", "get", "-d", path).Run()
}

func modPath(path string) string {
	if filepath.Separator != '/' {
		path = strings.Replace(path, "/", string(filepath.Separator), -1)
	}
	return filepath.Join(download, path)
}

var (
	download string
	modules  sync.Map
)

func init() {
	list := filepath.SplitList(os.Getenv("GOPATH"))
	if len(list) == 0 || list[0] == "" {
		log.Fatalf("missing $GOPATH")
	}

	var err error
	download = filepath.Join(list[0], "src", "mod", "cache", "download")
	modules, err = loadGoMods(download)
	if err != nil {
		log.Fatalf("load modules failed, err=%v", err)
	}
}

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimLeft(r.URL.Path, "/")
		i := strings.Index(path, "/@v/")
		if i < 0 {
			http.NotFound(w, r)
			return
		}

		mod, file := path[:i], path[i+len("/@v/"):]
		if file == "list" {
			err := downloadMod(mod, "latest")
			if err != nil {
				http.NotFound(w, r)
				return
			}

			vers, err := readModList(modPath(path))
			if err != nil {
				http.NotFound(w, r)
				return
			}

			// todo ...
			modules.Store(mod, &verset{Versions: vers})

			for _, v := range vers {
				fmt.Fprintf(w, "%s\n", v)
			}
			return
		}

		i = strings.LastIndex(file, ".")
		if i < 0 {
			http.NotFound(w, r)
			return
		}

		ver, ext := file[:i], file[i+1:]
		if ext != "info" && ext != "mod" && ext != "zip" {
			http.NotFound(w, r)
			return
		}

		if !findMod(mod, ver) && fetchMod(mod, ver) != nil {
			http.NotFound(w, r)
			return
		}

		f, err := os.Open(modPath(path))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return
	})
	log.Fatal(http.ListenAndServe(":6633", nil))
}
