# mod

Server:

```shell
go get github.com/4396/mod@latest
mod -addr=:6633
```

Client:

```shell
export GOPROXY=http://{ServerIP}:6633
```

Example:

```shell
go get github.com/gorilla/mux@latest
go get github.com/gorilla/mux@master
go get github.com/gorilla/mux@v1.6.2
go get github.com/gorilla/mux@e3702be
go get github.com/gorilla/mux@v0.0.0-20180517173623-c85619274f5d
```
