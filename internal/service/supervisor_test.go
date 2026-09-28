package service

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSupervisorWaitReturnsOnceEveryLoopHasStopped(t *testing.T) {
	supervisor := NewSupervisor()
	release := make(chan struct{})
	stopped := make(chan string, 2)
	for _, name := range []string{"worker-0", "scheduler"} {
		supervisor.Go(name, func() {
			<-release
			time.Sleep(50 * time.Millisecond)
			stopped <- name
		})
	}

	waited := make(chan error, 1)
	go func() { waited <- supervisor.Wait(context.Background()) }()
	select {
	case err := <-waited:
		t.Fatalf("Wait returned %v while both loops were running", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("Wait = %v, want nil once both loops returned", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after both loops stopped")
	}
	if len(stopped) != 2 {
		t.Fatalf("Wait returned with %d of 2 loops stopped", len(stopped))
	}
}

func TestSupervisorWaitNamesTheLoopsThatOutliveTheDeadline(t *testing.T) {
	supervisor := NewSupervisor()
	block := make(chan struct{})
	defer close(block)
	supervisor.Go("worker-0", func() {})
	supervisor.Go("worker-1", func() { <-block })
	supervisor.Go("deployment worker", func() { <-block })
	time.Sleep(20 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	waited := make(chan error, 1)
	go func() { waited <- supervisor.Wait(ctx) }()
	var err error
	select {
	case err = <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not give up at the deadline")
	}
	if err == nil {
		t.Fatal("Wait = nil while two loops were still running")
	}
	message := err.Error()
	if !strings.Contains(message, "deployment worker") || !strings.Contains(message, "worker-1") || strings.Contains(message, "worker-0") {
		t.Fatalf("Wait = %q, want it to name exactly the loops still running", message)
	}
}
