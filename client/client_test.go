package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestNewClient_Defaults(t *testing.T) {
	c := NewClient("all", "/tmp")
	if c.Mode != "all" || c.LocalDir != "/tmp" {
		t.Fatalf("unexpected fields: %+v", c)
	}
	if c.HTTPClient == nil {
		t.Fatal("HTTPClient should be initialized")
	}
	if cap(c.uploadChan) != NumWorkers {
		t.Fatalf("uploadChan capacity = %d, want %d", cap(c.uploadChan), NumWorkers)
	}
}

func TestAddRemoteTarget_FirstBecomesActive(t *testing.T) {
	c := NewClient("all", "/tmp")
	c.AddRemoteTarget("dev", "http://x/recv", "/r/dev", "tk1")
	if c.ActiveTarget != "dev" {
		t.Fatalf("first target should auto-activate, got %q", c.ActiveTarget)
	}
	c.AddRemoteTarget("prod", "http://y/recv", "/r/prod", "tk2")
	if c.ActiveTarget != "dev" {
		t.Fatalf("active target should remain 'dev', got %q", c.ActiveTarget)
	}
	if len(c.ListRemoteTargets()) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(c.ListRemoteTargets()))
	}
}

func TestSetActiveTarget(t *testing.T) {
	c := NewClient("all", "/tmp")
	c.AddRemoteTarget("dev", "http://x", "/r/dev", "tk1")
	c.AddRemoteTarget("prod", "http://y", "/r/prod", "tk2")

	if err := c.SetActiveTarget("prod"); err != nil {
		t.Fatalf("SetActiveTarget(prod) err=%v", err)
	}
	if c.ActiveTarget != "prod" {
		t.Fatalf("ActiveTarget=%q, want prod", c.ActiveTarget)
	}
	if err := c.SetActiveTarget("missing"); err == nil {
		t.Fatal("expected error for missing target")
	}
}

func TestDeletesEnabled_PerTargetExplicitTrue(t *testing.T) {
	c := NewClient("all", "/tmp")
	enabled := true
	c.AddRemoteTargetWithDeletes("dev", "http://x", "/r/dev", "tk1", &enabled)
	c.PropagateDeletes = false
	if !c.deletesEnabled() {
		t.Fatal("per-target explicit true should override global false")
	}
}

func TestDeletesEnabled_PerTargetExplicitFalse(t *testing.T) {
	c := NewClient("all", "/tmp")
	disabled := false
	c.AddRemoteTargetWithDeletes("dev", "http://x", "/r/dev", "tk1", &disabled)
	c.PropagateDeletes = true
	if c.deletesEnabled() {
		t.Fatal("per-target explicit false should override global true")
	}
}

func TestDeletesEnabled_FallsBackToGlobal(t *testing.T) {
	c := NewClient("all", "/tmp")
	c.AddRemoteTarget("dev", "http://x", "/r/dev", "tk1") // nil：未显式配置
	c.PropagateDeletes = true
	if !c.deletesEnabled() {
		t.Fatal("should fall back to global true when target unset")
	}
	c.PropagateDeletes = false
	if c.deletesEnabled() {
		t.Fatal("should fall back to global false when target unset")
	}
}

func TestGetActiveTarget(t *testing.T) {
	c := NewClient("all", "/tmp")
	if _, err := c.GetActiveTarget(); err == nil {
		t.Fatal("expected error when no targets")
	}
	c.AddRemoteTarget("dev", "http://x", "/r/dev", "tk1")
	tgt, err := c.GetActiveTarget()
	if err != nil || tgt.Name != "dev" || tgt.Token != "tk1" {
		t.Fatalf("active target wrong: %+v err=%v", tgt, err)
	}
}

func TestAddIgnorePattern_AnchorAuto(t *testing.T) {
	c := NewClient("all", "/tmp")
	c.AddIgnorePattern(".*/foo")
	if !c.ShouldIgnore("/a/b/foo") {
		t.Fatal("expected ignore for /a/b/foo")
	}
	if c.ShouldIgnore("/a/b/foo/bar") {
		t.Fatal("anchored pattern must not match prefixes")
	}
}

func TestAddIgnorePattern_Invalid(t *testing.T) {
	c := NewClient("all", "/tmp")
	c.AddIgnorePattern("[invalid(")
	if len(c.IgnorePatterns) != 0 {
		t.Fatalf("invalid pattern should not be added, got %d", len(c.IgnorePatterns))
	}
}

func TestShouldIgnore_Multiple(t *testing.T) {
	c := NewClient("all", "/tmp")
	c.AddIgnorePattern(".*/\\.git/.*")
	c.AddIgnorePattern(".*/node_modules")
	cases := map[string]bool{
		"/x/.git/HEAD":       true,
		"/x/node_modules":    true,
		"/x/src/main.go":     false,
		"/x/.gitignore":      false,
	}
	for p, want := range cases {
		if got := c.ShouldIgnore(p); got != want {
			t.Errorf("ShouldIgnore(%q)=%v want %v", p, got, want)
		}
	}
}

func TestMapPath(t *testing.T) {
	c := NewClient("all", "/tmp")
	if got := c.MapPath("/anything"); got != "/anything" {
		t.Fatalf("no mappings -> identity, got %q", got)
	}
	c.AddPathMapping("^/src(/.*)\\.js$", "/build$1.min.js")
	if got := c.MapPath("/src/app.js"); got != "/build/app.min.js" {
		t.Fatalf("mapping result = %q", got)
	}
	if got := c.MapPath("/other/file.txt"); got != "/other/file.txt" {
		t.Fatalf("non-matching path should pass through, got %q", got)
	}
}

func TestMapPath_InvalidRegexIgnored(t *testing.T) {
	c := NewClient("all", "/tmp")
	c.AddPathMapping("[bad(", "/x")
	if len(c.PathMappings) != 0 {
		t.Fatal("invalid mapping must not be added")
	}
}

func TestCollectFiles_AllMode(t *testing.T) {
	dir := t.TempDir()
	must := func(p, content string) {
		full := filepath.Join(dir, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("a.txt", "a")
	must("sub/b.txt", "b")
	must("ignored/c.txt", "c")

	c := NewClient("all", dir)
	c.AddIgnorePattern(".*/ignored")
	files, err := c.CollectFiles(false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range files {
		rel, _ := filepath.Rel(dir, f)
		got[rel] = true
	}
	if !got["a.txt"] || !got[filepath.Join("sub", "b.txt")] {
		t.Fatalf("missing expected files: %v", got)
	}
	if got[filepath.Join("ignored", "c.txt")] {
		t.Fatal("ignored file leaked into results")
	}
}

func TestCollectFiles_UnknownMode(t *testing.T) {
	dir := t.TempDir()
	c := NewClient("bogus", dir)
	if _, err := c.CollectFiles(false); err == nil {
		t.Fatal("expected error for unknown mode")
	}
}

func TestUploadFile_SendsCorrectFields(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "sub", "u.txt")
	_ = os.MkdirAll(filepath.Dir(fp), 0o755)
	if err := os.WriteFile(fp, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	type rec struct{ token, op, target, body string }
	var got rec
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		f, _, _ := r.FormFile("file")
		defer f.Close()
		b, _ := io.ReadAll(f)
		got = rec{
			token:  r.FormValue("token"),
			op:     r.FormValue("op"),
			target: r.FormValue("target"),
			body:   string(b),
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient("all", dir)
	c.AddRemoteTarget("dev", srv.URL, "/remote", "tk")
	if err := c.uploadFile(fp, dir); err != nil {
		t.Fatalf("uploadFile err=%v", err)
	}
	if got.token != "tk" || got.op != "upload" || got.body != "hello" {
		t.Fatalf("unexpected: %+v", got)
	}
	if !strings.HasSuffix(got.target, "/remote/sub/u.txt") {
		t.Fatalf("target=%q", got.target)
	}
}

func TestUploadFile_NoActiveTarget(t *testing.T) {
	c := NewClient("all", t.TempDir())
	if err := c.uploadFile("anything", t.TempDir()); err == nil {
		t.Fatal("expected error when no active target")
	}
}

func TestUploadFile_ServerError(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "x.txt")
	_ = os.WriteFile(fp, []byte("x"), 0o644)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient("all", dir)
	c.AddRemoteTarget("dev", srv.URL, "/remote", "tk")
	if err := c.uploadFile(fp, dir); err == nil {
		t.Fatal("expected upload error")
	}
}

func TestDeleteRemote_SendsCorrectFields(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "sub", "d.txt")

	type rec struct{ token, op, target string; hasFile bool }
	var got rec
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		_, _, ferr := r.FormFile("file")
		got = rec{
			token:   r.FormValue("token"),
			op:      r.FormValue("op"),
			target:  r.FormValue("target"),
			hasFile: ferr == nil,
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient("all", dir)
	c.AddRemoteTarget("dev", srv.URL, "/remote", "tk")
	if err := c.deleteRemote(fp, dir); err != nil {
		t.Fatalf("deleteRemote err=%v", err)
	}
	if got.token != "tk" || got.op != "delete" || got.hasFile {
		t.Fatalf("unexpected: %+v", got)
	}
	if !strings.HasSuffix(got.target, "/remote/sub/d.txt") {
		t.Fatalf("target=%q", got.target)
	}
}

func TestDeleteRemote_NoActiveTarget(t *testing.T) {
	c := NewClient("all", t.TempDir())
	if err := c.deleteRemote("/x", "/x"); err == nil {
		t.Fatal("expected error when no active target")
	}
}

func TestDeleteRemote_ServerError(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewClient("all", dir)
	c.AddRemoteTarget("dev", srv.URL, "/remote", "tk")
	if err := c.deleteRemote(filepath.Join(dir, "x.txt"), dir); err == nil {
		t.Fatal("expected error")
	}
}

func TestScheduleDelete_FiresAfterDebounce(t *testing.T) {
	c := NewClient("all", t.TempDir())
	c.uploadChan = make(chan syncTask, 4)
	c.DeleteDebounce = 30 * time.Millisecond

	c.scheduleDelete("/x/a.txt")
	select {
	case task := <-c.uploadChan:
		if task.op != opDelete || task.path != "/x/a.txt" {
			t.Fatalf("got %+v", task)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("delete task not enqueued")
	}
}

func TestScheduleDelete_LatestReplacesPending(t *testing.T) {
	c := NewClient("all", t.TempDir())
	c.uploadChan = make(chan syncTask, 4)
	c.DeleteDebounce = 50 * time.Millisecond

	c.scheduleDelete("/x/a.txt")
	c.scheduleDelete("/x/a.txt") // should replace, not duplicate
	time.Sleep(150 * time.Millisecond)

	count := 0
loop:
	for {
		select {
		case <-c.uploadChan:
			count++
		default:
			break loop
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 enqueued delete, got %d", count)
	}
}

func TestCancelPendingDelete_PreventsDelete(t *testing.T) {
	c := NewClient("all", t.TempDir())
	c.uploadChan = make(chan syncTask, 4)
	c.DeleteDebounce = 80 * time.Millisecond

	c.scheduleDelete("/x/a.txt")
	c.cancelPendingDelete("/x/a.txt")
	time.Sleep(150 * time.Millisecond)
	select {
	case task := <-c.uploadChan:
		t.Fatalf("delete should have been cancelled, got %+v", task)
	default:
	}
}

func TestCancelPendingDelete_NoOpWhenAbsent(t *testing.T) {
	c := NewClient("all", t.TempDir())
	c.cancelPendingDelete("/never/scheduled")
}

func TestWorker_UploadsFromChannel(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "w.txt")
	if err := os.WriteFile(fp, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		w.WriteHeader(200)
		select {
		case done <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()

	c := NewClient("all", dir)
	c.AddRemoteTarget("dev", srv.URL, "/remote", "tk")
	go c.worker(1)
	c.uploadChan <- syncTask{op: opUpload, path: fp}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not deliver upload")
	}
	close(c.uploadChan)
}

func TestWorker_DispatchesDelete(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "d.txt")

	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		select {
		case got <- r.FormValue("op"):
		default:
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient("all", dir)
	c.AddRemoteTarget("dev", srv.URL, "/remote", "tk")
	go c.worker(1)
	c.uploadChan <- syncTask{op: opDelete, path: fp}
	select {
	case op := <-got:
		if op != "delete" {
			t.Fatalf("op=%q want delete", op)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not deliver delete")
	}
	close(c.uploadChan)
}

func TestWorker_ContinuesOnUploadError(t *testing.T) {
	dir := t.TempDir()
	c := NewClient("all", dir)
	go c.worker(1)
	c.uploadChan <- syncTask{op: opUpload, path: filepath.Join(dir, "missing.txt")}
	close(c.uploadChan)
}

func TestGetGitDiffFiles_NotGitRepoReturnsErr(t *testing.T) {
	dir := t.TempDir()
	if _, err := getGitDiffFiles(dir); err == nil {
		t.Fatal("expected error in non-git directory")
	}
}

func TestGetGitDiffFiles_GitMain(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@x")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("commit", "-aq", "-m", "change")

	files, err := getGitDiffFiles(dir)
	if err != nil {
		t.Fatalf("getGitDiffFiles err=%v", err)
	}
	found := false
	for _, f := range files {
		if strings.HasSuffix(f, "a.txt") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a.txt in diff, got %v", files)
	}
}

func TestCollectFiles_GitMode(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@x")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skip.log"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("k2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skip.log"), []byte("s2"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("commit", "-aq", "-m", "ch")

	c := NewClient("git", dir)
	c.AddIgnorePattern(".*\\.log")
	files, err := c.CollectFiles(false)
	if err != nil {
		t.Fatalf("CollectFiles err=%v", err)
	}
	hasKeep, hasSkip := false, false
	for _, f := range files {
		if strings.HasSuffix(f, "keep.txt") {
			hasKeep = true
		}
		if strings.HasSuffix(f, "skip.log") {
			hasSkip = true
		}
	}
	if !hasKeep {
		t.Fatalf("expected keep.txt in git diff, got %v", files)
	}
	if hasSkip {
		t.Fatalf("ignored file leaked: %v", files)
	}
}

func TestCollectFiles_AddsDirsToWatcher(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	c := NewClient("all", dir)
	c.watcher = w
	if _, err := c.CollectFiles(true); err != nil {
		t.Fatal(err)
	}
	watched := w.WatchList()
	wantDir := dir
	wantSub := sub
	foundDir, foundSub := false, false
	for _, p := range watched {
		if p == wantDir {
			foundDir = true
		}
		if p == wantSub {
			foundSub = true
		}
	}
	if !foundDir || !foundSub {
		t.Fatalf("watcher missing entries: %v", watched)
	}
}

func TestCollectFiles_BadLocalDir(t *testing.T) {
	c := NewClient("all", "/nonexistent-path-xyz-123")
	if _, err := c.CollectFiles(false); err == nil {
		t.Fatal("expected error for missing dir")
	}
}

func TestInitNewDir_FeedsUploadChan(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	c := NewClient("all", dir)
	c.watcher = w
	c.uploadChan = make(chan syncTask, 8)

	done := make(chan struct{})
	go func() { c.initNewDir(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("initNewDir did not finish")
	}
	close(c.uploadChan)
	got := 0
	for range c.uploadChan {
		got++
	}
	if got != 2 {
		t.Fatalf("uploadChan saw %d files, want 2", got)
	}
}

func TestWatcherThread_DetectsCreate(t *testing.T) {
	dir := t.TempDir()
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient("all", dir)
	c.watcher = w
	c.uploadChan = make(chan syncTask, 8)

	go c.watcherThread()()

	time.Sleep(100 * time.Millisecond)
	target := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case task := <-c.uploadChan:
		if task.op != opUpload || task.path != target {
			t.Fatalf("uploadChan got %+v want upload %q", task, target)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not emit upload event")
	}
	w.Close()
}

func TestWatcherThread_RemoveSchedulesDelete(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "doomed.txt")
	if err := os.WriteFile(target, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient("all", dir)
	c.watcher = w
	c.uploadChan = make(chan syncTask, 8)
	c.PropagateDeletes = true
	c.DeleteDebounce = 30 * time.Millisecond

	go c.watcherThread()()
	time.Sleep(50 * time.Millisecond)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case task := <-c.uploadChan:
			if task.op == opDelete && task.path == target {
				w.Close()
				return
			}
		case <-deadline:
			w.Close()
			t.Fatal("did not observe scheduled delete")
		}
	}
}

func TestWatcherThread_RemoveIgnoredWhenPropagateDisabled(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(target, []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient("all", dir)
	c.watcher = w
	c.uploadChan = make(chan syncTask, 8)
	// PropagateDeletes left at zero value (false).
	c.DeleteDebounce = 30 * time.Millisecond

	go c.watcherThread()()
	time.Sleep(50 * time.Millisecond)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	w.Close()
	close(c.uploadChan)
	for task := range c.uploadChan {
		if task.op == opDelete {
			t.Fatalf("delete should not propagate when disabled, got %+v", task)
		}
	}
}

func TestWatcherThread_PerTargetEnabledSchedulesDelete(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "doomed.txt")
	if err := os.WriteFile(target, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient("all", dir)
	c.watcher = w
	c.uploadChan = make(chan syncTask, 8)
	// 全局关闭，per-target 显式开启：删除仍应传播
	enabled := true
	c.AddRemoteTargetWithDeletes("dev", "http://x", "/r/dev", "tk1", &enabled)
	c.DeleteDebounce = 30 * time.Millisecond

	go c.watcherThread()()
	time.Sleep(50 * time.Millisecond)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case task := <-c.uploadChan:
			if task.op == opDelete && task.path == target {
				w.Close()
				return
			}
		case <-deadline:
			w.Close()
			t.Fatal("did not observe scheduled delete")
		}
	}
}

func TestWatcherThread_PerTargetDisabledOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(target, []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient("all", dir)
	c.watcher = w
	c.uploadChan = make(chan syncTask, 8)
	// 全局开启，per-target 显式关闭：删除不应传播
	disabled := false
	c.AddRemoteTargetWithDeletes("dev", "http://x", "/r/dev", "tk1", &disabled)
	c.PropagateDeletes = true
	c.DeleteDebounce = 30 * time.Millisecond

	go c.watcherThread()()
	time.Sleep(50 * time.Millisecond)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	w.Close()
	close(c.uploadChan)
	for task := range c.uploadChan {
		if task.op == opDelete {
			t.Fatalf("delete should not propagate when per-target disabled, got %+v", task)
		}
	}
}

func TestWatcherThread_AtomicSaveCancelsDelete(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "atomic.txt")
	if err := os.WriteFile(real, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient("all", dir)
	c.watcher = w
	c.uploadChan = make(chan syncTask, 8)
	c.PropagateDeletes = true
	c.DeleteDebounce = 200 * time.Millisecond

	go c.watcherThread()()
	time.Sleep(50 * time.Millisecond)

	// Simulate atomic save: remove then immediately recreate within debounce.
	if err := os.Remove(real); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(real, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wait past the debounce window.
	time.Sleep(400 * time.Millisecond)
	w.Close()

	// Drain channel; must contain no opDelete for this path.
	close(c.uploadChan)
	for task := range c.uploadChan {
		if task.op == opDelete && task.path == real {
			t.Fatal("atomic save should have cancelled the delete")
		}
	}
}
