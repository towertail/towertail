package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// fakeChannels accepts each channel with a name. Test fails for a
// target that has "fail".
type fakeChannels struct{ tested []store.Channel }

func (f *fakeChannels) Validate(ch store.Channel) error {
	if ch.Name == "" {
		return errors.New("name is required")
	}
	return nil
}

func (f *fakeChannels) Test(_ context.Context, ch store.Channel) error {
	f.tested = append(f.tested, ch)
	if strings.Contains(ch.Target, "fail") {
		return errors.New("404 Not Found")
	}
	return nil
}

func TestChannelsCRUD(t *testing.T) {
	e := newScanEnv(t)
	body := `{"kind":"webhook","name":"ops","target":"https://example.com/h","secret":"s3cret","route":{"minTint":"critical","tags":["prod"]}}`
	rec := e.do(http.MethodPost, "/v1/channels", e.user, []byte(body))
	wantCode(t, rec, http.StatusCreated)
	if strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("the reply has the secret: %s", rec.Body)
	}
	var ch wire.Channel
	decode(t, rec.Body.Bytes(), &ch)
	if !ch.Enabled || !ch.HasSecret || ch.Route.MinTint != wire.TintCritical || ch.Route.Tags[0] != "prod" {
		t.Fatalf("create: %+v", ch)
	}
	path := "/v1/channels/" + ch.ID.String()

	wantCode(t, e.do(http.MethodPost, "/v1/channels", e.user, []byte(`{"kind":"slack"}`)), http.StatusBadRequest)
	wantCode(t, e.do(http.MethodGet, path, e.otherUser, nil), http.StatusNotFound)
	wantCode(t, e.do(http.MethodPut, path, e.otherUser, []byte(body)), http.StatusNotFound)
	wantCode(t, e.do(http.MethodDelete, path, e.otherUser, nil), http.StatusNotFound)

	// A PUT with no target and no secret keeps them.
	rec = e.do(http.MethodPut, path, e.user, []byte(`{"kind":"webhook","name":"ops-2","enabled":false}`))
	wantCode(t, rec, http.StatusOK)
	ch = wire.Channel{}
	decode(t, rec.Body.Bytes(), &ch)
	if ch.Name != "ops-2" || ch.Enabled || ch.Target != "https://example.com/h" || !ch.HasSecret || ch.Route.MinTint != "" {
		t.Fatalf("update: %+v", ch)
	}
	rec = e.do(http.MethodPut, path, e.user, []byte(`{"kind":"webhook","name":"ops-2","secret":""}`))
	ch = wire.Channel{}
	decode(t, rec.Body.Bytes(), &ch)
	if ch.HasSecret || ch.Enabled {
		t.Fatalf("remove secret: %+v", ch)
	}

	var list []wire.Channel
	decode(t, e.do(http.MethodGet, "/v1/channels", e.user, nil).Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	decode(t, e.do(http.MethodGet, "/v1/channels", e.otherUser, nil).Body.Bytes(), &list)
	if len(list) != 0 {
		t.Fatalf("other org list: %+v", list)
	}

	wantCode(t, e.do(http.MethodDelete, path, e.user, nil), http.StatusNoContent)
	wantCode(t, e.do(http.MethodDelete, path, e.user, nil), http.StatusNotFound)
	wantCode(t, e.do(http.MethodGet, "/v1/channels", e.samplerOn, nil), http.StatusForbidden)
}

func TestChannelTest(t *testing.T) {
	e := newScanEnv(t)
	create := func(target string) string {
		rec := e.do(http.MethodPost, "/v1/channels", e.user, []byte(`{"kind":"webhook","name":"x","target":"`+target+`"}`))
		wantCode(t, rec, http.StatusCreated)
		var ch wire.Channel
		decode(t, rec.Body.Bytes(), &ch)
		return "/v1/channels/" + ch.ID.String() + "/test"
	}
	var res wire.ChannelTestResponse
	rec := e.do(http.MethodPost, create("https://ok.example.com"), e.user, nil)
	wantCode(t, rec, http.StatusOK)
	decode(t, rec.Body.Bytes(), &res)
	if !res.OK {
		t.Errorf("ok channel: %+v", res)
	}
	decode(t, e.do(http.MethodPost, create("https://fail.example.com"), e.user, nil).Body.Bytes(), &res)
	if res.OK || res.Error != "404 Not Found" {
		t.Errorf("failed channel: %+v", res)
	}
}
