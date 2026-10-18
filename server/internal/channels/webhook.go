package channels

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// webhook posts a wire.AlertPayload. With a secret, it signs
// "<timestamp>.<body>" with HMAC-SHA256. The timestamp is the time of
// the send, so the receiver can refuse old requests.
type webhook struct{ p *poster }

func (w webhook) send(ctx context.Context, ch store.Channel, a notify.Alert) error {
	kind := "alert"
	if a.Test {
		kind = "test"
	}
	body, err := json.Marshal(wire.AlertPayload{
		V: 1, Type: kind, EventID: a.EventID, OrgID: a.OrgID, NodeID: a.NodeID, NodeName: a.NodeName,
		Tags: a.Tags, Metric: a.Metric, From: a.From, To: a.To, Value: a.Value, At: a.At,
		Title: a.Title(), Text: a.Text(),
	})
	if err != nil {
		return &sendError{err: err}
	}
	headers := map[string]string{"X-Towertail-Event": kind}
	if ch.Secret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		headers["X-Towertail-Timestamp"] = ts
		headers["X-Towertail-Signature"] = "sha256=" + sign(ch.Secret, ts, body)
	}
	return w.p.postRaw(ctx, ch.Target, body, headers)
}

func sign(secret, ts string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}
