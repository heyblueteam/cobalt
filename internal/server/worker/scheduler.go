// Package worker runs the daemon's periodic background jobs: image
// cleanup, expired pending-app cleanup, and (later) project-level service
// crons defined in cobalt.json.
//
// The scheduler is a thin wrapper over robfig/cron with our slog logger
// and panic recovery so a misbehaving job never takes down the daemon.
package worker

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Job is a unit of scheduled work. It receives a fresh context each time
// it fires; the context is canceled only when the scheduler is stopped.
type Job func(ctx context.Context)

// Scheduler runs registered jobs on cron schedules. It is safe for
// concurrent Schedule calls.
type Scheduler struct {
	log     *slog.Logger
	cron    *cron.Cron
	rootCtx context.Context
	cancel  context.CancelFunc

	mu      sync.Mutex
	entries map[string]cron.EntryID // job name → cron entry id
}

// NewScheduler constructs a stopped scheduler. Call Start to begin firing
// jobs. The supplied logger is used for every job lifecycle event and any
// recovered panic.
func NewScheduler(log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{
		log:     log,
		cron:    cron.New(cron.WithLogger(cronLogger{log})),
		entries: map[string]cron.EntryID{},
	}
}

// Schedule registers a job. spec is a 5-field cron expression or any of
// robfig/cron's predefined names (@hourly, @daily, @every 10m, ...).
//
// The name must be unique. Registering the same name twice replaces the
// previous schedule.
func (s *Scheduler) Schedule(name, spec string, job Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id, ok := s.entries[name]; ok {
		s.cron.Remove(id)
		delete(s.entries, name)
	}

	id, err := s.cron.AddFunc(spec, func() {
		s.runJob(name, job)
	})
	if err != nil {
		return err
	}
	s.entries[name] = id
	s.log.Info("scheduler: job registered", "name", name, "spec", spec)
	return nil
}

// Entry is a registered job's name + the next time it will fire.
// Returned by Entries for read-only inspection.
type Entry struct {
	Name string
	Next time.Time
}

// Entries returns every registered job's name and next fire time,
// sorted by name. Used by the cron-services API to surface
// project-cron schedules to operators.
func (s *Scheduler) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	idToName := make(map[cron.EntryID]string, len(s.entries))
	for name, id := range s.entries {
		idToName[id] = name
	}

	cronEntries := s.cron.Entries()
	out := make([]Entry, 0, len(cronEntries))
	for _, e := range cronEntries {
		name, ok := idToName[e.ID]
		if !ok {
			continue
		}
		out = append(out, Entry{Name: name, Next: e.Next})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Remove unregisters a previously-Scheduled job. No-op if name is unknown.
func (s *Scheduler) Remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.entries[name]; ok {
		s.cron.Remove(id)
		delete(s.entries, name)
		s.log.Info("scheduler: job removed", "name", name)
	}
}

// Start begins firing jobs. Subsequent calls are no-ops.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rootCtx != nil {
		return
	}
	s.rootCtx, s.cancel = context.WithCancel(ctx)
	s.cron.Start()
	s.log.Info("scheduler: started")
}

// Stop halts the scheduler and waits for any in-flight job to return.
// Calling Stop on a never-started scheduler is a no-op.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.rootCtx = nil
	s.mu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	<-s.cron.Stop().Done()
	s.log.Info("scheduler: stopped")
}

// runJob is the wrapper every scheduled job runs through. It carries
// per-execution log context, recovers panics, and logs duration.
func (s *Scheduler) runJob(name string, job Job) {
	s.mu.Lock()
	ctx := s.rootCtx
	s.mu.Unlock()
	if ctx == nil {
		return
	}
	defer func() {
		if rv := recover(); rv != nil {
			s.log.Error(
				"scheduler: job panic",
				"name", name,
				"recovered", rv,
				"stack", string(debug.Stack()),
			)
		}
	}()
	s.log.Debug("scheduler: job firing", "name", name)
	job(ctx)
}

// cronLogger adapts our slog.Logger to robfig/cron's logger interface.
type cronLogger struct{ log *slog.Logger }

func (l cronLogger) Info(msg string, keysAndValues ...any) {
	l.log.Info("cron: "+msg, keysAndValues...)
}

func (l cronLogger) Error(err error, msg string, keysAndValues ...any) {
	l.log.Error("cron: "+msg, append(keysAndValues, "error", err)...)
}
