package remote

import (
	"time"

	"leaflet/internal/store"
)

// SignedInForTest returns a client signed in as userID against a fake
// server at url. Its token does not expire and nothing goes to the keychain.
func SignedInForTest(url string, st *store.Store, userID string) *Client {
	c := New(Settings{URL: url, PublishableKey: "test"}, st)
	c.sess = &session{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour).Unix(), User: &authUser{ID: userID}}
	c.noKeychain = true
	return c
}

// SignedInToProjectForTest is SignedInForTest for a project chosen with a
// Supabase account, as its ref names it.
func SignedInToProjectForTest(url, ref string, st *store.Store, userID string) *Client {
	c := SignedInForTest(url, st, userID)
	c.cfg.Ref = ref
	return c
}

// SignInForTest signs c in again as userID, as a sign-in does, beginning
// another session.
func (c *Client) SignInForTest(userID string) {
	c.keep(&session{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour).Unix(), User: &authUser{ID: userID}})
}
