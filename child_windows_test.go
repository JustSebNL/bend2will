package bend2will

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPrepareChildIOUsesRotatingLogger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	cfg := DefaultConfig("test", "child.exe")
	cfg.LogPath = path
	cfg.LogMaxBytes = 8
	logger, closer, err := NewLogger(cfg.Name, cfg.LogPath, cfg.LogLevel, cfg.LogMaxBytes)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer closer.Close()

	cmd := &exec.Cmd{}
	childCloser, err := prepareChildIOWithConsole(cmd, &cfg, logger, false)
	if err != nil {
		t.Fatalf("prepareChildIOWithConsole: %v", err)
	}
	if childCloser != nil {
		defer childCloser.Close()
	}
	if _, err := cmd.Stdout.Write([]byte("12345678")); err != nil {
		t.Fatalf("write child stdout: %v", err)
	}
	if _, err := cmd.Stderr.Write([]byte("x")); err != nil {
		t.Fatalf("write child stderr: %v", err)
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current log: %v", err)
	}
	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read backup log: %v", err)
	}
	if string(current) != "x" || string(backup) != "12345678" {
		t.Fatalf("current=%q backup=%q", current, backup)
	}
}
