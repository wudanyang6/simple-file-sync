package client

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	NumWorkers      = 30
	DefaultDebounce = 500 * time.Millisecond
)

// syncOp identifies the kind of remote operation a worker should perform.
type syncOp int

const (
	opUpload syncOp = iota
	opDelete
)

type syncTask struct {
	op   syncOp
	path string
}

type PathMapping struct {
	SourcePattern *regexp.Regexp
	TargetPath    string
}

// RemoteTarget 表示一个远程目标配置
type RemoteTarget struct {
	Name      string
	URL       string
	TargetDir string
	Token     string
	// PropagateDeletesSet 表示该目标是否在配置中显式声明了 propagate_deletes。
	// 显式声明时优先于 Client.PropagateDeletes（全局默认值）。
	PropagateDeletesSet bool
	PropagateDeletes    bool
}

type Client struct {
	Mode           string
	LocalDir       string
	RemoteTargets  []RemoteTarget
	ActiveTarget   string // 当前激活的远程目标名称
	PathMappings   []PathMapping
	IgnorePatterns []*regexp.Regexp
	HTTPClient     *http.Client
	// PropagateDeletes gates whether local Remove/Rename events are forwarded to
	// the remote as op=delete. Off by default — must be explicitly enabled.
	PropagateDeletes bool
	// DeleteDebounce delays remote deletion to allow editor atomic-saves
	// (write-temp + rename) to cancel a pending delete. Default DefaultDebounce.
	DeleteDebounce time.Duration

	uploadChan chan syncTask
	watcher    *fsnotify.Watcher

	pendingMu      sync.Mutex
	pendingDeletes map[string]*time.Timer
}

func NewClient(mode, baseDir string) *Client {
	return &Client{
		Mode:           mode,
		LocalDir:       baseDir,
		RemoteTargets:  []RemoteTarget{},
		PathMappings:   []PathMapping{},
		IgnorePatterns: []*regexp.Regexp{},
		HTTPClient:     &http.Client{Timeout: 30 * time.Second},
		DeleteDebounce: DefaultDebounce,
		uploadChan:     make(chan syncTask, NumWorkers),
		pendingDeletes: make(map[string]*time.Timer),
	}
}

// AddRemoteTargetWithDeletes 添加一个远程目标；propagateDeletes 为 nil 表示未显式配置，
// 删除传播回退到全局 Client.PropagateDeletes。
func (c *Client) AddRemoteTargetWithDeletes(name, url, targetDir, token string, propagateDeletes *bool) {
	c.RemoteTargets = append(c.RemoteTargets, RemoteTarget{
		Name:                name,
		URL:                 url,
		TargetDir:           targetDir,
		Token:               token,
		PropagateDeletesSet: propagateDeletes != nil,
		PropagateDeletes:    propagateDeletes != nil && *propagateDeletes,
	})

	// 如果是第一个添加的目标，默认设为激活状态
	if len(c.RemoteTargets) == 1 {
		c.ActiveTarget = name
	}

	log.Printf("Added remote target: %s -> %s", name, url)
}

// AddRemoteTarget 添加一个远程目标，删除传播使用全局默认（等价于
// AddRemoteTargetWithDeletes 传入 nil）。
func (c *Client) AddRemoteTarget(name, url, targetDir, token string) {
	c.AddRemoteTargetWithDeletes(name, url, targetDir, token, nil)
}

// SetActiveTarget 设置当前激活的远程目标
func (c *Client) SetActiveTarget(name string) error {
	for _, target := range c.RemoteTargets {
		if target.Name == name {
			c.ActiveTarget = name
			log.Printf("Activated remote target: %s", name)
			return nil
		}
	}
	return fmt.Errorf("remote target not found: %s", name)
}

// GetActiveTarget 获取当前激活的远程目标
func (c *Client) GetActiveTarget() (*RemoteTarget, error) {
	for _, target := range c.RemoteTargets {
		if target.Name == c.ActiveTarget {
			return &target, nil
		}
	}
	return nil, fmt.Errorf("no active remote target set")
}

// deletesEnabled 判断删除传播在当前激活目标上是否开启：
// active target 显式配置了 propagate_deletes 时优先使用该值，
// 否则回退到全局 Client.PropagateDeletes（默认关闭）。
func (c *Client) deletesEnabled() bool {
	t, err := c.GetActiveTarget()
	if err == nil && t.PropagateDeletesSet {
		return t.PropagateDeletes
	}
	return c.PropagateDeletes
}

// ListRemoteTargets 列出所有可用的远程目标
func (c *Client) ListRemoteTargets() []RemoteTarget {
	return c.RemoteTargets
}

// AddPathMapping 添加一个路径映射，使用正则表达式
func (c *Client) AddPathMapping(source, target string) {
	// 编译正则表达式
	pattern, err := regexp.Compile(source)
	if err != nil {
		log.Printf("Invalid path mapping pattern %s: %v", source, err)
		return
	}

	c.PathMappings = append(c.PathMappings, PathMapping{
		SourcePattern: pattern,
		TargetPath:    target,
	})
	log.Printf("Added path mapping: %s -> %s", source, target)
}

// AddIgnorePattern 添加一个忽略模式，使用正则表达式
func (c *Client) AddIgnorePattern(pattern string) {
	// 如果模式不是以^开头和$结尾，添加这些锚点以确保完全匹配
	if !strings.HasPrefix(pattern, "^") {
		pattern = "^" + pattern
	}
	if !strings.HasSuffix(pattern, "$") {
		pattern = pattern + "$"
	}
	
	// 编译正则表达式
	re, err := regexp.Compile(pattern)
	if err != nil {
		log.Printf("Invalid ignore pattern %s: %v", pattern, err)
		return
	}

	c.IgnorePatterns = append(c.IgnorePatterns, re)
	log.Printf("Added ignore pattern: %s", pattern)
}

// ShouldIgnore 检查文件是否应该被忽略，使用正则表达式匹配
func (c *Client) ShouldIgnore(path string) bool {
	// 检查文件是否匹配忽略正则表达式
	for _, pattern := range c.IgnorePatterns {
		if pattern.MatchString(path) {
			log.Println("ignore pattern: ", pattern, path)
			return true
		}
	}

	return false
}

// MapPath 根据路径映射规则映射路径，使用正则表达式
func (c *Client) MapPath(path string) string {
	// 如果没有路径映射规则，直接返回原始路径
	if len(c.PathMappings) == 0 {
		return path
	}

	// 检查是否有匹配的路径映射
	for _, mapping := range c.PathMappings {
		if mapping.SourcePattern.MatchString(path) {
			// 使用正则表达式替换
			return mapping.SourcePattern.ReplaceAllString(path, mapping.TargetPath)
		}
	}

	// 没有匹配的映射规则，返回原始路径
	return path
}

func (c *Client) Start() {
	// 启动 worker
	for w := 1; w <= NumWorkers; w++ {
		go c.worker(w)
	}

	var err error
	c.watcher, err = fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
		return
	}
	defer c.watcher.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go c.watcherThread()()
	wg.Add(1)
	go c.initNewDir()
	wg.Wait()
}

func (c *Client) watcherThread() func() {
	return func() {
		err := c.watcher.Add(c.LocalDir)
		if err != nil {
			log.Fatal(err)
		}

		for {
			select {
			case event, ok := <-c.watcher.Events:
				if !ok {
					return
				}
				log.Println(event)

				if c.ShouldIgnore(event.Name) {
					log.Println("Ignoring file:", event.Name)
					continue
				}

				switch {
				case event.Op&fsnotify.Create == fsnotify.Create:
					log.Println("Detected new file or directory:", event.Name)
					c.cancelPendingDelete(event.Name)
					if fi, err := os.Stat(event.Name); err == nil && fi.IsDir() {
						if errDir := c.watcher.Add(event.Name); errDir != nil {
							log.Println("Error adding directory to watcher:", event.Name, errDir)
						} else {
							log.Println("Watching new dir " + event.Name)
						}
						continue
					}
					time.Sleep(2 * time.Second) // 确保文件已完全写入
					c.uploadChan <- syncTask{op: opUpload, path: event.Name}
				case event.Op&fsnotify.Write == fsnotify.Write:
					log.Println("Detected file change:", event.Name)
					c.cancelPendingDelete(event.Name)
					time.Sleep(2 * time.Second)
					c.uploadChan <- syncTask{op: opUpload, path: event.Name}
			case event.Op&fsnotify.Remove == fsnotify.Remove,
				event.Op&fsnotify.Rename == fsnotify.Rename:
				if !c.deletesEnabled() {
					log.Println("Delete propagation disabled, ignoring:", event.Name)
					continue
				}
					log.Println("Detected file removal/rename, scheduling delete:", event.Name)
					c.scheduleDelete(event.Name)
				}
			case err, ok := <-c.watcher.Errors:
				if !ok {
					return
				}
				log.Println("Error:", err)
			}
		}
	}
}

// scheduleDelete enqueues a remote-delete task after DeleteDebounce. A subsequent
// Create/Write for the same path within the window cancels the delete (atomic
// editor saves frequently produce Rename then Create).
func (c *Client) scheduleDelete(path string) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if existing, ok := c.pendingDeletes[path]; ok {
		existing.Stop()
	}
	delay := c.DeleteDebounce
	if delay <= 0 {
		delay = DefaultDebounce
	}
	c.pendingDeletes[path] = time.AfterFunc(delay, func() {
		c.pendingMu.Lock()
		delete(c.pendingDeletes, path)
		c.pendingMu.Unlock()
		c.uploadChan <- syncTask{op: opDelete, path: path}
	})
}

func (c *Client) cancelPendingDelete(path string) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if t, ok := c.pendingDeletes[path]; ok {
		t.Stop()
		delete(c.pendingDeletes, path)
		log.Println("Cancelled pending delete (atomic save):", path)
	}
}

func (c *Client) initNewDir() {
	files, err := c.CollectFiles(true)
	if err != nil {
		log.Fatal("Failed to collect files:", err)
	}
	for _, f := range files {
		c.uploadChan <- syncTask{op: opUpload, path: f}
	}
}

// CollectFiles walks LocalDir and returns the list of files to upload according to Mode.
// When addToWatcher is true, encountered directories are added to the fsnotify watcher.
func (c *Client) CollectFiles(addToWatcher bool) ([]string, error) {
	var filesToUpload []string

	err := filepath.Walk(c.LocalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if c.ShouldIgnore(path) {
			if info.IsDir() {
				log.Println("Skipping ignored directory:", path)
				return filepath.SkipDir
			}
			log.Println("Skipping ignored file:", path)
			return nil
		}
		if info.IsDir() {
			if addToWatcher && c.watcher != nil {
				if werr := c.watcher.Add(path); werr != nil {
					log.Println("Failed to add directory to watcher:", path, werr)
				}
			}
		} else if c.Mode == "all" {
			filesToUpload = append(filesToUpload, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	switch c.Mode {
	case "all":
		log.Printf("Collected files (all): %d", len(filesToUpload))
	default:
		return nil, fmt.Errorf("unknown mode: %s", c.Mode)
	}
	return filesToUpload, nil
}

// FullSync was an experimental one-shot sync helper; removed in favor of the
// always-on watcher which now propagates create/modify/delete events.

// remoteTargetPath returns the mapped target path for a given local file path.
func (c *Client) remoteTargetPath(localPath, baseDir string, target *RemoteTarget) (string, error) {
	rel, err := filepath.Rel(baseDir, localPath)
	if err != nil {
		return "", err
	}
	return c.MapPath(filepath.Join(target.TargetDir, rel)), nil
}

func (c *Client) uploadFile(filename string, baseDir string) error {
	activeTarget, err := c.GetActiveTarget()
	if err != nil {
		return err
	}

	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	err = writer.WriteField("token", activeTarget.Token)
	if err != nil {
		return err
	}
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

	// 应用路径映射
	mappedPath := c.MapPath(filepath.Join(activeTarget.TargetDir, relativePath))
	targetPath := mappedPath

	log.Printf("Uploading file to %s: %s", activeTarget.Name, targetPath)

	writer.WriteField("op", "upload")
	writer.WriteField("target", targetPath)
	contentType := writer.FormDataContentType()
	writer.Close()

	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}

	req, err := http.NewRequest("POST", activeTarget.URL, &requestBody)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	
	// 读取 body
	body, _ := io.ReadAll(resp.Body)

	log.Printf("upload res: %+v", string(body))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to upload file: %s", resp.Status)
	}

	return nil
}

// deleteRemote tells the active target to delete the file at the mapped target path.
func (c *Client) deleteRemote(filename string, baseDir string) error {
	activeTarget, err := c.GetActiveTarget()
	if err != nil {
		return err
	}
	targetPath, err := c.remoteTargetPath(filename, baseDir, activeTarget)
	if err != nil {
		return err
	}

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	if err := writer.WriteField("token", activeTarget.Token); err != nil {
		return err
	}
	if err := writer.WriteField("op", "delete"); err != nil {
		return err
	}
	if err := writer.WriteField("target", targetPath); err != nil {
		return err
	}
	contentType := writer.FormDataContentType()
	writer.Close()

	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequest("POST", activeTarget.URL, &requestBody)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)

	log.Printf("Deleting on %s: %s", activeTarget.Name, targetPath)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("delete res: %s", string(body))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to delete file: %s", resp.Status)
	}
	return nil
}

func (c *Client) worker(id int) {
	for task := range c.uploadChan {
		var err error
		switch task.op {
		case opDelete:
			err = c.deleteRemote(task.path, c.LocalDir)
		default:
			err = c.uploadFile(task.path, c.LocalDir)
		}
		if err != nil {
			log.Printf("Worker %d failed op=%d path=%s err=%v", id, task.op, task.path, err)
		}
	}
}
