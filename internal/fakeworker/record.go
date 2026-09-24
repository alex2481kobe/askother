package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
)

// record is what the fake saw and did, written to $ASKOTHER_FAKE_RECORD so tests
// can assert what AskOther passed. Only environment names are recorded, never values.
type record struct {
	path          string
	Dialect       string      `json:"dialect"`
	Scenario      string      `json:"scenario"`
	Arg           string      `json:"arg,omitempty"`
	Argv          []string    `json:"argv"`
	Cwd           string      `json:"cwd"`
	EnvNames      []string    `json:"env_names"`
	Stdin         string      `json:"stdin"`
	PID           int         `json:"pid"`
	PGID          int         `json:"pgid"`
	Invocation    invocation  `json:"invocation"`
	SessionID     string      `json:"session_id,omitempty"`
	Answer        *answerInfo `json:"answer,omitempty"`
	GrandchildPID int         `json:"grandchild_pid,omitempty"`
	SplitWrites   int         `json:"split_writes,omitempty"`
	WriteErrors   int         `json:"stdout_write_errors,omitempty"`
}

// answerInfo describes the final answer the fake emitted, so a test can check
// a published answer without knowing the scenario's text.
type answerInfo struct {
	Bytes  int    `json:"bytes"`
	Runes  int    `json:"runes"`
	SHA256 string `json:"sha256"`
}

func newRecord(dialect, scenario string, argv []string, in invocation, stdin []byte) *record {
	r := &record{
		path:       os.Getenv(envRecord),
		Dialect:    dialect,
		Scenario:   scenario,
		Arg:        os.Getenv(envArg),
		Argv:       argv,
		Stdin:      string(stdin),
		PID:        os.Getpid(),
		Invocation: in,
	}
	r.Cwd, _ = os.Getwd()
	r.PGID, _ = syscall.Getpgid(0)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		r.EnvNames = append(r.EnvNames, k)
	}
	sort.Strings(r.EnvNames)
	return r
}

func (r *record) setAnswer(answer string) {
	sum := sha256.Sum256([]byte(answer))
	r.Answer = &answerInfo{Bytes: len(answer), Runes: utf8.RuneCountInString(answer), SHA256: hex.EncodeToString(sum[:])}
	r.save()
}

// save replaces the record file atomically. Failures are reported on stderr
// and otherwise ignored: the record is a test aid, not part of the protocol.
func (r *record) save() {
	if r.path == "" {
		return
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err == nil {
		tmp := filepath.Join(filepath.Dir(r.path), fmt.Sprintf(".%s.%d.tmp", filepath.Base(r.path), os.Getpid()))
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, r.path)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakeworker: record: %v\n", err)
	}
}
