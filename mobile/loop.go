package mobile

import (
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sync"

	"leaflet/locales"
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

// do runs fn after what was posted before it, and waits for it. A panic
// in fn goes on in the caller, as if fn ran there, for the exported
// function that called it to return as an error (catch); the queue goes on
// to the next function. It must not be called from the queue's own
// functions, which would wait on themselves.
func (q *queue) do(fn func()) {
	done := make(chan *panicked, 1)
	q.post(func() {
		ok := false
		defer func() {
			if ok {
				done <- nil
				return
			}
			// recover is nil for a runtime.Goexit, which still ends fn.
			done <- &panicked{value: recover(), stack: debug.Stack()}
		}()
		fn()
		ok = true
	})
	if p := <-done; p != nil {
		panic(p)
	}
}

// panicked is a panic on the queue, with where it happened, as do passes
// it on to its caller.
type panicked struct {
	value any
	stack []byte
}

func (p *panicked) String() string { return fmt.Sprint(p.value) }

// catch, deferred first in each of the package's exported functions,
// keeps a panic in the core from taking the app down with it, as gomobile
// would: it is logged, with where it happened, and the function returns it
// as an error to err, when it returns one. The goroutines the package
// starts run through guard.Go, which logs theirs; one that answers with
// its error defers catch itself, as ChatGPT's sign-in does, and the
// queue's catches each function it runs (call).
func catch(err *error) {
	r := recover()
	if r == nil {
		return
	}
	stack := debug.Stack()
	if p, ok := r.(*panicked); ok {
		r, stack = p.value, p.stack
	}
	log.Printf("mobile: %v\n%s", r, stack)
	if err != nil {
		*err = errors.New(locales.AppErrorInternal(fmt.Sprint(r)))
	}
}
