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
	"unicode/utf8"
)

func modPath(mod, ver, ext string) (string, error) {
	path := filepath.Join(download, strings.Replace(mod, "/", string(filepath.Separator), -1), "@v")
	if ver == "" {
		path = filepath.Join(path, ext)
	} else {
		path = filepath.Join(path, ver) + ext
	}
	return encodePath(path)
}

func readModList(mod string) ([]string, error) {
	path, err := modPath(mod, "", "list")
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
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
		if strings.HasPrefix(ver, v[i+1:]) {
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
			if f[3] != ver && hexVer(ver) {
				path, err = modPath(mod, f[3], ".info")
				if err != nil {
					return "", err
				}
				if isNotExist(path) {
					t := time.Now()
					ss := strings.Split(f[3], "-")
					if len(ss) == 3 {
						t, err = time.Parse("20060102150405", ss[1])
						if err != nil {
							return "", err
						}
					}
					info := fmt.Sprintf(`{"Version":"%s","Time":"%s"}`, f[3], t.Format(time.RFC3339))
					err = ioutil.WriteFile(path, []byte(info), os.ModePerm)
				}
			}
			return f[3], err
		}
	}
}

func encodePath(s string) (encoding string, err error) {
	haveUpper := false
	for _, r := range s {
		if r == '!' || r >= utf8.RuneSelf {
			return "", fmt.Errorf("inconsistency")
		}
		if 'A' <= r && r <= 'Z' {
			haveUpper = true
		}
	}

	if !haveUpper {
		return s, nil
	}

	var buf []byte
	for _, r := range s {
		if 'A' <= r && r <= 'Z' {
			buf = append(buf, '!', byte(r+'a'-'A'))
		} else {
			buf = append(buf, byte(r))
		}
	}
	return string(buf), nil
}

func decodePath(encoding string) (string, bool) {
	var buf []byte
	bang := false
	for _, r := range encoding {
		if r >= utf8.RuneSelf {
			return "", false
		}
		if bang {
			bang = false
			if r < 'a' || 'z' < r {
				return "", false
			}
			buf = append(buf, byte(r+'A'-'a'))
			continue
		}
		if r == '!' {
			bang = true
			continue
		}
		if 'A' <= r && r <= 'Z' {
			return "", false
		}
		buf = append(buf, byte(r))
	}
	if bang {
		return "", false
	}
	return string(buf), true
}

func isNotExist(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return os.IsNotExist(err)
	}
	return false
}

func fetchModPath(mod, ver, ext string) (string, error) {
	if ext == "list" {
		_, err := fetchMod(mod, "latest")
		if err != nil {
			return "", err
		}
		return modPath(mod, "", "list")
	}

	best, _, err := findMod(mod, ver)
	if err != nil {
		return "", err
	}

	path, err := modPath(mod, best, ext)
	if err != nil {
		return "", err
	}

	if isNotExist(path) {
		best, err = fetchMod(mod, best)
		if err != nil {
			return "", err
		}
		return modPath(mod, best, ext)
	}
	return path, nil
}

func serveMod(w http.ResponseWriter, r *http.Request, mod, ver, ext string) {
	log.Println(r.URL.Path)
	path, err := fetchModPath(mod, ver, ext)
	if err != nil {
		http.Error(w, err.Error(), 500)
	} else {
		http.ServeFile(w, r, path)
	}
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

		path, ok := decodePath(strings.TrimLeft(r.URL.Path, "/"))
		if !ok {
			http.NotFound(w, r)
			return
		}

		i := strings.Index(path, "/@v/")
		if i < 0 {
			http.NotFound(w, r)
			return
		}

		mod, file := path[:i], path[i+len("/@v/"):]
		if file == "list" {
			serveMod(w, r, mod, "", file)
			return
		}

		i = strings.LastIndex(file, ".")
		if i < 0 || (file[i:] != ".info" && file[i:] != ".mod" && file[i:] != ".zip") {
			http.NotFound(w, r)
			return
		}
		serveMod(w, r, mod, file[:i], file[i:])
	})
	log.Fatal(http.ListenAndServe(*addr, nil))
}
