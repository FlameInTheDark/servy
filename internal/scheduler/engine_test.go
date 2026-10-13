package scheduler

import (
	"testing"
	"time"

	"github.com/FlameInTheDark/servy/internal/repo/gen"
)

func TestComputeNextRunIntervals(t *testing.T) {
	from := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := gen.Schedule{Interval: "minutes", IntervalInt: 30}
	if got := ComputeNextRun(s, from); got != from.Add(30*time.Minute).Format(time.RFC3339) {
		t.Fatalf("minutes: %s", got)
	}
	s = gen.Schedule{Interval: "hours", IntervalInt: 6}
	if got := ComputeNextRun(s, from); got != from.Add(6*time.Hour).Format(time.RFC3339) {
		t.Fatalf("hours: %s", got)
	}
	s = gen.Schedule{Interval: "days", IntervalInt: 1}
	if got := ComputeNextRun(s, from); got != from.Add(24*time.Hour).Format(time.RFC3339) {
		t.Fatalf("days: %s", got)
	}
}

func TestComputeNextRunCron(t *testing.T) {
	from := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cron := "0 4 * * *"
	s := gen.Schedule{Interval: "cron", CronString: &cron}
	got := ComputeNextRun(s, from)
	expect := "2026-01-02T04:00:00Z"
	if got != expect {
		t.Fatalf("cron next = %s, want %s", got, expect)
	}
}

func TestComputeNextRunDaily(t *testing.T) {
	from := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	st := "09:30"
	s := gen.Schedule{Interval: "daily", StartTime: &st}
	got := ComputeNextRun(s, from)
	if got != "2026-01-02T09:30:00Z" {
		t.Fatalf("daily = %s", got)
	}
}

func TestValidateSchedule(t *testing.T) {
	if err := ValidateSchedule(gen.Schedule{Interval: "hours", IntervalInt: 2, Action: "start"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSchedule(gen.Schedule{Interval: "hours", IntervalInt: 0, Action: "start"}); err == nil {
		t.Fatal("0 interval accepted")
	}
	if err := ValidateSchedule(gen.Schedule{Interval: "cron", Action: "start"}); err == nil {
		t.Fatal("empty cron accepted")
	}
	cron := "61 * * *"
	if err := ValidateSchedule(gen.Schedule{Interval: "cron", CronString: &cron, Action: "start"}); err == nil {
		t.Fatal("invalid cron accepted")
	}
	if err := ValidateSchedule(gen.Schedule{Interval: "minutes", IntervalInt: 5, Action: "explode"}); err == nil {
		t.Fatal("unknown action accepted")
	}
}
