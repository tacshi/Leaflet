package mobile

import (
	"log"
	"runtime/debug"
	"sync"
)

// queue runs functions one at a time, in the order they came, on a
// goroutine of its own. Posting never blocks, so what a function posts
// waits behind it rather than for it.
type queue struct {
	mu   sync.Mutex
	wake *sync.Cond
	fns  []func()
}

func newQueue() *queue {
	q := &queue{}
	q.wake = sync.NewCond(&q.mu)
	go q.run()
	return q
}

func (q *queue) run() {
	for {
		q.mu.Lock()
		for len(q.fns) == 0 {
			q.wake.Wait()
		}
		fn := q.fns[0]
		q.fns[0] = nil
		q.fns = q.fns[1:]
		q.mu.Unlock()
		q.call(fn)
	}
}

// call runs fn, keeping a panic in it from taking the app down with the
// rest of the queue: it is logged, and the next function runs.
func (q *queue) call(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("mobile: %v\n%s", r, debug.Stack())
		}
	}()
	fn()
}

// post runs fn after what was posted before it.
func (q *queue) post(fn func()) {
	q.mu.Lock()
	q.fns = append(q.fns, fn)
	q.mu.Unlock()
	q.wake.Signal()
}

// do runs fn after what was posted before it, and waits for it. It must
// not be called from the queue's own functions, which would wait on
// themselves.
func (q *queue) do(fn func()) {
	done := make(chan struct{})
	q.post(func() {
		defer close(done)
		fn()
	})
	<-done
}
