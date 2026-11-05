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

// Realtime is a connection to the project's Realtime, for the private
// channels the database tells of saves on (protocol §6): Phoenix's
// protocol 1.0.0 over a websocket, each message a JSON object. Its
// channels hear broadcasts only; it sends none.
type Realtime struct {
	c    *Client
	conn *websocket.Conn
	// ctx ends with the connection, and its writes with it.
	ctx    context.Context
	cancel context.CancelFunc
	// events are the broadcasts heard and the channels the server closed,
	// as they come; the reader closes it as the connection ends.
	events chan RealtimeEvent
	// done is closed as the connection ends, err then saying why.
	done chan struct{}
	end  sync.Once
	err  error

	mu  sync.Mutex
	ref int
	// replies wait for the server's answer to a message, by its ref.
	replies map[string]chan realtimeReply
	// joined are the channels joined, by topic, with their join's ref.
	joined map[string]string
	// token is the access token the channels were last given.
	token string
	// beat is the ref of the heartbeat the server has not answered, or "".
	beat string
	// every is realtimeBeat as the connection was made.
	every time.Duration
}

// RealtimeEvent is a broadcast on a channel, or the channel's end that
// the server chose (Closed), as an access token it no longer takes.
type RealtimeEvent struct {
	// Topic is the channel's, without Realtime's "realtime:" prefix.
	Topic   string
	Event   string
	Payload json.RawMessage
	Closed  bool
}

// RealtimeError is Realtime's refusal to join a channel, with its reason,
// as "Unauthorized: You do not have permissions to read from this Channel
// topic: page:…" for a page the caller may not read, or a project whose
// database does not say who may.
type RealtimeError struct{ Reason string }

func (e *RealtimeError) Error() string { return "realtime: " + e.Reason }

// errRealtimeSilent is a connection whose heartbeat the server did not
// answer before the next was due.
var errRealtimeSilent = errors.New("realtime: the server stopped answering")

// errRealtimeClosed is a connection Close ended.
var errRealtimeClosed = errors.New("realtime: closed")

// realtimeBeat is how often a connection says it is still there, which
// Realtime wants at least every 25 seconds, and renews its channels'
// access token when the session's was; tests shorten it.
var realtimeBeat = 25 * time.Second

// realtimeWrite is how long a message has to go out.
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

// realtimeURL is the websocket address of a project's Realtime.
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

// DialRealtime connects to the project's Realtime as the person signed
// in, whose access token the channels joined have, renewed as it is.
func (c *Client) DialRealtime(ctx context.Context) (*Realtime, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	addr, err := realtimeURL(c.settings())
	if err != nil {
		return nil, err
	}
	conn, _, err := websocket.Dial(ctx, addr, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(1 << 20)
	r := &Realtime{c: c, conn: conn, events: make(chan RealtimeEvent, 64), done: make(chan struct{}),
		replies: map[string]chan realtimeReply{}, joined: map[string]string{}, token: tok, every: realtimeBeat}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	guard.Go(r.read)
	guard.Go(r.keep)
	return r, nil
}

// Events are the broadcasts heard and the channels the server closed. It
// is closed as the connection ends.
func (r *Realtime) Events() <-chan RealtimeEvent { return r.events }

// Done is closed as the connection ends; Err then says why.
func (r *Realtime) Done() <-chan struct{} { return r.done }

// Err is why the connection ended, once Done is closed.
func (r *Realtime) Err() error {
	<-r.done
	return r.err
}

// Close ends the connection.
func (r *Realtime) Close() { r.fail(errRealtimeClosed) }

// fail ends the connection for err, the first time.
func (r *Realtime) fail(err error) {
	r.end.Do(func() {
		r.err = err
		close(r.done)
		r.cancel()
		// Not waiting on the server's side of a close, which one that
		// stopped answering never sends.
		_ = r.conn.CloseNow()
	})
}

// Join joins the private channel of topic, as the person signed in, and
// waits for Realtime to take them: one it refuses is a RealtimeError.
func (r *Realtime) Join(ctx context.Context, topic string) error {
	tok, err := r.c.token(ctx)
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

// Leave leaves the channel of topic, without waiting for Realtime to say
// it has.
func (r *Realtime) Leave(topic string) {
	r.mu.Lock()
	joinRef, ok := r.joined[topic]
	delete(r.joined, topic)
	r.mu.Unlock()
	if ok {
		_ = r.send("realtime:"+topic, "phx_leave", &joinRef)
	}
}

// send sends an event with an empty payload, returning its ref.
func (r *Realtime) send(topic, event string, joinRef *string, payload ...json.RawMessage) string {
	r.mu.Lock()
	r.ref++
	ref := strconv.Itoa(r.ref)
	r.mu.Unlock()
	body := json.RawMessage(`{}`)
	if len(payload) > 0 {
		body = payload[0]
	}
	if err := r.write(realtimeMsg{Topic: topic, Event: event, Payload: body, Ref: &ref, JoinRef: joinRef}); err != nil {
		r.fail(err)
	}
	return ref
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

// read reads the server's messages until the connection ends: answers go
// to whoever waits for them, and broadcasts and the channels closed to
// Events.
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
				default: // answered already
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
			// A channel left here closes too: that one is gone already.
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

// keep beats every realtimeBeat, and gives the channels the session's
// access token as it is renewed, before the one they have runs out. A
// heartbeat not answered by the next ends the connection, as does the
// person signing out.
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
		ref := r.send("phoenix", "heartbeat", nil)
		r.mu.Lock()
		r.beat = ref
		r.mu.Unlock()
		r.renew()
	}
}

// renew gives the channels joined the session's access token, if it
// changed since they were given one.
func (r *Realtime) renew() {
	ctx, cancel := context.WithTimeout(r.ctx, realtimeWrite)
	defer cancel()
	tok, err := r.c.token(ctx)
	if errors.Is(err, ErrSignedOut) {
		r.fail(err)
		return
	}
	if err != nil {
		return // the next beat tries again
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
		_ = r.send("realtime:"+topic, "access_token", &joinRef, payload)
	}
}
