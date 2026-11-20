package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

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

// blockKinds blocks the listed channel kinds.
type blockKinds []store.ChannelKind

func (b blockKinds) ChannelAllowed(_ context.Context, _ uuid.UUID, k store.ChannelKind) bool {
	return !slices.Contains(b, k)
}

func TestCreateChannelBlocked(t *testing.T) {
	e := newScanEnv(t)
	e.srv.channelLimits = blockKinds{store.ChannelSlack}
	slack := []byte(`{"kind":"slack","name":"s","target":"https://hooks.example.com/x"}`)

	rec := e.do(http.MethodPost, "/v1/channels", e.user, slack)
	wantCode(t, rec, http.StatusPaymentRequired)
	if !strings.Contains(rec.Body.String(), "plan_limit") || !strings.Contains(rec.Body.String(), "needs the Team plan") {
		t.Fatalf("body: %s", rec.Body)
	}
	rec = e.do(http.MethodPost, "/v1/channels", e.user, []byte(`{"kind":"email","name":"m","target":"ops@example.com"}`))
	wantCode(t, rec, http.StatusCreated)
	var mail wire.Channel
	decode(t, rec.Body.Bytes(), &mail)

	// An update that changes the kind to a blocked kind gives 402.
	wantCode(t, e.do(http.MethodPut, "/v1/channels/"+mail.ID.String(), e.user, slack), http.StatusPaymentRequired)

	// A channel of a kind that the plan blocks later stays editable, but a test send gives 402.
	e.srv.channelLimits = nil
	rec = e.do(http.MethodPost, "/v1/channels", e.user, slack)
	wantCode(t, rec, http.StatusCreated)
	var sl wire.Channel
	decode(t, rec.Body.Bytes(), &sl)
	e.srv.channelLimits = blockKinds{store.ChannelSlack}
	path := "/v1/channels/" + sl.ID.String()
	wantCode(t, e.do(http.MethodPut, path, e.user, []byte(`{"kind":"slack","name":"s2","enabled":false}`)), http.StatusOK)
	wantCode(t, e.do(http.MethodPost, path+"/test", e.user, nil), http.StatusPaymentRequired)
	wantCode(t, e.do(http.MethodPost, "/v1/channels/"+mail.ID.String()+"/test", e.user, nil), http.StatusOK)
}

func TestListChannelsBlocked(t *testing.T) {
	e := newScanEnv(t)
	create := func(body string) wire.Channel {
		rec := e.do(http.MethodPost, "/v1/channels", e.user, []byte(body))
		wantCode(t, rec, http.StatusCreated)
		var ch wire.Channel
		decode(t, rec.Body.Bytes(), &ch)
		return ch
	}
	sl := create(`{"kind":"slack","name":"s","target":"https://hooks.example.com/x"}`)
	mail := create(`{"kind":"email","name":"m","target":"ops@example.com"}`)
	e.srv.channelLimits = blockKinds{store.ChannelSlack}

	var list []wire.Channel
	decode(t, e.do(http.MethodGet, "/v1/channels", e.user, nil).Body.Bytes(), &list)
	blocked := map[uuid.UUID]bool{}
	for _, ch := range list {
		blocked[ch.ID] = ch.Blocked
	}
	if len(list) != 2 || !blocked[sl.ID] || blocked[mail.ID] {
		t.Fatalf("list: %+v", list)
	}
	var got wire.Channel
	decode(t, e.do(http.MethodGet, "/v1/channels/"+sl.ID.String(), e.user, nil).Body.Bytes(), &got)
	if !got.Blocked {
		t.Fatalf("get: %+v", got)
	}
}
