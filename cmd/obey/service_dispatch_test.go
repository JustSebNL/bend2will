package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JustSebNL/bend2will"
)

type serviceDispatchTestPlatform struct {
	restartTestPlatform
	config bend2will.Config
	mode   bend2will.Mode
}

func (p *serviceDispatchTestPlatform) Connect(_ context.Context, cfg *bend2will.Config, mode bend2will.Mode, _ bend2will.SuperviseFunc, _ *bend2will.Stopper) (bool, error) {
	p.config = *cfg
	p.mode = mode
	return true, nil
}

func TestServiceCommandDispatchesToServiceMode(t *testing.T) {
	stdout, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()

	platform := &serviceDispatchTestPlatform{}
	arguments := strings.Join([]string{"-addr", "127.0.0.1:1618", "-db", `D:\state\timekeeper.db`}, "\x1f")
	code := run([]string{"service", "--name", "TimeKeeper", "--command", "timekeeper.exe", "--arguments", arguments, "--mode", "service"}, stdout, stderr, platform)
	if code != exitOK {
		t.Fatalf("service command exit = %d", code)
	}
	if platform.mode != bend2will.ModeService {
		t.Fatalf("service mode = %v, want %v", platform.mode, bend2will.ModeService)
	}
	if platform.config.Name != "TimeKeeper" || filepath.Base(platform.config.Command) != "timekeeper.exe" {
		t.Fatalf("service config = %#v", platform.config)
	}
	if len(platform.config.Args) != 4 || platform.config.Args[0] != "-addr" || platform.config.Args[2] != "-db" || platform.config.Args[3] != `D:\state\timekeeper.db` {
		t.Fatalf("service args = %#v", platform.config.Args)
	}
}
