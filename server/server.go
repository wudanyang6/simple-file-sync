package server

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

type Server struct {
	Port     int
	Token    string
	LimitDir string
	Listener net.Listener // optional; if set, Start uses it instead of dialing Port
}

func NewServer(port int, token, limitDir string) *Server {
	return &Server{
		Port:     port,
		Token:    token,
		LimitDir: limitDir,
	}
}

func (s *Server) Start() {
	ln := s.Listener
	if ln == nil {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT)
		go func() {
			<-sigs
			log.Println("Caught SIGINT, stopping server...")
			os.Exit(0)
		}()
		var err error
		ln, err = net.Listen("tcp", fmt.Sprintf(":%d", s.Port))
		if err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("Starting server at port %d, limit=%s, token=%s\n", s.Port, s.LimitDir, s.Token)
	if err := s.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Println(err)
	}
}

// Serve serves HTTP on the given listener until it is closed or an error occurs.
// Exposed for tests.
func (s *Server) Serve(ln net.Listener) error {
	srv := &http.Server{Handler: s.Handler()}
	return srv.Serve(ln)
}

// Handler returns the HTTP handler used by Start.
// Exposed for tests.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/receiver", s.uploadHandler)
	return mux
}

func (s *Server) uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	if len(s.Token) != 0 {
		uToken := r.PostFormValue("token")
		if uToken != s.Token {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}
	}

	if r.FormValue("op") == "delete" {
		s.deleteHandler(w, r)
		return
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

	if !filepath.IsAbs(fullPath) || !strings.HasPrefix(fullPath, s.LimitDir) {
		http.Error(w, "Invalid target path, valid path: "+s.LimitDir, http.StatusBadRequest)
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

func (s *Server) deleteHandler(w http.ResponseWriter, r *http.Request) {
	target := r.FormValue("target")
	if target == "" {
		http.Error(w, "Missing target", http.StatusBadRequest)
		return
	}
	if !filepath.IsAbs(target) || !strings.HasPrefix(target, s.LimitDir) {
		http.Error(w, "Invalid target path, valid path: "+s.LimitDir, http.StatusBadRequest)
		return
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Println(err)
		return
	}
	log.Println("Deleted: ", target)
	fmt.Fprintf(w, "File deleted: %s\n", target)
}
