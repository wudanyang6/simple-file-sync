package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

var (
	port     = flag.Int("port", 8120, "Port to listen on")
	token    = flag.String("token", "", "Token for authentication")
	limitDir = flag.String("limit-dir", "", "Limit directory, start with home dir")
)

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	if len(*token) != 0 {
		uToken := r.PostFormValue("token")
		if uToken != *token {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	fullPath := r.FormValue("target")
	if fullPath == "" {
		http.Error(w, "Missing target", http.StatusBadRequest)
		return
	}
	log.Println("Uploading to: ", fullPath)

	// 获取当前用户的家目录，如果上传文件在家目录之外则报错
	home, err := os.UserHomeDir()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Println(err)
		return
	}

	if !filepath.IsAbs(fullPath) || !strings.HasPrefix(fullPath, home+*limitDir) {
		http.Error(w, "Invalid target path, valid path: "+home+*limitDir, http.StatusBadRequest)
		return
	}

	// 创建目录
	// 判断目录是否存在
	if _, err := os.Stat(filepath.Dir(fullPath)); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			log.Println(err)
			return
		}
	}

	var out *os.File
	// 创建文件
	out, err = os.Create(fullPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Println(err)
		return
	}
	defer out.Close()

	// 将上传的文件内容写入到新文件中
	if _, err := io.Copy(out, file); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Println(err)
		return
	}

	fmt.Fprintf(w, "File uploaded successfully: %s\n", fullPath)
}

func main() {
	flag.Parse()

	// 捕获 ctrl-c 信号，并关闭服务器
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT)

	go func() {
		<-sigs
		log.Println("Caught SIGINT, stopping server...")
		os.Exit(0)
	}()

	http.HandleFunc("/receiver", uploadHandler)
	log.Printf("Starting server at port %d...\n", *port)

	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), nil); err != nil {
		log.Fatal(err)
	}
}
