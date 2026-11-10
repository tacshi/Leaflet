package remote

import (
	"time"

	"leaflet/internal/store"
)

func SignedInForTest(url string, st *store.Store, userID string) *Client {
	c := New(Settings{URL: url, PublishableKey: "test"}, st)
	c.sess = &session{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour).Unix(), User: &authUser{ID: userID}}
	c.noKeychain = true
	return c
}

func SignedInToProjectForTest(url, ref string, st *store.Store, userID string) *Client {
	c := SignedInForTest(url, st, userID)
	c.cfg.Ref = ref
	return c
}

func (c *Client) TimeoutForTest(d time.Duration) { c.http.Timeout = d }

func (c *Client) SignInForTest(userID string) {
	c.keep(&session{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour).Unix(), User: &authUser{ID: userID}})
}
