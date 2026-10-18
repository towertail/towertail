package api

import (
	"net/http"
	"testing"
)

func TestKillProcessChecksOrg(t *testing.T) {
	e := newScanEnv(t)
	path := "/v1/nodes/" + e.on.String() + "/kill-process?pid=42"
	wantCode(t, e.do(http.MethodPost, path, e.otherUser, nil), http.StatusNotFound)
	wantCode(t, e.do(http.MethodPost, path, e.user, nil), http.StatusAccepted)
}
