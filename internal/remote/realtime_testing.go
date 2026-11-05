package remote

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// RealtimeForTest stands in for a project's Realtime in tests, served at
// /realtime/v1/websocket: it answers joins, refusing those in Refused,
// heartbeats and leaves, keeps what the clients sent, and broadcasts to
// the clients joined to a topic.
type RealtimeForTest struct {
	mu sync.Mutex
	// Refused are the topics it refuses a join of, with the reason.
	Refused map[string]string
	// Silent has it answer no heartbeat.
	Silent bool
	// Queries are the dials' query strings, as apikey and vsn.
	Queries []url.Values
	conns   map[*websocket.Conn]map[string]string
	sent    []RealtimeSent
	changed chan struct{}
}

// RealtimeSent is a message a client sent, its topic without "realtime:".
type RealtimeSent struct {
	Topic   string
	Event   string
	Payload json.RawMessage
}

func (f *RealtimeForTest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	f.mu.Lock()
	if f.conns == nil {
		f.conns = map[*websocket.Conn]map[string]string{}
	}
	f.conns[conn] = map[string]string{}
	f.Queries = append(f.Queries, r.URL.Query())
	f.mu.Unlock()
	f.touch()
	defer func() {
		f.mu.Lock()
		delete(f.conns, conn)
		f.mu.Unlock()
		f.touch()
	}()
	for {
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var m realtimeMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		topic := strings.TrimPrefix(m.Topic, "realtime:")
		reply := map[string]any{"status": "ok", "response": map[string]any{}}
		f.mu.Lock()
		f.sent = append(f.sent, RealtimeSent{Topic: topic, Event: m.Event, Payload: m.Payload})
		answer := true
		switch m.Event {
		case "phx_join":
			if reason, refused := f.Refused[topic]; refused {
				reply = map[string]any{"status": "error", "response": map[string]any{"reason": reason}}
			} else if m.JoinRef != nil {
				f.conns[conn][topic] = *m.JoinRef
			}
		case "phx_leave":
			delete(f.conns[conn], topic)
		case "heartbeat":
			answer = !f.Silent
		default:
			answer = false
		}
		f.mu.Unlock()
		if answer {
			f.write(conn, realtimeMsg{Topic: m.Topic, Event: "phx_reply", Payload: mustJSON(reply), Ref: m.Ref, JoinRef: m.JoinRef})
		}
		f.touch()
	}
}

// Broadcast sends event with payload on topic to the clients joined to it.
func (f *RealtimeForTest) Broadcast(topic, event string, payload any) {
	body := mustJSON(map[string]any{"type": "broadcast", "event": event, "payload": payload})
	for conn, joinRef := range f.joinedTo(topic) {
		f.write(conn, realtimeMsg{Topic: "realtime:" + topic, Event: "broadcast", Payload: body, JoinRef: &joinRef})
	}
}

// CloseChannel closes topic's channel for the clients joined to it, as
// Realtime does for one whose access token ran out.
func (f *RealtimeForTest) CloseChannel(topic string) {
	for conn, joinRef := range f.joinedTo(topic) {
		f.mu.Lock()
		delete(f.conns[conn], topic)
		f.mu.Unlock()
		f.write(conn, realtimeMsg{Topic: "realtime:" + topic, Event: "phx_close", Payload: json.RawMessage(`{}`), JoinRef: &joinRef})
	}
	f.touch()
}

// Drop ends every client's connection, as a network that went away does.
func (f *RealtimeForTest) Drop() {
	f.mu.Lock()
	conns := slices.Collect(maps.Keys(f.conns))
	f.mu.Unlock()
	for _, c := range conns {
		_ = c.CloseNow()
	}
}

// Joined are the topics the clients are joined to, sorted, once each.
func (f *RealtimeForTest) Joined() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, topics := range f.conns {
		for t := range topics {
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	slices.Sort(out)
	return out
}

// Connected is how many clients are connected.
func (f *RealtimeForTest) Connected() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.conns)
}

// Sent are the messages the clients sent of event.
func (f *RealtimeForTest) Sent(event string) []RealtimeSent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []RealtimeSent
	for _, m := range f.sent {
		if m.Event == event {
			out = append(out, m)
		}
	}
	return out
}

// Await waits up to d for ok to hold, looking again as clients connect,
// send or leave, and reports whether it held.
func (f *RealtimeForTest) Await(d time.Duration, ok func() bool) bool {
	deadline := time.After(d)
	for {
		f.mu.Lock()
		if f.changed == nil {
			f.changed = make(chan struct{})
		}
		changed := f.changed
		f.mu.Unlock()
		if ok() {
			return true
		}
		select {
		case <-changed:
		case <-deadline:
			return ok()
		}
	}
}

func (f *RealtimeForTest) touch() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.changed != nil {
		close(f.changed)
		f.changed = nil
	}
}

func (f *RealtimeForTest) joinedTo(topic string) map[*websocket.Conn]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[*websocket.Conn]string{}
	for c, topics := range f.conns {
		if ref, ok := topics[topic]; ok {
			out[c] = ref
		}
	}
	return out
}

func (f *RealtimeForTest) write(conn *websocket.Conn, m realtimeMsg) {
	data, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, data)
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
