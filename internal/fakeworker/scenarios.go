package main

import (
	"fmt"
	"os"
	"strings"
)

// worker is one dialect's event vocabulary. Scenarios are written against it
// so the same script runs as Codex or Claude.
type worker interface {
	start()                                  // report the session id, start the turn
	activity()                               // an event that is neither progress nor answer
	progress(text string)                    // an interim message
	finish(answer string) int                // final answer and exit code
	partial()                                // half an event, no newline
	special(name string) (code int, ok bool) // dialect-only scenarios
}

// longAnswerRunes is the length of the progress_then_final answer.
const longAnswerRunes = 36118

// play runs a scenario: common ones first, then lifecycle ones, then the
// dialect's own.
func play(name, arg string, w worker, e *emitter, rec *record) int {
	switch name {
	case "ok":
		w.start()
		w.activity()
		if arg == "" {
			arg = "OK"
		}
		return w.finish(arg)
	case "progress_then_final":
		w.start()
		for i := range 10 {
			w.progress(fmt.Sprintf("Progress %d of 10: still working on the synthetic task.", i+1))
		}
		return w.finish(longAnswer())
	case "huge_line":
		w.start()
		return w.finish(hugeAnswer(parseSize(arg, 3<<20)))
	case "split_utf8":
		e.split = true
		w.start()
		w.progress("进度 \U0001F680 halfway")
		code := w.finish(splitAnswer())
		rec.SplitWrites = e.cuts
		rec.save()
		return code
	case "no_trailing_newline":
		e.noTrailingNewline = true
		w.start()
		return w.finish("no trailing newline")
	case "resume_same_thread", "resume_new_thread":
		// The session id was chosen in run: resume_new_thread got a fresh one.
		if !rec.Invocation.Resume {
			return misuse(name + " needs a resume invocation")
		}
		w.start()
		return w.finish("resumed")
	case "stderr_only":
		// A failure before any event: exit 1 with only stderr to explain it.
		if arg == "" {
			arg = "Error: synthetic startup failure"
		}
		fmt.Fprintln(os.Stderr, arg)
		return 1
	}
	if code, ok := lifecycle(name, arg, w, e, rec); ok {
		return code
	}
	if code, ok := w.special(name); ok {
		return code
	}
	return misuse(fmt.Sprintf("scenario %q is not available for %s", name, rec.Dialect))
}

// longAnswer is exactly longAnswerRunes runes of numbered lines mixing ASCII,
// CJK and emoji, with no trailing newline (real answers have none).
func longAnswer() string {
	var runes []rune
	for i := 1; len(runes) < longAnswerRunes; i++ {
		line := fmt.Sprintf("%04d 猫\U0001F41F final answer line, lorem ipsum dolor sit amet.\n", i)
		runes = append(runes, []rune(line)...)
	}
	return string(runes[:longAnswerRunes])
}

// hugeAnswer is at least n bytes of valid UTF-8 without a trailing newline.
func hugeAnswer(n int) string {
	var b strings.Builder
	b.Grow(n + 128)
	for i := 0; b.Len() <= n; i++ {
		fmt.Fprintf(&b, "%07d 文字 huge answer filler, consectetur adipiscing elit.\n", i)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func splitAnswer() string {
	return "split: " + strings.Repeat("中文\U0001F600 猫\U0001F41F ", 40) + "end"
}
