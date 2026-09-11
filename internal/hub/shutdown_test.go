package hub

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/philband/callboard/internal/api"
	"github.com/philband/callboard/internal/client"
)

func TestShutdownEndsLongPolls(t *testing.T) {
	h := newHub(t, Options{Version: "test"})
	cl, _ := serveHub(t, h)
	ctx := context.Background()
	checkin(t, h, "alice")

	polling := make(chan error, 1)
	go func() {
		_, err := cl.Inbox(ctx, "alice", 30*time.Second, false)
		polling <- err
	}()
	// Let the poll reach the hub and park on the generation channel.
	time.Sleep(100 * time.Millisecond)

	if err := cl.Shutdown(ctx, h.Build().ModTime); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	select {
	case err := <-polling:
		if !client.IsRestarting(err) {
			t.Fatalf("in-flight inbox returned %v, want %q", err, api.MsgHubRestarting)
		}
		var e *client.APIError
		if !errors.As(err, &e) || e.Code != http.StatusServiceUnavailable {
			t.Fatalf("inbox error = %v, want 503", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("inbox long poll did not end when the hub started shutting down")
	}

	select {
	case <-h.Closing():
	default:
		t.Fatal("hub is not closing after a 202 shutdown")
	}

	// Events long polls end the same way; from the current seq there is
	// nothing to return, so the call would otherwise park.
	_, err := h.Events(ctx, h.Seq(), time.Second, "")
	var he *Error
	if !errors.As(err, &he) || he.Code != 503 || he.Msg != api.MsgHubRestarting {
		t.Fatalf("events after shutdown = %v, want 503 %q", err, api.MsgHubRestarting)
	}
}

func TestShutdownRefusesOtherBuild(t *testing.T) {
	h := newHub(t, Options{Version: "test"})
	cl, _ := serveHub(t, h)

	err := cl.Shutdown(context.Background(), time.Unix(1, 0))
	var e *client.APIError
	if !errors.As(err, &e) || e.Code != http.StatusConflict {
		t.Fatalf("shutdown with a foreign build = %v, want 409", err)
	}
	select {
	case <-h.Closing():
		t.Fatal("hub shut down on a mismatched build")
	default:
	}
}
