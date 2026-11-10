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

type RealtimeForTest struct {
	mu      sync.Mutex
	Refused map[string]string
	Silent  bool
	Queries []url.Values
	conns   map[*websocket.Conn]map[string]string
	sent    []RealtimeSent
	changed chan struct{}
}

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

func (f *RealtimeForTest) Broadcast(topic, event string, payload any) {
	body := mustJSON(map[string]any{"type": "broadcast", "event": event, "payload": payload})
	for conn, joinRef := range f.joinedTo(topic) {
		f.write(conn, realtimeMsg{Topic: "realtime:" + topic, Event: "broadcast", Payload: body, JoinRef: &joinRef})
	}
}

func (f *RealtimeForTest) CloseChannel(topic string) {
	for conn, joinRef := range f.joinedTo(topic) {
		f.mu.Lock()
		delete(f.conns[conn], topic)
		f.mu.Unlock()
		f.write(conn, realtimeMsg{Topic: "realtime:" + topic, Event: "phx_close", Payload: json.RawMessage(`{}`), JoinRef: &joinRef})
	}
	f.touch()
}

func (f *RealtimeForTest) Drop() {
	f.mu.Lock()
	conns := slices.Collect(maps.Keys(f.conns))
	f.mu.Unlock()
	for _, c := range conns {
		_ = c.CloseNow()
	}
}

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

func (f *RealtimeForTest) Connected() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.conns)
}

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
