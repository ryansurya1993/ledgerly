package handler

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ryansurya1993/ledgerly/notification-service/internal/stream"
)

// TestStream_DeliversPublishedEventInSSEFormat drives Stream through a
// real net/http server and a real client connection -- not
// httptest.NewRecorder(), which isn't safe for one goroutine to read
// while the handler goroutine is still writing to it (this test used
// to do exactly that, and go test -race caught it). A real connection
// sidesteps the problem entirely and is a closer match for how this
// handler is actually used in production.
func TestStream_DeliversPublishedEventInSSEFormat(t *testing.T) {
	hub := stream.NewHub()
	srv := httptest.NewServer(Stream(hub))
	defer srv.Close()

	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", srv.URL, err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		r := bufio.NewReader(resp.Body)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				lines <- line
			}
			if err != nil {
				return
			}
		}
	}()

	// The server-side handler reaches hub.Subscribe() asynchronously,
	// so the first Publish could race ahead of it and be missed --
	// exactly like any publisher racing a not-yet-connected subscriber
	// (see internal/stream.Hub's doc comment: it's a live fan-out, not
	// a durable queue). Republishing on a ticker until this test has
	// seen what it needs is a harmless way to wait that out.
	publishCtx, stopPublishing := context.WithCancel(context.Background())
	defer stopPublishing()
	publishDone := make(chan struct{})
	go func() {
		defer close(publishDone)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-publishCtx.Done():
				return
			case <-ticker.C:
				hub.Publish([]byte(`{"transaction_id":"test-tx-abc"}`))
			}
		}
	}()

	var gotEventLine, gotDataLine bool
	timeout := time.After(3 * time.Second)
readLoop:
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				break readLoop
			}
			if strings.TrimSpace(line) == "event: transaction.posted" {
				gotEventLine = true
			}
			if strings.Contains(line, "test-tx-abc") {
				gotDataLine = true
			}
			if gotEventLine && gotDataLine {
				break readLoop
			}
		case <-timeout:
			break readLoop
		}
	}

	stopPublishing()
	<-publishDone
	cancelReq()

	if !gotEventLine {
		t.Error(`did not see "event: transaction.posted" line`)
	}
	if !gotDataLine {
		t.Error("did not see expected data line")
	}
}
