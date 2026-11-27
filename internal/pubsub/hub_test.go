package pubsub

import "testing"

func TestHubSubscribePublishAndCancel(t *testing.T) {
	h := NewHub[string](2)
	ch1, cancel1 := h.Subscribe("run_1")
	ch2, cancel2 := h.Subscribe("run_1")
	other, cancelOther := h.Subscribe("run_2")
	defer cancel2()
	defer cancelOther()

	h.Publish("run_1", "hello")
	if got := <-ch1; got != "hello" {
		t.Fatalf("ch1 = %q, want hello", got)
	}
	if got := <-ch2; got != "hello" {
		t.Fatalf("ch2 = %q, want hello", got)
	}
	select {
	case got := <-other:
		t.Fatalf("unexpected item on run_2: %q", got)
	default:
	}

	// Cancel is idempotent and stops future deliveries.
	cancel1()
	cancel1()
	h.Publish("run_1", "second")
	if got := <-ch2; got != "second" {
		t.Fatalf("ch2 = %q, want second", got)
	}
	select {
	case got := <-ch1:
		t.Fatalf("canceled subscriber received %q", got)
	default:
	}

	// Non-blocking publish drops when subscriber buffer is full.
	h.Publish("run_1", "fill-1")
	h.Publish("run_1", "fill-2")
	h.Publish("run_1", "drop-3")

	// Nil receiver is safe.
	var nilHub *Hub[string]
	nilHub.Publish("run_1", "noop")
}
