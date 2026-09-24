package ui

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/alex2481kobe/askother/internal/lifecycle"
	"github.com/alex2481kobe/askother/internal/run"
)

func testUI(t *testing.T) (*http.Client, string, string, *run.Store) {
	t.Helper()
	store, err := run.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ln, server, address, err := Listen(lifecycle.Deps{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(ln)
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	u, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Timeout: 3 * time.Second}, address, u.Query().Get("token"), store
}

func TestLocalUIRequiresTokenAndLocalOrigin(t *testing.T) {
	client, address, token, _ := testUI(t)
	u, _ := url.Parse(address)
	request := func(path, host, origin, key string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, u.Scheme+"://"+u.Host+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if key != "" {
			req.Header.Set("X-AskOther-Token", key)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := request("/?token="+token, u.Host, "", ""); got != http.StatusOK {
		t.Fatalf("page with token = %d", got)
	}
	if got := request("/", u.Host, "", ""); got != http.StatusForbidden {
		t.Fatalf("page without token = %d", got)
	}
	if got := request("/api/runs", u.Host, "", token); got != http.StatusOK {
		t.Fatalf("API with token = %d", got)
	}
	if got := request("/api/runs", u.Host, "", ""); got != http.StatusForbidden {
		t.Fatalf("API without token = %d", got)
	}
	if got := request("/api/runs", "not-local.example", "", token); got != http.StatusForbidden {
		t.Fatalf("API with foreign Host = %d", got)
	}
	if got := request("/api/runs", u.Host, "https://not-local.example", token); got != http.StatusForbidden {
		t.Fatalf("API with foreign Origin = %d", got)
	}
}

func TestClearHidesRunWithoutReadingOrDeletingIt(t *testing.T) {
	client, address, token, store := testUI(t)
	now := time.Now().UTC()
	zero := 0
	duration := int64(1500)
	id := "20260924T000000Z-00000000000000000000000000000001"
	record := &run.Run{
		SchemaVersion: run.SchemaVersion, ID: id, Key: "synthetic", CallerID: "caller",
		CallerSource: run.SourceCodex,
		Request:      run.Request{Worker: "codex", Prompt: "synthetic", CWD: t.TempDir(), Mode: "read-only"},
		State:        run.StateDone, Execution: run.ExecExited, CreatedAt: now.Add(-time.Minute),
		EndedAt: &now, DurationMS: &duration, Exit: &run.Exit{Code: &zero},
		Result: run.Result{Available: true},
	}
	if err := store.WithIndexLock(func() error { return store.Put(record) }); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(address)
	req, _ := http.NewRequest(http.MethodPost, u.Scheme+"://"+u.Host+"/api/clear", bytes.NewBufferString(`{"id":"`+id+`"}`))
	req.Header.Set("X-AskOther-Token", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear = %d", resp.StatusCode)
	}
	stored, err := store.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.UIHiddenAt == nil || stored.FirstReadAt != nil {
		t.Fatalf("clear wrote hidden=%v read=%v", stored.UIHiddenAt, stored.FirstReadAt)
	}
	views, err := (&handler{d: lifecycle.Deps{Store: store}}).snapshot()
	if err != nil || len(views) != 0 {
		t.Fatalf("visible runs = %v, err %v", views, err)
	}
}
