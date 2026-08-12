package server

import (
	"bytes"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newServer(t *testing.T, token string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	return NewServer(0, token, dir), dir
}

func buildUpload(t *testing.T, fields map[string]string, fileName, fileBody string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if fileName != "" {
		fw, err := w.CreateFormFile("file", fileName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(fw, fileBody); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}

func TestNewServer(t *testing.T) {
	s := NewServer(8080, "tk", "/x")
	if s.Port != 8080 || s.Token != "tk" || s.LimitDir != "/x" {
		t.Fatalf("unexpected: %+v", s)
	}
}

func TestUpload_RejectsNonPOST(t *testing.T) {
	s, _ := newServer(t, "tk")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/receiver", nil)
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rr.Code)
	}
}

func TestUpload_BadToken(t *testing.T) {
	s, _ := newServer(t, "secret")
	body, ct := buildUpload(t, map[string]string{"token": "wrong", "target": "/whatever"}, "f.txt", "x")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rr.Code)
	}
}

func TestUpload_EmptyTokenAllowsAnything(t *testing.T) {
	s, dir := newServer(t, "")
	target := filepath.Join(dir, "any.txt")
	body, ct := buildUpload(t, map[string]string{"target": target}, "f.txt", "hello")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestUpload_MissingFile(t *testing.T) {
	s, dir := newServer(t, "tk")
	body, ct := buildUpload(t, map[string]string{"token": "tk", "target": filepath.Join(dir, "x.txt")}, "", "")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
}

func TestUpload_MissingTarget(t *testing.T) {
	s, _ := newServer(t, "tk")
	body, ct := buildUpload(t, map[string]string{"token": "tk"}, "f.txt", "x")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
}

func TestUpload_RelativeTargetRejected(t *testing.T) {
	s, _ := newServer(t, "tk")
	body, ct := buildUpload(t, map[string]string{"token": "tk", "target": "rel/path.txt"}, "f.txt", "x")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
}

func TestUpload_OutsideLimitDirRejected(t *testing.T) {
	s, _ := newServer(t, "tk")
	body, ct := buildUpload(t, map[string]string{"token": "tk", "target": "/etc/passwd"}, "f.txt", "x")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Invalid target path") {
		t.Fatalf("body=%q", rr.Body.String())
	}
}

func TestUpload_Success_WritesFileAndCreatesDirs(t *testing.T) {
	s, dir := newServer(t, "tk")
	target := filepath.Join(dir, "deep", "nested", "out.txt")
	body, ct := buildUpload(t, map[string]string{"token": "tk", "target": target}, "out.txt", "payload")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("file content=%q", string(got))
	}
}

func TestUpload_Success_OverwritesExisting(t *testing.T) {
	s, dir := newServer(t, "tk")
	target := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, ct := buildUpload(t, map[string]string{"token": "tk", "target": target}, "x", "new")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new" {
		t.Fatalf("not overwritten: %q", got)
	}
}

func TestUpload_TargetIsDirectoryFails(t *testing.T) {
	s, dir := newServer(t, "tk")
	target := dir
	body, ct := buildUpload(t, map[string]string{"token": "tk", "target": target}, "x", "data")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rr.Code)
	}
}

func TestUpload_MkdirAllFails(t *testing.T) {
	s, dir := newServer(t, "tk")
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("file-not-dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(blocker, "below", "x.txt")
	body, ct := buildUpload(t, map[string]string{"token": "tk", "target": target}, "x", "data")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rr.Code)
	}
}

func TestHandler_RoutesReceiver(t *testing.T) {
	s, dir := newServer(t, "tk")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	target := filepath.Join(dir, "h.txt")
	body, ct := buildUpload(t, map[string]string{"token": "tk", "target": target}, "x", "via-handler")
	resp, err := http.Post(ts.URL+"/receiver", ct, body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(b))
	}
	got, _ := os.ReadFile(target)
	if string(got) != "via-handler" {
		t.Fatalf("got %q", got)
	}
}

func TestHandler_404OnUnknownPath(t *testing.T) {
	s, _ := newServer(t, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want 404", resp.StatusCode)
	}
}

func TestStart_WithInjectedListener(t *testing.T) {
	s, dir := newServer(t, "tk")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	s.Listener = ln

	done := make(chan struct{})
	go func() { s.Start(); close(done) }()

	target := filepath.Join(dir, "started.txt")
	body, ct := buildUpload(t, map[string]string{"token": "tk", "target": target}, "x", "started")
	resp, err := http.Post("http://"+addr+"/receiver", ct, body)
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		ln.Close()
		t.Fatalf("status=%d", resp.StatusCode)
	}

	ln.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return")
	}
}

func TestServe_ServesUntilListenerClosed(t *testing.T) {
	s, dir := newServer(t, "tk")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	done := make(chan error, 1)
	go func() { done <- s.Serve(ln) }()

	target := filepath.Join(dir, "served.txt")
	body, ct := buildUpload(t, map[string]string{"token": "tk", "target": target}, "x", "served")
	resp, err := http.Post("http://"+addr+"/receiver", ct, body)
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		ln.Close()
		t.Fatalf("status=%d", resp.StatusCode)
	}

	ln.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after listener close")
	}

	got, _ := os.ReadFile(target)
	if string(got) != "served" {
		t.Fatalf("file content=%q", got)
	}
}

func TestUpload_OpDelete_RemovesFile(t *testing.T) {
	s, dir := newServer(t, "tk")
	target := filepath.Join(dir, "doomed.txt")
	if err := os.WriteFile(target, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, ct := buildUpload(t, map[string]string{"token": "tk", "op": "delete", "target": target}, "", "")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file should be gone, stat err=%v", err)
	}
}

func TestUpload_OpDelete_MissingFileIsOK(t *testing.T) {
	s, dir := newServer(t, "tk")
	target := filepath.Join(dir, "ghost.txt")
	body, ct := buildUpload(t, map[string]string{"token": "tk", "op": "delete", "target": target}, "", "")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestUpload_OpDelete_BadTokenRejected(t *testing.T) {
	s, dir := newServer(t, "tk")
	target := filepath.Join(dir, "x.txt")
	_ = os.WriteFile(target, []byte("x"), 0o644)
	body, ct := buildUpload(t, map[string]string{"token": "wrong", "op": "delete", "target": target}, "", "")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rr.Code)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("file should still exist: %v", err)
	}
}

func TestUpload_OpDelete_OutsideLimitDirRejected(t *testing.T) {
	s, _ := newServer(t, "tk")
	body, ct := buildUpload(t, map[string]string{"token": "tk", "op": "delete", "target": "/etc/passwd"}, "", "")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
}

func TestUpload_OpDelete_RelativeTargetRejected(t *testing.T) {
	s, _ := newServer(t, "tk")
	body, ct := buildUpload(t, map[string]string{"token": "tk", "op": "delete", "target": "rel/x.txt"}, "", "")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
}

func TestUpload_OpDelete_MissingTarget(t *testing.T) {
	s, _ := newServer(t, "tk")
	body, ct := buildUpload(t, map[string]string{"token": "tk", "op": "delete"}, "", "")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
}

func TestUpload_OpDelete_TargetIsDirectoryFails(t *testing.T) {
	s, dir := newServer(t, "tk")
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "blocker.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, ct := buildUpload(t, map[string]string{"token": "tk", "op": "delete", "target": sub}, "", "")
	req := httptest.NewRequest(http.MethodPost, "/receiver", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	s.uploadHandler(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rr.Code)
	}
}