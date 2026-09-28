package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Supervisor tracks the control plane's background loops so shutdown can wait
// for each to finish its current work, such as a run destroying its VM and
// recording how it ended, before the process exits.
type Supervisor struct {
	mu      sync.Mutex
	running map[string]int
	done    chan struct{}
	wg      sync.WaitGroup
}

func NewSupervisor() *Supervisor {
	return &Supervisor{running: map[string]int{}}
}

// Go runs loop in its own goroutine under name, which identifies it if it is
// still running when shutdown gives up waiting.
func (s *Supervisor) Go(name string, loop func()) {
	s.mu.Lock()
	s.running[name]++
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			s.running[name]--
			if s.running[name] == 0 {
				delete(s.running, name)
			}
			s.mu.Unlock()
		}()
		loop()
	}()
}

// Wait blocks until every loop has returned, or until ctx ends, in which case
// it returns an error naming the loops still running.
func (s *Supervisor) Wait(ctx context.Context) error {
	s.mu.Lock()
	if s.done == nil {
		s.done = make(chan struct{})
		go func(done chan struct{}) {
			s.wg.Wait()
			close(done)
		}(s.done)
	}
	done := s.done
	s.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}
	s.mu.Lock()
	names := make([]string, 0, len(s.running))
	for name := range s.running {
		names = append(names, name)
	}
	s.mu.Unlock()
	sort.Strings(names)
	return fmt.Errorf("stopped waiting for %s: %w", strings.Join(names, ", "), ctx.Err())
}
