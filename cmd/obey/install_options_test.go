package main

import (
	"io"
	"testing"
	"time"

	"github.com/JustSebNL/bend2will"
)

func TestInstallOptionsIncludeWindowsServiceSettings(t *testing.T) {
	fs := newFlagSet("install", io.Discard)
	var o options
	o.register(fs)
	args := []string{
		"TimeKeeper",
		"--command", "timekeeper.exe",
		"--service-account", "LocalService",
		"--enable",
		"--delayed-auto-start",
		"--restart-service-on-failure",
		"--service-restart-delay", "5s",
		"--log", `C:\TimeKeeper\service.log`,
		"--log-max-bytes", "10485760",
	}
	if err := parse(fs, args); err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg, err := o.config(fs, fs.Args())
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.LogPath != `C:\TimeKeeper\service.log` || cfg.LogMaxBytes != 10485760 {
		t.Fatalf("log config = (%q, %d)", cfg.LogPath, cfg.LogMaxBytes)
	}
	got, err := o.installOptions()
	if err != nil {
		t.Fatalf("installOptions: %v", err)
	}
	if !got.Enable || !got.WindowsDelayedAutoStart || !got.WindowsServiceRestartOnFailure {
		t.Fatalf("install options lost boolean flags: %+v", got)
	}
	if got.WindowsServiceAccount != "LocalService" || got.WindowsServiceRestartDelay != 5*time.Second {
		t.Fatalf("install options = %+v", got)
	}
}

func TestInstallOptionsRejectInvalidRestartDelay(t *testing.T) {
	fs := newFlagSet("install", io.Discard)
	var o options
	o.register(fs)
	if err := parse(fs, []string{"--service-restart-delay", "soon"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := o.installOptions(); err == nil {
		t.Fatal("expected invalid duration to be rejected")
	}
}

func TestWindowsServiceOptionsRequireWindowsPlatform(t *testing.T) {
	opts := bend2will.InstallOptions{
		WindowsServiceAccount: "LocalService",
	}
	if err := opts.Validate("systemd"); err == nil {
		t.Fatal("expected Windows-only options to be rejected on systemd")
	}
	if err := opts.Validate("windows service control manager"); err != nil {
		t.Fatalf("Windows options rejected on Windows: %v", err)
	}
}
