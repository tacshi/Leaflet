package core

import (
	"context"
	"testing"

	"leaflet/internal/agents"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// host is the app as the hub sees it, with the default agent set.
type host struct{ settings store.Settings }

func (h host) Update(fn func())                        { fn() }
func (h host) Live(string) agents.LiveDoc              { return nil }
func (h host) SaveNow(string)                          {}
func (h host) Online() bool                            { return true }
func (h host) Settings() store.Settings                { return h.settings }
func (h host) FlushPage(context.Context, string) error { return nil }
func (h host) SyncNow()                                {}
func (h host) Tint(string, []string)                   {}

// A comment's agent is one it names, the first the hub lists, or else the
// default for @agent; one that mentions none has none, and @agent with no
// agent to run says why.
func TestCommentAgent(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	h := host{settings: store.DefaultSettings()}
	hub := agents.NewHub(st, remote.New(remote.Settings{}, st), h)
	if _, err := CommentAgent(hub, "@agent sum it up"); err == nil {
		t.Error("@agent with no agent to run")
	}
	for _, p := range []store.Provider{
		{ID: "a", Preset: "ollama", Name: "Ollama", AuthKind: "none", DefaultModel: "llama3", IsLocal: true},
		{ID: "b", Preset: "ollama", Name: "Studio", AuthKind: "none", DefaultModel: "qwen", IsLocal: true},
	} {
		if err := st.PutProvider(p); err != nil {
			t.Fatal(err)
		}
	}
	h.settings.DefaultModel = store.ModelChoice{ProviderID: "b"}.Encode()
	hub = agents.NewHub(st, remote.New(remote.Settings{}, st), h)
	for body, want := range map[string]string{
		"sum it up, not `@agent`": "",
		"@agent sum it up":        "b",
		"@agent ask @Ollama":      "a",
		"@Studio and @Ollama":     "a",
	} {
		got, err := CommentAgent(hub, body)
		switch {
		case err != nil:
			t.Errorf("%q: %v", body, err)
		case want == "" && got != nil:
			t.Errorf("%q: %+v, want none", body, got)
		case want != "" && (got == nil || got.ProviderID != want):
			t.Errorf("%q: %+v, want %s", body, got, want)
		}
	}
}
