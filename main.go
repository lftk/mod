package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func modPath(mod, ver, ext string) string {
	if ver == "" {
		return filepath.Join(download, strings.Replace(mod, "/", string(filepath.Separator), -1), "@v", ext)
	}
	return filepath.Join(download, strings.Replace(mod, "/", string(filepath.Separator), -1), "@v", ver) + ext
}

func readModList(mod string) ([]string, error) {
	f, err := os.Open(modPath(mod, "", "list"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var vers []string
	br := bufio.NewReader(f)
	for {
		line, _, err := br.ReadLine()
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

func findMod(mod, ver string) (string, bool, error) {
	vers, err := readModList(mod)
	if err != nil {
		return "", false, err
	}

	b := hexVer(ver)
	for _, v := range vers {
		if !b {
			if ver == v {
				return ver, true, nil
			}
			continue
		}

		i := strings.LastIndex(v, "-")
		if i == -1 {
			continue
		}

		if strings.HasPrefix(v[i+1:], ver) {
			return v, true, nil
		}
	}
	return ver, false, nil
}

func fetchMod(mod, ver string) (string, error) {
	v, ok := locks.Load(mod)
	if !ok {
		v, _ = locks.LoadOrStore(mod, &sync.Mutex{})
	}
	v.(*sync.Mutex).Lock()
	defer v.(*sync.Mutex).Unlock()

	var buf bytes.Buffer
	path := mod + "@" + ver
	cmd := exec.Command("go", "get", "-d", path)
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil {
		return "", err
	}

	br := bufio.NewReader(&buf)
	for {
		line, _, err := br.ReadLine()
		if err != nil {
			if err == io.EOF {
				return ver, nil
			}
			return "", err
		}

		f := strings.Fields(string(line))
		if len(f) != 4 {
			continue
		}

		if f[1] == "downloading" && f[2] == mod {
			return f[3], nil
		}
	}
}

func httpError(w http.ResponseWriter, r *http.Request, err error) {
	log.Println(r.URL.Path, err)
	http.Error(w, err.Error(), 500)
}

var (
	download string
	locks    sync.Map

	addr = flag.String("addr", ":6633", "mod server address")
)

func init() {
	list := filepath.SplitList(os.Getenv("GOPATH"))
	if len(list) == 0 || list[0] == "" {
		log.Fatalf("missing $GOPATH")
	}

	download = filepath.Join(list[0], "pkg", "mod", "cache", "download")
	if _, err := os.Stat(download); err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(download, os.ModePerm)
		}
		if err != nil {
			log.Fatal(err)
		}
	}

	if dir, err := os.Getwd(); err != nil {
		log.Fatal(err)
	} else {
		path := filepath.Join(dir, "go.mod")
		_, err = os.Stat(path)
		if os.IsNotExist(err) {
			err = ioutil.WriteFile(path, []byte("module mod"), os.ModePerm)
		}
		if err != nil {
			log.Fatal(err)
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

		path := strings.TrimLeft(r.URL.Path, "/")
		i := strings.Index(path, "/@v/")
		if i < 0 {
			http.NotFound(w, r)
			return
		}
		log.Println(r.URL.Path)

		mod, file := path[:i], path[i+len("/@v/"):]
		if file == "list" {
			_, err := fetchMod(mod, "latest")
			if err != nil {
				httpError(w, r, err)
				return
			}
			http.ServeFile(w, r, modPath(mod, "", "list"))
			return
		}

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

		best, ok, err := findMod(mod, ver)
		if err != nil {
			httpError(w, r, err)
			return
		}

		if !ok {
			best, err = fetchMod(mod, ver)
			if err != nil {
				httpError(w, r, err)
				return
			}
		}

		path = modPath(mod, best, ext)
		if _, err = os.Stat(path); err != nil {
			if !os.IsNotExist(err) {
				httpError(w, r, err)
				return
			}

			if ext == ".info" && hexVer(ver) {
				t := time.Now()
				ss := strings.Split(best, "-")
				if len(ss) == 3 {
					t, err = time.Parse("20060102150405", ss[1])
					if err != nil {
						httpError(w, r, err)
						return
					}
				}
				fmt.Fprintf(w, `{"Version":"%s","Time":"%s"}`, best, t.Format(time.RFC3339))
				return
			}
		}
		http.ServeFile(w, r, path)
	})
	log.Fatal(http.ListenAndServe(*addr, nil))
}
