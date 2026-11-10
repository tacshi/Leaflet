package app

import (
	"bytes"
	"testing"

	"leaflet/locales"
)

func TestChunksGoInOrder(t *testing.T) {
	var got []byte
	s := &captureSink{chunk: func(pcm []byte) { got = append(got, pcm...) }}
	for _, seq := range []int{1, 0, 3, 2, 2, 0} {
		s.deliver(seq, []byte{byte(seq)})
	}
	if !bytes.Equal(got, []byte{0, 1, 2, 3}) {
		t.Fatalf("wrote %v", got)
	}
	got = nil
	for seq := 5; seq <= 5+maxHeld; seq++ {
		s.deliver(seq, []byte{byte(seq)})
	}
	if len(got) != maxHeld+1 || got[0] != 5 {
		t.Fatalf("wrote %v", got)
	}
}

func TestRefusedMicSaysWhereToAllowIt(t *testing.T) {
	const webKit = "The request is not allowed by the user agent or the platform in the current context, possibly because the user denied permission."
	for _, tc := range []struct{ name, goos, want string }{
		{"NotAllowedError", "darwin", locales.RecErrMicDeniedMac()},
		{"NotAllowedError", "windows", locales.RecErrMicDeniedWindows()},
		{"NotAllowedError", "linux", webKit},
		{"NotReadableError", "darwin", webKit},
		{"", "darwin", webKit},
	} {
		if got := micFailure(tc.name, webKit, tc.goos); got != tc.want {
			t.Errorf("%s on %s: %q, want %q", tc.name, tc.goos, got, tc.want)
		}
	}
}

func TestHeldChunksGoOnAsTheMicStops(t *testing.T) {
	var got []byte
	s := &captureSink{chunk: func(pcm []byte) { got = append(got, pcm...) }}
	for _, seq := range []int{0, 4, 2, 3} {
		s.deliver(seq, []byte{byte(seq)})
	}
	if !bytes.Equal(got, []byte{0}) {
		t.Fatalf("wrote %v before stopping", got)
	}
	s.flush()
	s.deliver(3, []byte{3})
	s.deliver(1, []byte{1})
	if !bytes.Equal(got, []byte{0, 2, 3, 4}) {
		t.Fatalf("wrote %v", got)
	}
}
