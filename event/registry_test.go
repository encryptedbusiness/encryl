package event

import (
	"errors"
	"testing"
)

type fakeConn struct{}

func TestRegistryOnAndLookup(t *testing.T) {
	r := NewRegistry[*fakeConn]()
	if err := r.On(1, func(c *fakeConn, m Message) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := r.On(1, func(c *fakeConn, m Message) error { return nil }); err == nil {
		t.Fatal("duplicate On accepted")
	}
	if err := r.On(2, nil); err == nil {
		t.Fatal("nil handler accepted")
	}
	if _, ok := r.Lookup(1); !ok {
		t.Fatal("handler not found")
	}
	if _, ok := r.Lookup(3); ok {
		t.Fatal("unexpected handler")
	}
}

func TestMiddlewareOrder(t *testing.T) {
	r := NewRegistry[*fakeConn]()
	var order []string
	if err := r.Use(func(next Handler[*fakeConn]) Handler[*fakeConn] {
		return func(c *fakeConn, m Message) error {
			order = append(order, "a")
			err := next(c, m)
			order = append(order, "a-out")
			return err
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Use(func(next Handler[*fakeConn]) Handler[*fakeConn] {
		return func(c *fakeConn, m Message) error {
			order = append(order, "b")
			return next(c, m)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.On(1, func(c *fakeConn, m Message) error {
		order = append(order, "h")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h, ok := r.Lookup(1)
	if !ok {
		t.Fatal("handler not found")
	}
	if err := h(&fakeConn{}, Message{Event: 1}); err != nil {
		t.Fatalf("dispatch = %v", err)
	}
	want := []string{"a", "b", "h", "a-out"}
	if len(order) != len(want) {
		t.Fatalf("order = %v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestUseAfterOnRejected(t *testing.T) {
	r := NewRegistry[*fakeConn]()
	if err := r.On(1, func(c *fakeConn, m Message) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := r.Use(func(next Handler[*fakeConn]) Handler[*fakeConn] { return next }); err == nil {
		t.Fatal("Use after On accepted")
	}
}

func TestLookupMissingHandler(t *testing.T) {
	r := NewRegistry[*fakeConn]()
	if _, ok := r.Lookup(9); ok {
		t.Fatal("unexpected handler")
	}
}

func TestHandlerErrorPropagates(t *testing.T) {
	r := NewRegistry[*fakeConn]()
	boom := errors.New("boom")
	if err := r.On(1, func(c *fakeConn, m Message) error { return boom }); err != nil {
		t.Fatal(err)
	}
	h, ok := r.Lookup(1)
	if !ok {
		t.Fatal("handler not found")
	}
	if err := h(&fakeConn{}, Message{Event: 1}); !errors.Is(err, boom) {
		t.Fatalf("dispatch = %v", err)
	}
}
