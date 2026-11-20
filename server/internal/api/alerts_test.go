package api

import (
	"testing"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

func TestAckedBy(t *testing.T) {
	user := uuid.New()
	withUser := store.Token{ID: uuid.New(), UserID: &user}
	if got := ackedBy(withUser); got != user {
		t.Errorf("token with a user: %s, want %s", got, user)
	}
	plain := store.Token{ID: uuid.New()}
	if got := ackedBy(plain); got != plain.ID {
		t.Errorf("token with no user: %s, want %s", got, plain.ID)
	}
}
