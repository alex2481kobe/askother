package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/alex2481kobe/orca/internal/run"
)

// maxErrorRunes bounds the error message on a status line, which must stay
// one short line.
const maxErrorRunes = 200

// statusLine is the one-line status `orca wait` prints per run:
//
//	orca: run <id> <state>[ (<how>)] <elapsed>[ <role>][ "<task>"][: <CODE>: <message>][ -> result <id>]
//
// <how> is "exit N" or "signal NAME" when the worker exit is known, and
// "execution <execution>" for an interrupted run without one. <elapsed> is
// duration_ms when recorded, else from started_at (or created_at) to
// ended_at, or to now for a run that has not ended or reads interrupted: "340ms" under a second, otherwise whole seconds as in
// "4m12s". <role> and <message> are flattened to one line, <message> cut to
// 200 runes plus "..."; <task> is Go-quoted. Every ended run gets
// "-> result <id>", the run to read.
func statusLine(r *run.Run, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "orca: run %s %s", r.ID, r.State)
	if how := howEnded(r); how != "" {
		fmt.Fprintf(&b, " (%s)", how)
	}
	b.WriteString(" " + fmtElapsed(elapsed(r, now)))
	if r.Request.Role != "" {
		b.WriteString(" " + oneLine(r.Request.Role))
	}
	if r.Request.Task != "" {
		b.WriteString(" " + strconv.Quote(r.Request.Task))
	}
	if r.Error != nil {
		fmt.Fprintf(&b, ": %s: %s", r.Error.Code, cut(oneLine(r.Error.Message), maxErrorRunes))
	}
	if run.IsTerminal(r.State) {
		b.WriteString(" -> result " + r.ID)
	}
	return b.String()
}

func howEnded(r *run.Run) string {
	switch {
	case r.Exit != nil && r.Exit.Signal != nil:
		return "signal " + *r.Exit.Signal
	case r.Exit != nil && r.Exit.Code != nil:
		return "exit " + strconv.Itoa(*r.Exit.Code)
	case r.State == run.StateInterrupted:
		return "execution " + string(r.Execution)
	}
	return ""
}

func elapsed(r *run.Run, now time.Time) time.Duration {
	if r.DurationMS != nil {
		return time.Duration(*r.DurationMS) * time.Millisecond
	}
	start := r.CreatedAt
	if r.StartedAt != nil {
		start = *r.StartedAt
	}
	end := now
	if r.EndedAt != nil {
		end = *r.EndedAt
	}
	return max(end.Sub(start), 0)
}

func fmtElapsed(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// oneLine turns control characters and runs of whitespace into single spaces.
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(c rune) bool {
		return unicode.IsSpace(c) || unicode.IsControl(c)
	}), " ")
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}
