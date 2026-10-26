package remote

import (
	"time"

	"leaflet/internal/store"
)

// SignedInForTest returns a client signed in as userID against a fake
// server at url. Its token does not expire and nothing goes to the keychain.
func SignedInForTest(url string, st *store.Store, userID string) *Client {
	c := New(Settings{URL: url, AnonKey: "test"}, st)
	c.sess = &session{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour).Unix(), User: &authUser{ID: userID}}
	return c
}
