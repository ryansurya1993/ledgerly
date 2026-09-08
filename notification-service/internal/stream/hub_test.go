package stream

import (
	"testing"
	"time"
)

func TestHub_PublishDeliversToAllSubscribers(t *testing.T) {
	h := NewHub()
	a := h.Subscribe()
	b := h.Subscribe()
	defer h.Unsubscribe(a)
	defer h.Unsubscribe(b)

	h.Publish([]byte("hello"))

	for _, ch := range []chan []byte{a, b} {
		select {
		case got := <-ch:
			if string(got) != "hello" {
				t.Errorf("got %q, want %q", got, "hello")
			}
		case <-time.After(time.Second):
			t.Fatal("subscriber did not receive published payload")
		}
	}
}

func TestHub_UnsubscribeStopsDelivery(t *testing.T) {
	h := NewHub()
	ch := h.Subscribe()
	h.Unsubscribe(ch)

	h.Publish([]byte("should not be delivered"))

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("received a payload on an unsubscribed channel")
		}
		// ok == false: the channel was closed by Unsubscribe, as expected.
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel neither closed nor readable after Unsubscribe")
	}
}

func TestHub_SlowSubscriberDoesNotBlockOthers(t *testing.T) {
	h := NewHub()
	slow := h.Subscribe() // never read from -- simulates a stalled client
	fast := h.Subscribe()
	defer h.Unsubscribe(slow)
	defer h.Unsubscribe(fast)

	// Fill the slow subscriber's buffer past capacity, then publish
	// more still -- Publish must not block on slow (this call
	// completing at all is the assertion), and fast must still get
	// through despite slow's backlog.
	for i := 0; i < subscriberBuffer+5; i++ {
		h.Publish([]byte("msg"))
	}

	select {
	case <-fast:
	case <-time.After(time.Second):
		t.Fatal("fast subscriber received nothing despite a slow sibling")
	}
}
