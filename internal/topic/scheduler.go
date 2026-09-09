package topic

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ActiveUserSource lists users with recent practice sessions.
type ActiveUserSource interface {
	ListActiveUserIDs(ctx context.Context, since time.Time) ([]string, error)
}

// Scheduler runs the 04:00 UTC topic-card batch.
type Scheduler struct {
	gen     *Generator
	active  ActiveUserSource
	logger  *slog.Logger
	mu      sync.Mutex
	lastRun time.Time
}

// NewScheduler constructs the daily batch runner.
func NewScheduler(gen *Generator, active ActiveUserSource, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{gen: gen, active: active, logger: logger.With("component", "topic.scheduler")}
}

// RunIfDue starts DailyCardGeneration once per UTC day after 04:00.
func (s *Scheduler) RunIfDue(ctx context.Context, now time.Time) {
	utc := now.UTC()
	if utc.Hour() < CronHourUTC {
		return
	}
	day := utcDate(utc)
	s.mu.Lock()
	if s.lastRun.Equal(day) {
		s.mu.Unlock()
		return
	}
	s.lastRun = day
	s.mu.Unlock()
	if err := s.DailyCardGeneration(ctx, day); err != nil && s.logger != nil {
		s.logger.Warn("daily topic card generation", "err", err)
	}
}

// DailyCardGeneration generates cards for users active in the last 30 days.
func (s *Scheduler) DailyCardGeneration(ctx context.Context, runDate time.Time) error {
	if s.gen == nil || s.active == nil {
		return nil
	}
	day := utcDate(runDate)
	since := day.Add(-ActiveLookback)
	users, err := s.active.ListActiveUserIDs(ctx, since)
	if err != nil {
		return err
	}
	sem := make(chan struct{}, MaxInFlight)
	var wg sync.WaitGroup
	for _, userID := range users {
		userID := userID
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.gen.GenerateForUser(ctx, userID, day); err != nil && s.logger != nil {
				s.logger.Warn("generate topic cards", "user_id", userID, "err", err)
			}
		}()
	}
	wg.Wait()
	return nil
}
