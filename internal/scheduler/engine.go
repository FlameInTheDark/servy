// Package scheduler implements the legacy schedule semantics: interval
// (minutes/hours/days), cron strings, one-time, start_time daily, and
// reaction children (triggered by parent server events with a delay).
package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/FlameInTheDark/servy/internal/config"
	"github.com/FlameInTheDark/servy/internal/database"
	"github.com/FlameInTheDark/servy/internal/events"
	"github.com/FlameInTheDark/servy/internal/logging"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
)

// Actions supported by schedules (parity with original: start/stop/restart/
// kill/backup/command + reaction actions).
const (
	ActionStart   = "start"
	ActionStop    = "stop"
	ActionRestart = "restart"
	ActionKill    = "kill"
	ActionBackup  = "backup"
	ActionCommand = "command"
)

// Executor is supplied by the app container: performs a schedule action
// against a server (local registry or cluster router).
type Executor func(serverID, action, actionID, command string, userID int64) error

// Engine runs the schedule loop.
type Engine struct {
	db   *database.Queries
	log  *logging.Manager
	cfg  *config.Manager
	bus  *events.Bus
	exec Executor
	stop chan struct{}
	once sync.Once
	mu   sync.Mutex
	// reaction children cached by parent action
	children map[int64][]gen.Schedule
}

// New builds the engine.
func New(db *database.Queries, log *logging.Manager, cfg *config.Manager, bus *events.Bus, exec Executor) *Engine {
	return &Engine{db: db, log: log, cfg: cfg, bus: bus, exec: exec, stop: make(chan struct{}), children: map[int64][]gen.Schedule{}}
}

// Start launches the ticker loop and subscribes to lifecycle events for reactions.
func (e *Engine) Start() error {
	// compute initial next_run for schedules lacking one
	rows, err := e.db.ListEnabledSchedules()
	if err != nil {
		return err
	}
	for _, s := range rows {
		if s.NextRun == nil || *s.NextRun == "" {
			nr := ComputeNextRun(s, time.Now().UTC())
			e.db.SetScheduleNextRun(&nr, nil, s.ScheduleID)
		}
	}
	go e.loop()
	e.subscribeReactions()
	return nil
}

// Stop terminates the engine.
func (e *Engine) Stop() { e.once.Do(func() { close(e.stop) }) }

func (e *Engine) loop() {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-tick.C:
			e.tickRun()
		}
	}
}

func (e *Engine) tickRun() {
	now := time.Now().UTC()
	rows, err := e.db.ListEnabledSchedules()
	if err != nil {
		return
	}
	for _, s := range rows {
		if s.Parent != nil {
			continue // reaction children run via events
		}
		if s.NextRun == nil || *s.NextRun == "" {
			nr := ComputeNextRun(s, now)
			e.db.SetScheduleNextRun(&nr, nil, s.ScheduleID)
			continue
		}
		next, err := time.Parse(time.RFC3339, *s.NextRun)
		if err != nil {
			continue
		}
		if now.After(next) || now.Equal(next) {
			e.Run(s, 0)
			nr := ComputeNextRun(s, now)
			var lastRun *string
			lr := now.Format(time.RFC3339)
			lastRun = &lr
			if s.OneTime == 1 {
				// disable one-time schedules after firing
				e.db.SetScheduleEnabled(0, s.ScheduleID)
			}
			e.db.SetScheduleNextRun(&nr, lastRun, s.ScheduleID)
		}
	}
}

// Run executes a schedule action now.
func (e *Engine) Run(s gen.Schedule, userID int64) {
	e.log.Get("schedule").Info("running schedule %q (%d) on server %s action=%s",
		deref(s.Name), s.ScheduleID, s.ServerID, s.Action)
	err := e.exec(s.ServerID, s.Action, deref(s.ActionID), deref(s.Command), userID)
	if err != nil {
		e.log.Get("schedule").Error("schedule %d failed: %v", s.ScheduleID, err)
	} else {
		e.log.Get("schedule").Info("schedule %d completed", s.ScheduleID)
	}
	// cascade reaction children (only for parent actions)
	sid := s.ScheduleID
	children, _ := e.db.ListChildSchedules(&sid)
	for _, c := range children {
		if c.Enabled != 1 {
			continue
		}
		delay := time.Duration(c.Delay) * time.Second
		child := c
		go func() {
			if delay > 0 {
				time.Sleep(delay)
			}
			e.log.Get("schedule").Info("running reaction child %d (parent %d)", child.ScheduleID, s.ScheduleID)
			if err := e.exec(child.ServerID, child.Action, deref(child.ActionID), deref(child.Command), userID); err != nil {
				e.log.Get("schedule").Error("reaction child %d failed: %v", child.ScheduleID, err)
			}
		}()
	}
}

// subscribeReactions wires lifecycle events to parent schedules with
// interval "reaction": when the server performs the action, children fire.
func (e *Engine) subscribeReactions() {
	ch, cancel := e.bus.Subscribe("*")
	go func() {
		defer cancel()
		for {
			select {
			case <-e.stop:
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				var action string
				switch ev.Type {
				case events.EvServerStart:
					action = ActionStart
				case events.EvServerStop:
					action = ActionStop
				case events.EvServerRestart:
					action = ActionRestart
				case events.EvServerKill:
					action = ActionKill
				case events.EvServerCrash:
					action = "crash"
				case events.EvBackupDone:
					action = ActionBackup
				default:
					continue
				}
				e.fireReactions(ev.ServerID, action)
			}
		}
	}()
}

func (e *Engine) fireReactions(serverID, action string) {
	rows, _ := e.db.ListEnabledSchedules()
	for _, s := range rows {
		if s.Parent != nil || s.Interval != "reaction" {
			continue
		}
		if s.ServerID != serverID {
			continue
		}
		if s.Action != action {
			continue
		}
		// the reaction parent itself runs on the event; children cascade via Run
		go e.Run(s, 0)
	}
}

// ComputeNextRun derives the next run time for a schedule.
func ComputeNextRun(s gen.Schedule, from time.Time) string {
	switch s.Interval {
	case "minutes":
		d := time.Duration(max64(s.IntervalInt, 1)) * time.Minute
		return from.Add(d).Format(time.RFC3339)
	case "hours":
		d := time.Duration(max64(s.IntervalInt, 1)) * time.Hour
		return from.Add(d).Format(time.RFC3339)
	case "days":
		d := time.Duration(max64(s.IntervalInt, 1)) * 24 * time.Hour
		return from.Add(d).Format(time.RFC3339)
	case "reaction":
		return "" // event-driven
	case "cron":
		if s.CronString != nil && *s.CronString != "" {
			return nextCron(*s.CronString, from)
		}
		return ""
	default:
		// daily start_time HH:MM
		if s.StartTime != nil && *s.StartTime != "" {
			return nextDaily(*s.StartTime, from)
		}
		return from.Add(24 * time.Hour).Format(time.RFC3339)
	}
}

// nextCron parses a 5/6-field cron expression and returns the next fire time.
func nextCron(expr string, from time.Time) string {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	sched, err := parser.Parse(expr)
	if err != nil {
		return from.Add(24 * time.Hour).Format(time.RFC3339)
	}
	return sched.Next(from).Format(time.RFC3339)
}

// nextDaily computes the next HH:MM occurrence.
func nextDaily(hhmm string, from time.Time) string {
	parts := strings.Split(hhmm, ":")
	h, m := 0, 0
	if len(parts) >= 1 {
		h, _ = strconv.Atoi(parts[0])
	}
	if len(parts) >= 2 {
		m, _ = strconv.Atoi(parts[1])
	}
	next := time.Date(from.Year(), from.Month(), from.Day(), h, m, 0, 0, time.UTC)
	if !next.After(from) {
		next = next.Add(24 * time.Hour)
	}
	return next.Format(time.RFC3339)
}

// ValidateSchedule validates a schedule config (API helper).
func ValidateSchedule(s gen.Schedule) error {
	switch s.Interval {
	case "minutes", "hours", "days":
		if s.IntervalInt < 1 {
			return fmt.Errorf("interval must be >= 1")
		}
	case "cron":
		if s.CronString == nil || *s.CronString == "" {
			return fmt.Errorf("cron_string required for cron interval")
		}
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
		if _, err := parser.Parse(*s.CronString); err != nil {
			return fmt.Errorf("invalid cron string: %v", err)
		}
	case "reaction":
		if s.Parent == nil {
			return fmt.Errorf("reaction schedules require a parent action")
		}
	default:
		return fmt.Errorf("unknown interval type %q", s.Interval)
	}
	switch s.Action {
	case ActionStart, ActionStop, ActionRestart, ActionKill, ActionBackup, ActionCommand:
	default:
		// reaction parents use the lifecycle action names; unknown = error
		if s.Interval != "reaction" {
			return fmt.Errorf("unknown action %q", s.Action)
		}
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
