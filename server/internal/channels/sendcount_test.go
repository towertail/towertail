package channels

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestSendCache(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1700000000, 0)}
	c := newSendCache(5*time.Minute, 2, clock.now)
	k := sendKey{org: uuid.New(), node: uuid.New(), metric: "cpu"}

	if _, ok := c.get(k); ok {
		t.Fatal("empty cache has an entry")
	}
	c.add(k, 1) // no entry: no effect
	if _, ok := c.get(k); ok {
		t.Fatal("add made an entry")
	}
	c.put(k, 10)
	c.add(k, 1)
	if n, ok := c.get(k); !ok || n != 11 {
		t.Errorf("get = %d, %v, want 11", n, ok)
	}
	clock.t = clock.t.Add(4 * time.Minute)
	if _, ok := c.get(k); !ok {
		t.Error("entry expired too early")
	}
	clock.t = clock.t.Add(time.Minute)
	if _, ok := c.get(k); ok {
		t.Error("entry did not expire after 5 min")
	}
}

func TestSendCacheFull(t *testing.T) {
	c := newSendCache(time.Hour, 2, time.Now)
	k := func(m string) sendKey { return sendKey{metric: m} }
	c.put(k("a"), 1)
	c.put(k("b"), 2)
	c.put(k("a"), 3) // an update does not drop
	if n, ok := c.get(k("b")); !ok || n != 2 {
		t.Fatalf("b = %d, %v", n, ok)
	}
	c.put(k("c"), 4)
	if _, ok := c.get(k("a")); ok {
		t.Error("a stayed in a full cache")
	}
	if n, ok := c.get(k("c")); !ok || n != 4 {
		t.Errorf("c = %d, %v", n, ok)
	}
}

func TestRouterSendCacheTTL(t *testing.T) {
	m, cnt := &fakeMailer{}, &fakeCounter{n: 7}
	r := newCountRouter(cnt, m, channel(store.ChannelEmail, "ops@example.com"))
	clock := &fakeClock{t: time.Now()}
	r.sends = newSendCache(5*time.Minute, 10, clock.now)
	a := testAlert()
	notify := func() {
		t.Helper()
		if err := r.Notify(t.Context(), a); err != nil {
			t.Fatal(err)
		}
	}
	notify()
	clock.t = clock.t.Add(time.Minute)
	notify()
	if cnt.count() != 1 {
		t.Fatalf("calls = %d, want 1", cnt.count())
	}
	clock.t = clock.t.Add(5 * time.Minute)
	notify()
	if cnt.count() != 2 || m.alert.RuleEmails != 7 {
		t.Errorf("calls = %d, RuleEmails = %d, want 2 and 7", cnt.count(), m.alert.RuleEmails)
	}
}
