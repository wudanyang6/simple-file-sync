package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

func uploadFile(filename string, serverURL string, target string, baseDir string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	part, err := writer.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}

	relativePath, err := filepath.Rel(baseDir, filename)
	if err != nil {
		return err
	}

	targetPath := filepath.Join(target, relativePath)
	log.Println("Uploading file: ", targetPath)

	writer.WriteField("target", targetPath)
	contentType := writer.FormDataContentType()
	writer.Close()

	resp, err := http.Post(serverURL, contentType, &requestBody)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to upload file: %s", resp.Status)
	}

	return nil
}

func worker(id int, jobs <-chan string, serverURL string, target string, baseDir string) {
	for filename := range jobs {
		err := uploadFile(filename, serverURL, target, baseDir)
		if err != nil {
			log.Printf("Worker %d failed to upload file: %s error: %v\n", id, filename, err)
		}
	}
}

func getGitDiffFiles(baseDir string) ([]string, error) {
	cmd := exec.Command("git", "diff", "origin", "--name-only")
	cmd.Dir = baseDir
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	files := strings.Split(string(output), "\n")
	var diffFiles []string
	for _, file := range files {
		if file != "" {
			diffFiles = append(diffFiles, filepath.Join(baseDir, file))
		}
	}
	return diffFiles, nil
}

func main() {
	initMode := flag.String("mode", "all", "Initialization mode: 'all' to upload all files, 'git' to upload git diff files")
	baseDir := flag.String("dir", "/path/to/your/watched/directory", "Directory to watch")
	serverURL := flag.String("url", "http://localhost:8080/receiver", "Server URL")
	target := flag.String("target", "/path/to/your/destination/directory", "Target directory on server")
	flag.Parse()

	isSetBaseDir, isSetServerURL, isSetTarget := false, false, false
	flag.VisitAll(func(f *flag.Flag) {
		switch f.Name {
		case "dir":
			isSetBaseDir = true
		case "url":
			isSetServerURL = true
		case "target":
			isSetTarget = true
		}
	})

	if !isSetBaseDir || !isSetServerURL || !isSetTarget {
		fmt.Println("Please set the flags before running the program", isSetBaseDir, isSetServerURL, isSetTarget)
		flag.Usage()
		return
	}

	*baseDir, _ = filepath.Abs(*baseDir)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	const numWorkers = 30
	jobs := make(chan string, numWorkers)

	for w := 1; w <= numWorkers; w++ {
		go worker(w, jobs, *serverURL, *target, *baseDir)
	}

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				log.Println(event)
				if strings.HasSuffix(event.Name, "~") {
					log.Println("Skipping backup file:", event.Name)
					continue
				}
				fi, err := os.Stat(event.Name)
				if err == nil && fi.IsDir() && strings.HasPrefix(fi.Name(), ".") {
					log.Println("Skipping hidden directory:", event.Name)
					continue
				}

				if event.Op&fsnotify.Create == fsnotify.Create {
					log.Println("Detected new file or directory:", event.Name)

					if err == nil && fi.IsDir() {
						errDir := watcher.Add(event.Name)
						log.Println("Watching new dir" + event.Name)
						if errDir != nil {
							log.Println("Error adding directory to watcher:", event.Name, errDir)
						}
						continue
					}

					time.Sleep(2 * time.Second) // 确保文件已完全写入
					jobs <- event.Name
				} else if event.Op&fsnotify.Write == fsnotify.Write {
					log.Println("Detected file change:", event.Name)
					time.Sleep(2 * time.Second) // 确保文件已完全写入
					jobs <- event.Name
				} else if event.Op&fsnotify.Remove == fsnotify.Remove {
					log.Println("Detected file removal:", event.Name)
				} else if event.Op&fsnotify.Rename == fsnotify.Rename {
					log.Println("Detected file rename:", event.Name)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("Error:", err)
			}
		}
	}()

	err = watcher.Add(*baseDir)
	if err != nil {
		log.Fatal(err)
	}

	log.Println("Watching directory:", *baseDir)

	// 初始化时上传文件
	var filesToUpload []string

	err = filepath.Walk(*baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if *initMode == "all" {
				filesToUpload = append(filesToUpload, path)
			}
		} else {
			if strings.HasPrefix(info.Name(), ".") {
				log.Println("Skipping hidden directory:", path)
				return filepath.SkipDir
			}
			err = watcher.Add(path)
			if err != nil {
				log.Println("Error adding directory to watcher:", path, err)
			}
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	if *initMode == "all" {
		log.Println("Uploading all files:", len(filesToUpload))
	} else if *initMode == "git" {
		filesToUpload, err = getGitDiffFiles(*baseDir)
		if err != nil {
			log.Fatal(err)
		}
		log.Println("Uploading git diff files:", len(filesToUpload))
	} else {
		log.Fatalf("Unknown mode: %s", *initMode)
	}

	for _, file := range filesToUpload {
		jobs <- file
	}

	<-make(chan struct{})
}
