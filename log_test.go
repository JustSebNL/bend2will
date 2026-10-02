package bend2will

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLogFileRotatesAndKeepsOneBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	logger, closer, err := NewLogger("test", path, "info", 8)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer closer.Close()
	writer := logger.(*logFileLogger)

	if _, err := writer.Write([]byte("12345678")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if _, err := writer.Write([]byte("abcdefgh")); err != nil {
		t.Fatalf("third write: %v", err)
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current log: %v", err)
	}
	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read backup log: %v", err)
	}
	if string(current) != "abcdefgh" || string(backup) != "x" {
		t.Fatalf("current=%q backup=%q", current, backup)
	}
}

func TestConfigRejectsNegativeLogMaxBytes(t *testing.T) {
	cfg := DefaultConfig("test", "child")
	cfg.LogMaxBytes = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected negative LogMaxBytes to be rejected")
	}
}

func TestConfigFileParsesLogMaxBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "obey.conf")
	data := "[app]\ncommand = child\nlog = service.log\nlog_max_bytes = 10485760\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfigFile(path, "app")
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if cfg.LogMaxBytes != 10485760 {
		t.Errorf("LogMaxBytes = %d, want 10485760", cfg.LogMaxBytes)
	}
}
