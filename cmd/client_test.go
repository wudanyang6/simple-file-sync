package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resetFlags() {
	ClientUploadMode = ""
	ClientLocalDir = ""
	ClientRemoteDir = ""
	ClientServerAddr = ""
	ClientServerToken = ""
	ClientIgnorePatterns = nil
	ClientPathMappings = nil
	ClientConfigFile = ""
	ClientTargetName = ""
	ClientPropagateDeletes = false
	clientPropagateDeletesSet = false
}

func writeTOML(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfig_FromFile(t *testing.T) {
	resetFlags()
	dir := t.TempDir()
	body := `mode = "all"
local_dir = "/tmp/x"
active_target = "dev"

[[remote_targets]]
name = "dev"
server_addr = "http://x/recv"
remote_dir = "/r"
token = "tk"
`
	path := writeTOML(t, dir, "c.toml", body)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "all" || cfg.LocalDir != "/tmp/x" || cfg.ActiveTarget != "dev" {
		t.Fatalf("unexpected: %+v", cfg)
	}
	if len(cfg.RemoteTargets) != 1 || cfg.RemoteTargets[0].Token != "tk" {
		t.Fatalf("targets: %+v", cfg.RemoteTargets)
	}
}

func TestLoadConfig_DefaultMissingReturnsEmpty(t *testing.T) {
	resetFlags()
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	os.Chdir(dir)

	cfg, err := loadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "" || len(cfg.RemoteTargets) != 0 {
		t.Fatalf("expected empty cfg, got %+v", cfg)
	}
}

func TestLoadConfig_ExplicitMissingErrors(t *testing.T) {
	resetFlags()
	ClientConfigFile = "/no/such/file.toml"
	if _, err := loadConfig(ClientConfigFile); err == nil {
		t.Fatal("expected error for missing explicit config")
	}
}

func TestLoadConfig_BadTOMLErrors(t *testing.T) {
	resetFlags()
	dir := t.TempDir()
	path := writeTOML(t, dir, "bad.toml", "this is = not valid = toml = [[[")
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestOverrideConfigWithFlags_BasicFields(t *testing.T) {
	resetFlags()
	cfg := &ClientConfig{Mode: "all", LocalDir: "/old"}
	ClientUploadMode = "git"
	ClientLocalDir = "/new"
	ClientTargetName = "prod"
	overrideConfigWithFlags(cfg)
	if cfg.Mode != "git" || cfg.LocalDir != "/new" || cfg.ActiveTarget != "prod" {
		t.Fatalf("override failed: %+v", cfg)
	}
}

func TestOverrideConfigWithFlags_AddsCmdLineTarget(t *testing.T) {
	resetFlags()
	cfg := &ClientConfig{}
	ClientRemoteDir = "/r"
	ClientServerAddr = "http://s"
	ClientServerToken = "tk"
	overrideConfigWithFlags(cfg)
	if len(cfg.RemoteTargets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(cfg.RemoteTargets))
	}
	got := cfg.RemoteTargets[0]
	if got.RemoteDir != "/r" || got.ServerAddr != "http://s" || got.Token != "tk" {
		t.Fatalf("target wrong: %+v", got)
	}
	if !strings.HasPrefix(got.Name, "cmdline_") {
		t.Fatalf("name should be cmdline_*: %q", got.Name)
	}
	if cfg.ActiveTarget != got.Name {
		t.Fatalf("active=%q want %q", cfg.ActiveTarget, got.Name)
	}
}

func TestOverrideConfigWithFlags_PartialTargetIgnored(t *testing.T) {
	resetFlags()
	cfg := &ClientConfig{}
	ClientRemoteDir = "/r"
	overrideConfigWithFlags(cfg)
	if len(cfg.RemoteTargets) != 0 {
		t.Fatalf("partial flags should not create target, got %+v", cfg.RemoteTargets)
	}
}

func TestOverrideConfigWithFlags_AppendsIgnoreAndMapping(t *testing.T) {
	resetFlags()
	cfg := &ClientConfig{Ignore: []string{"a"}, PathMappings: []string{"x:y"}}
	ClientIgnorePatterns = []string{"b", "c"}
	ClientPathMappings = []string{"m:n"}
	overrideConfigWithFlags(cfg)
	if len(cfg.Ignore) != 3 || cfg.Ignore[0] != "a" {
		t.Fatalf("ignore=%v", cfg.Ignore)
	}
	if len(cfg.PathMappings) != 2 {
		t.Fatalf("mappings=%v", cfg.PathMappings)
	}
}

func TestValidateConfig_Defaults(t *testing.T) {
	cfg := &ClientConfig{
		LocalDir:      "/tmp",
		RemoteTargets: []RemoteTargetConfig{{Name: "a", ServerAddr: "x", RemoteDir: "/r"}},
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "all" {
		t.Fatalf("default mode=%q want all", cfg.Mode)
	}
	if cfg.ActiveTarget != "a" {
		t.Fatalf("default active=%q", cfg.ActiveTarget)
	}
}

func TestValidateConfig_InvalidMode(t *testing.T) {
	cfg := &ClientConfig{Mode: "weird", LocalDir: "/tmp"}
	if err := validateConfig(cfg); err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestValidateConfig_MissingLocalDir(t *testing.T) {
	cfg := &ClientConfig{Mode: "all"}
	if err := validateConfig(cfg); err == nil {
		t.Fatal("expected error for missing local_dir")
	}
}

func TestValidateConfig_NoTargets(t *testing.T) {
	cfg := &ClientConfig{Mode: "all", LocalDir: "/tmp"}
	if err := validateConfig(cfg); err == nil {
		t.Fatal("expected error when no remote_targets")
	}
}

func TestValidateConfig_TargetMissingFields(t *testing.T) {
	cfg := &ClientConfig{
		Mode:          "all",
		LocalDir:      "/tmp",
		RemoteTargets: []RemoteTargetConfig{{Name: "a"}},
	}
	if err := validateConfig(cfg); err == nil {
		t.Fatal("expected error for missing server_addr")
	}
	cfg.RemoteTargets[0].ServerAddr = "x"
	if err := validateConfig(cfg); err == nil {
		t.Fatal("expected error for missing remote_dir")
	}
}

func TestValidateConfig_ActiveTargetNotFound(t *testing.T) {
	cfg := &ClientConfig{
		Mode:          "all",
		LocalDir:      "/tmp",
		ActiveTarget:  "missing",
		RemoteTargets: []RemoteTargetConfig{{Name: "a", ServerAddr: "x", RemoteDir: "/r"}},
	}
	if err := validateConfig(cfg); err == nil {
		t.Fatal("expected error when active_target not in targets")
	}
}

func TestBuildClient_OK(t *testing.T) {
	cfg := &ClientConfig{
		Mode:         "all",
		LocalDir:     "/tmp/x",
		ActiveTarget: "dev",
		Ignore:       []string{".*\\.log$"},
		PathMappings: []string{"^/a:/b"},
		RemoteTargets: []RemoteTargetConfig{
			{Name: "dev", ServerAddr: "http://x", RemoteDir: "/r", Token: "tk"},
		},
	}
	c, err := buildClient(cfg)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if c.Mode != "all" || c.LocalDir != "/tmp/x" {
		t.Fatalf("client base wrong: %+v", c)
	}
	if c.ActiveTarget != "dev" {
		t.Fatalf("active=%q", c.ActiveTarget)
	}
	if len(c.IgnorePatterns) != 1 {
		t.Fatalf("ignore=%v", c.IgnorePatterns)
	}
	// AddPathMapping is called once explicitly + once for base dir mapping.
	if len(c.PathMappings) != 2 {
		t.Fatalf("mappings=%d want 2", len(c.PathMappings))
	}
}

func TestBuildClient_InvalidActiveTarget(t *testing.T) {
	cfg := &ClientConfig{
		Mode:          "all",
		LocalDir:      "/tmp",
		ActiveTarget:  "nope",
		RemoteTargets: []RemoteTargetConfig{{Name: "dev", ServerAddr: "x", RemoteDir: "/r"}},
	}
	if _, err := buildClient(cfg); err == nil {
		t.Fatal("expected error for unknown active target")
	}
}

func TestBuildClient_BadMappingIgnored(t *testing.T) {
	cfg := &ClientConfig{
		Mode:         "all",
		LocalDir:     "/tmp",
		ActiveTarget: "dev",
		PathMappings: []string{"no-colon-here"},
		RemoteTargets: []RemoteTargetConfig{
			{Name: "dev", ServerAddr: "x", RemoteDir: "/r"},
		},
	}
	c, err := buildClient(cfg)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	// Only the auto base-dir mapping should be present.
	if len(c.PathMappings) != 1 {
		t.Fatalf("mappings=%d want 1", len(c.PathMappings))
	}
}

func TestBuildClient_NoLocalDirSkipsBaseMapping(t *testing.T) {
	cfg := &ClientConfig{
		Mode:         "all",
		ActiveTarget: "dev",
		RemoteTargets: []RemoteTargetConfig{
			{Name: "dev", ServerAddr: "x", RemoteDir: "/r"},
		},
	}
	c, err := buildClient(cfg)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(c.PathMappings) != 0 {
		t.Fatalf("expected no mappings, got %d", len(c.PathMappings))
	}
}

func TestLoadConfig_PropagateDeletesFromTOML(t *testing.T) {
	resetFlags()
	dir := t.TempDir()
	body := `mode = "all"
local_dir = "/tmp/x"
active_target = "dev"
propagate_deletes = true

[[remote_targets]]
name = "dev"
server_addr = "http://x"
remote_dir = "/r"
token = "tk"
`
	path := writeTOML(t, dir, "p.toml", body)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PropagateDeletes {
		t.Fatal("expected propagate_deletes=true from TOML")
	}
}

func TestOverrideConfigWithFlags_PropagateDeletesRespectsSet(t *testing.T) {
	resetFlags()
	cfg := &ClientConfig{PropagateDeletes: true}
	// Flag default is false, but Set marker is false → must NOT override.
	overrideConfigWithFlags(cfg)
	if !cfg.PropagateDeletes {
		t.Fatal("flag not explicitly set should not override config")
	}

	// Now simulate user explicitly passing --propagate-deletes=false.
	cfg2 := &ClientConfig{PropagateDeletes: true}
	clientPropagateDeletesSet = true
	ClientPropagateDeletes = false
	overrideConfigWithFlags(cfg2)
	if cfg2.PropagateDeletes {
		t.Fatal("explicit flag false should override config true")
	}
}

func TestBuildClient_PropagatesDeleteFlag(t *testing.T) {
	cfg := &ClientConfig{
		Mode:             "all",
		LocalDir:         "/tmp/x",
		ActiveTarget:     "dev",
		PropagateDeletes: true,
		RemoteTargets: []RemoteTargetConfig{
			{Name: "dev", ServerAddr: "x", RemoteDir: "/r"},
		},
	}
	c, err := buildClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !c.PropagateDeletes {
		t.Fatal("client.PropagateDeletes should be true")
	}
}
