package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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

var (
	download string // ${GOPATH}/pkg/mod/cache/download
	addr     = flag.String("addr", ":6633", "mod server address")
	timeout  = flag.Duration("timeout", 20*time.Minute, "")

	// 错误码
	errTimeOut = errors.New("Time out") // 常见超时错误
	errOK      = errors.New("OK")       // 该错误是被预期的正常错误
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

func serveMod(w http.ResponseWriter, r *http.Request, mod, ver, ext string) {
	log.Println("[URL]", r.URL.Path)
	path, err := fetchPath(mod, ver, ext)
	if err != nil {
		log.Println("[ERR]", err)
		http.Error(w, err.Error(), 500)
	} else {
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

	path = map[string]string{
		".info": m.Info,
		".mod":  m.GoMod,
		".zip":  m.Zip,
	}[ext]
	if strings.HasSuffix(path, ext) && isExist(path) {
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

	// 增加超时处理
	var err error
	e := make(chan error, 1)

	go func() {
		_, errLocal := runCmd("go", "get", "-d", mod+"@"+ver)
		if errLocal != nil {
			e <- errLocal
		} else {
			e <- errOK
		}
		return
	}()

	select {
	case <-time.After(*timeout):
		err = errTimeOut
	case err = <-e:
		if err == errOK {
			err = nil
		}
	}
	return err
}

type module struct {
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

func modInfo(mod, ver string) (*module, error) {
	b, err := runCmd("go", "mod", "download", "-json", mod+"@"+ver)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(b); i++ {
		if b[i] == '{' {
			var info module
			err = json.Unmarshal(b[i:], &info)
			if err != nil {
				return nil, err
			}
			return &info, nil
		}
	}
	return nil, errors.New("unexpected")
}

func modPath(mod, ver, ext string) (string, error) {
	path := filepath.Join(download, strings.Replace(mod, "/", string(filepath.Separator), -1), "@v")
	if ver == "" {
		path = filepath.Join(path, ext)
	} else {
		path = filepath.Join(path, ver) + ext
	}
	return encodePath(path)
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
	b, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
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
