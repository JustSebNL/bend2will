package main

import (
	"context"
	"testing"

	"github.com/JustSebNL/bend2will"
)

type restartTestPlatform struct {
	events []string
	states []bend2will.Status
}

func (p *restartTestPlatform) Name() string { return "test" }
func (p *restartTestPlatform) Connect(context.Context, *bend2will.Config, bend2will.Mode, bend2will.SuperviseFunc, *bend2will.Stopper) (bool, error) {
	return false, nil
}
func (p *restartTestPlatform) ReportStatus(bend2will.Status) error { return nil }
func (p *restartTestPlatform) Definition(*bend2will.Config) (bend2will.Definition, error) {
	return bend2will.Definition{}, nil
}
func (p *restartTestPlatform) Install(context.Context, *bend2will.Config, bend2will.InstallOptions) error {
	return nil
}
func (p *restartTestPlatform) Uninstall(context.Context, string, bool) error { return nil }
func (p *restartTestPlatform) Start(context.Context, string) error {
	p.events = append(p.events, "start")
	return nil
}
func (p *restartTestPlatform) Stop(context.Context, string) error {
	p.events = append(p.events, "stop")
	return nil
}
func (p *restartTestPlatform) Status(context.Context, string) (bend2will.ServiceStatus, error) {
	p.events = append(p.events, "status")
	state := bend2will.StatusStopped
	if len(p.states) > 0 {
		state, p.states = p.states[0], p.states[1:]
	}
	return bend2will.ServiceStatus{State: state}, nil
}
func (p *restartTestPlatform) Close() error { return nil }

func TestRestartStopsWaitsForStoppedThenStarts(t *testing.T) {
	platform := &restartTestPlatform{states: []bend2will.Status{
		bend2will.StatusStopping,
		bend2will.StatusStopped,
		bend2will.StatusStarting,
		bend2will.StatusRunning,
	}}
	if err := restartService(context.Background(), platform, "demo"); err != nil {
		t.Fatalf("restartService: %v", err)
	}
	want := []string{"stop", "status", "status", "start", "status", "status"}
	if len(platform.events) != len(want) {
		t.Fatalf("events = %v, want %v", platform.events, want)
	}
	for i := range want {
		if platform.events[i] != want[i] {
			t.Fatalf("events = %v, want %v", platform.events, want)
		}
	}
}
