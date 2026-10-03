package client_test

import (
	"context"
	"io"
	"math"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/client"
)

// MaxMessageSize: math.MaxInt ("no limit") overflows maxMessageSize+1.
func TestDialStreamMaxIntLimit(t *testing.T) {
	a, b := net.Pipe()
	go func() {
		_, _ = io.WriteString(b, `{"type":"config"}`+"\n")
		buf := make([]byte, 4096)
		for {
			if _, err := b.Read(buf); err != nil {
				return
			}
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("DialStream with MaxMessageSize math.MaxInt panicked: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := client.DialStream(ctx, func(context.Context) (io.ReadWriteCloser, error) { return a, nil },
		client.Options{MaxMessageSize: math.MaxInt, NoReconnect: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

// The server's auth-timeout auth_error crosses the client's auth frame (the
// hook is slow on one reconnect). The client counts it as the verdict of its
// own auth and shuts down for good, though the token was fine.
func TestAuthTimeoutCrossingAuthFrameReconnects(t *testing.T) {
	var calls atomic.Int32
	f := newReviewFixture(t, aprot.ServerOptions{AuthTimeout: 200 * time.Millisecond, ReconnectInterval: 5, ReconnectMaxInterval: 5},
		func(s *aprot.Server) {
			s.OnAuth(func(ctx context.Context, conn *aprot.Conn, tok string) error {
				if calls.Add(1) == 2 {
					time.Sleep(400 * time.Millisecond) // slow hook on one reconnect
				}
				conn.SetUserID("u1")
				return nil
			})
		})
	c := f.dial(t, client.Options{
		ConnectTimeout: 5 * time.Second,
		AuthToken:      func(ctx context.Context) (string, error) { return "ok", nil },
	})
	f.srv.DisconnectUser("u1")
	time.Sleep(1500 * time.Millisecond)
	if err := c.Err(); err != nil {
		t.Fatalf("an auth timeout on one reconnect stopped the client for good: %v", err)
	}
}
