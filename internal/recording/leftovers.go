package recording

import (
	"context"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"leaflet/internal/store"
)

// What a transcription puts on a provider's side, the audio it uploads and
// the job that reads it, is deleted there as soon as the transcript is in.
// A quit, a crash or the system ending the app can come first, and the
// process takes the deletes with it: so each is noted in the store from
// the moment the provider names it until the provider has deleted it, and
// what is still noted when the app starts again is deleted then.
const leftoverPrefix = "recording.leftover."

func leftoverKey(provider, path string) string { return leftoverPrefix + provider + ":" + path }

// Leftovers notes what transcriptions put on a provider's side. The zero
// Leftovers notes nothing.
type Leftovers struct{ st *store.Store }

func (r *Controller) leftovers() Leftovers { return Leftovers{st: r.st} }

func (l Leftovers) keep(provider, path string) {
	if l.st == nil {
		return
	}
	if err := l.st.SetValue(leftoverKey(provider, path), "1"); err != nil {
		log.Printf("recording: %v", err)
	}
}

// remove deletes what path names on the provider's side, and drops its
// note unless the provider is to be asked again at the next start.
func (l Leftovers) remove(provider, key, path string) {
	if !removeAt(provider, key, path) && l.st != nil {
		l.drop(provider, path)
	}
}

func (l Leftovers) drop(provider, path string) {
	if err := l.st.DeleteValue(leftoverKey(provider, path)); err != nil {
		log.Printf("recording: %v", err)
	}
}

// removeWait is how long a provider has to answer a delete.
const removeWait = 20 * time.Second

// removeAt deletes what path names on the provider's side, and tells
// whether to ask again later: the provider could not be reached, was busy,
// or still uses it, as for a job that runs on. Whatever else it answers, as
// that it holds no such thing or does not take the key, it would answer
// again then.
func removeAt(provider, key, path string) (again bool) {
	api := sonioxAPI
	if provider == Moss {
		api = mossAPI
	}
	ctx, cancel := context.WithTimeout(context.Background(), removeWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE", api+path, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return true
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	_ = res.Body.Close()
	return busy(res.StatusCode) || res.StatusCode == http.StatusConflict
}

// A leftover is what a transcription the app's end cut short left on a
// provider's side.
type leftover struct{ provider, path string }

// leftBehind reads what is noted as left on a provider's side. It runs as
// the app starts, before a transcription can note what it uploads.
func (r *Controller) leftBehind() []leftover {
	var left []leftover
	for name := range r.st.ValuesWithPrefix(leftoverPrefix) {
		provider, path, _ := strings.Cut(name, ":")
		left = append(left, leftover{provider, path})
	}
	return left
}

// deleteLeftovers deletes what is left on a provider's side with this
// device's key for it. Without one there is nothing to delete it with, and
// the note goes. A job goes before the audio it reads, as when a
// transcription ends.
func (r *Controller) deleteLeftovers(left []leftover) {
	audio := func(x leftover) int {
		if strings.HasPrefix(x.path, "/files/") {
			return 1
		}
		return 0
	}
	slices.SortStableFunc(left, func(a, b leftover) int { return audio(a) - audio(b) })
	l := r.leftovers()
	for _, x := range left {
		var key string
		switch x.provider {
		case "soniox":
			key = LocalSonioxKey()
		case Moss:
			key = MossKey()
		}
		if key == "" {
			l.drop(x.provider, x.path)
			continue
		}
		l.remove(x.provider, key, x.path)
	}
}
