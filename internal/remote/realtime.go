package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"leaflet/internal/guard"
)

type Realtime struct {
	c      *Client
	cfg    Settings
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	events chan RealtimeEvent
	done   chan struct{}
	end    sync.Once
	err    error

	mu      sync.Mutex
	ref     int
	replies map[string]chan realtimeReply
	joined  map[string]string
	token   string
	beat    string
	every   time.Duration
	beaten  func()
}

type RealtimeEvent struct {
	Topic   string
	Event   string
	Payload json.RawMessage
	Closed  bool
}

type RealtimeError struct{ Reason string }

func (e *RealtimeError) Error() string { return "realtime: " + e.Reason }

var errRealtimeSilent = errors.New("realtime: the server stopped answering")

var errRealtimeClosed = errors.New("realtime: closed")

var realtimeBeat = 25 * time.Second

var realtimeBeaten = func() {}

const realtimeWrite = 10 * time.Second

type realtimeMsg struct {
	Topic   string          `json:"topic"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
	Ref     *string         `json:"ref"`
	JoinRef *string         `json:"join_ref,omitempty"`
}

type realtimeReply struct {
	Status   string `json:"status"`
	Response struct {
		Reason string `json:"reason"`
	} `json:"response"`
}

func realtimeURL(cfg Settings) (string, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", errors.New("realtime: the project's address is not http or https")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/realtime/v1/websocket"
	u.RawQuery = url.Values{"apikey": {cfg.PublishableKey}, "vsn": {"1.0.0"}}.Encode()
	return u.String(), nil
}

func (c *Client) DialRealtime(ctx context.Context) (*Realtime, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	tok, cfg, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	addr, err := realtimeURL(cfg)
	if err != nil {
		return nil, err
	}
	conn, _, err := websocket.Dial(ctx, addr, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(1 << 20)
	r := &Realtime{c: c, cfg: cfg, conn: conn, events: make(chan RealtimeEvent, 64), done: make(chan struct{}),
		replies: map[string]chan realtimeReply{}, joined: map[string]string{}, token: tok, every: realtimeBeat, beaten: realtimeBeaten}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	guard.Go(r.read)
	guard.Go(r.keep)
	return r, nil
}

func (r *Realtime) Events() <-chan RealtimeEvent { return r.events }

func (r *Realtime) Done() <-chan struct{} { return r.done }

func (r *Realtime) Err() error {
	<-r.done
	return r.err
}

func (r *Realtime) Close() { r.fail(errRealtimeClosed) }

func (r *Realtime) fail(err error) {
	r.end.Do(func() {
		r.err = err
		close(r.done)
		r.cancel()
		_ = r.conn.CloseNow()
	})
}

func (r *Realtime) Join(ctx context.Context, topic string) error {
	tok, err := r.ownToken(ctx)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{
		"config": map[string]any{
			"broadcast": map[string]any{"ack": false, "self": false},
			"presence":  map[string]any{"enabled": false},
			"private":   true,
		},
		"access_token": tok,
	})
	reply := make(chan realtimeReply, 1)
	r.mu.Lock()
	r.ref++
	ref := strconv.Itoa(r.ref)
	r.replies[ref] = reply
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.replies, ref)
		r.mu.Unlock()
	}()
	if err := r.write(realtimeMsg{Topic: "realtime:" + topic, Event: "phx_join", Payload: payload, Ref: &ref, JoinRef: &ref}); err != nil {
		return err
	}
	select {
	case got := <-reply:
		if got.Status != "ok" {
			reason := got.Response.Reason
			if reason == "" {
				reason = got.Status
			}
			return &RealtimeError{Reason: reason}
		}
	case <-r.done:
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	r.joined[topic] = ref
	r.mu.Unlock()
	return nil
}

func (r *Realtime) Leave(topic string) {
	r.mu.Lock()
	joinRef, ok := r.joined[topic]
	delete(r.joined, topic)
	r.mu.Unlock()
	if ok {
		r.send("realtime:"+topic, "phx_leave", &joinRef)
	}
}

func (r *Realtime) send(topic, event string, joinRef *string, payload ...json.RawMessage) {
	r.mu.Lock()
	r.ref++
	ref := strconv.Itoa(r.ref)
	r.mu.Unlock()
	body := json.RawMessage(`{}`)
	if len(payload) > 0 {
		body = payload[0]
	}
	r.post(realtimeMsg{Topic: topic, Event: event, Payload: body, Ref: &ref, JoinRef: joinRef})
}

func (r *Realtime) post(m realtimeMsg) {
	if err := r.write(m); err != nil {
		r.fail(err)
	}
}

func (r *Realtime) heartbeat() {
	r.mu.Lock()
	r.ref++
	ref := strconv.Itoa(r.ref)
	r.beat = ref
	r.mu.Unlock()
	r.post(realtimeMsg{Topic: "phoenix", Event: "heartbeat", Payload: json.RawMessage(`{}`), Ref: &ref})
}

func (r *Realtime) write(m realtimeMsg) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.ctx, realtimeWrite)
	defer cancel()
	return r.conn.Write(ctx, websocket.MessageText, data)
}

func (r *Realtime) read() {
	defer close(r.events)
	for {
		_, data, err := r.conn.Read(r.ctx)
		if err != nil {
			r.fail(err)
			return
		}
		var m realtimeMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		ref := ""
		if m.Ref != nil {
			ref = *m.Ref
		}
		topic := strings.TrimPrefix(m.Topic, "realtime:")
		switch m.Event {
		case "phx_reply":
			var got realtimeReply
			_ = json.Unmarshal(m.Payload, &got)
			r.mu.Lock()
			if ref != "" && ref == r.beat {
				r.beat = ""
			}
			waiting := r.replies[ref]
			r.mu.Unlock()
			if waiting != nil {
				select {
				case waiting <- got:
				default:
				}
			}
		case "broadcast":
			var b struct {
				Event   string          `json:"event"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(m.Payload, &b) == nil {
				r.emit(RealtimeEvent{Topic: topic, Event: b.Event, Payload: b.Payload})
			}
		case "phx_close", "phx_error":
			r.mu.Lock()
			_, joined := r.joined[topic]
			delete(r.joined, topic)
			r.mu.Unlock()
			if joined {
				r.emit(RealtimeEvent{Topic: topic, Closed: true})
			}
		}
	}
}

func (r *Realtime) emit(e RealtimeEvent) {
	select {
	case r.events <- e:
	case <-r.done:
	}
}

func (r *Realtime) keep() {
	tick := time.NewTicker(r.every)
	defer tick.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-tick.C:
		}
		r.mu.Lock()
		silent := r.beat != ""
		r.mu.Unlock()
		if silent {
			r.fail(errRealtimeSilent)
			return
		}
		r.heartbeat()
		r.beaten()
		r.renew()
	}
}

// ownToken is the session's token while it is still of the project the socket is open to.
func (r *Realtime) ownToken(ctx context.Context) (string, error) {
	tok, cfg, err := r.c.token(ctx)
	if err == nil && cfg != r.cfg {
		return "", ErrSignedOut
	}
	return tok, err
}

func (r *Realtime) renew() {
	ctx, cancel := context.WithTimeout(r.ctx, realtimeWrite)
	defer cancel()
	tok, err := r.ownToken(ctx)
	if errors.Is(err, ErrSignedOut) {
		r.fail(err)
		return
	}
	if err != nil {
		return
	}
	r.mu.Lock()
	if tok == r.token {
		r.mu.Unlock()
		return
	}
	r.token = tok
	joined := make(map[string]string, len(r.joined))
	for topic, ref := range r.joined {
		joined[topic] = ref
	}
	r.mu.Unlock()
	payload, _ := json.Marshal(map[string]string{"access_token": tok})
	for topic, joinRef := range joined {
		r.send("realtime:"+topic, "access_token", &joinRef, payload)
	}
}
