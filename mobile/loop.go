package mobile

import (
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sync"

	"leaflet/locales"
)

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

func (q *queue) call(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("mobile: %v\n%s", r, debug.Stack())
		}
	}()
	fn()
}

func (q *queue) post(fn func()) {
	q.mu.Lock()
	q.fns = append(q.fns, fn)
	q.mu.Unlock()
	q.wake.Signal()
}

func (q *queue) do(fn func()) {
	done := make(chan *panicked, 1)
	q.post(func() {
		ok := false
		defer func() {
			if ok {
				done <- nil
				return
			}
			done <- &panicked{value: recover(), stack: debug.Stack()}
		}()
		fn()
		ok = true
	})
	if p := <-done; p != nil {
		panic(p)
	}
}

type panicked struct {
	value any
	stack []byte
}

func (p *panicked) String() string { return fmt.Sprint(p.value) }

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
