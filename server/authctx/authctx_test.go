package authctx

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

func TestWithToken(t *testing.T) {
	want := store.Token{ID: uuid.New(), OrgID: uuid.New(), Kind: store.TokenKindAPI}
	got, ok := FromContext(WithToken(context.Background(), want))
	if !ok || got.ID != want.ID || got.OrgID != want.OrgID {
		t.Fatalf("FromContext = %+v, %v; want %+v", got, ok, want)
	}
	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("empty context gives a token")
	}
}
