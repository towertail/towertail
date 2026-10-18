package channels

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
)

// email sends plain text mail through the SMTP server of the config.
type email struct {
	cfg config.SMTPConfig
	// tlsConfig is for tests. Nil means the system roots.
	tlsConfig *tls.Config
}

// recipients parses the comma-separated target.
func recipients(target string) ([]string, error) {
	list, err := mail.ParseAddressList(target)
	if err != nil {
		return nil, fmt.Errorf("email target: %w", err)
	}
	if len(list) == 0 || len(list) > 20 {
		return nil, errors.New("email target: give 1 to 20 addresses")
	}
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.Address
	}
	return out, nil
}

func (e email) send(ctx context.Context, ch store.Channel, a notify.Alert) error {
	to, err := recipients(ch.Target)
	if err != nil {
		return &sendError{err: err}
	}
	from, err := mail.ParseAddress(e.cfg.From)
	if err != nil {
		return &sendError{err: fmt.Errorf("smtp from: %w", err)}
	}
	msg := e.message(from, to, a)

	c, err := e.dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if e.cfg.User != "" {
		if err := c.Auth(smtp.PlainAuth("", e.cfg.User, e.cfg.Password, e.cfg.Host)); err != nil {
			return &sendError{err: fmt.Errorf("smtp auth: %w", err)}
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return smtpError(err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return smtpError(err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return smtpError(err)
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return smtpError(err)
	}
	return c.Quit()
}

// dial connects and starts TLS as the config says.
func (e email) dial(ctx context.Context) (*smtp.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	addr := net.JoinHostPort(e.cfg.Host, strconv.Itoa(e.cfg.Port))
	tc := e.tlsConfig
	if tc == nil {
		tc = &tls.Config{ServerName: e.cfg.Host, MinVersion: tls.VersionTLS12}
	}
	var (
		conn net.Conn
		err  error
	)
	if e.cfg.TLS == "tls" {
		conn, err = (&tls.Dialer{Config: tc}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl.Add(sendTimeout))
	}
	c, err := smtp.NewClient(conn, e.cfg.Host)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if e.cfg.TLS == "starttls" || e.cfg.TLS == "" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			c.Close()
			return nil, &sendError{err: errors.New("smtp: the server has no STARTTLS")}
		}
		if err := c.StartTLS(tc); err != nil {
			c.Close()
			return nil, err
		}
	}
	return c, nil
}

func (e email) message(from *mail.Address, to []string, a notify.Alert) []byte {
	var b bytes.Buffer
	host := e.cfg.Host
	if i := strings.LastIndex(from.Address, "@"); i >= 0 {
		host = from.Address[i+1:]
	}
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	header := [][2]string{
		{"From", from.String()},
		{"To", strings.Join(to, ", ")},
		{"Subject", mime.QEncoding.Encode("utf-8", a.Title())},
		{"Date", time.Now().UTC().Format(time.RFC1123Z)},
		{"Message-ID", "<" + hex.EncodeToString(id) + "@" + host + ">"},
		{"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=utf-8"},
		{"Content-Transfer-Encoding", "8bit"},
		{"Auto-Submitted", "auto-generated"},
	}
	for _, h := range header {
		fmt.Fprintf(&b, "%s: %s\r\n", h[0], h[1])
	}
	b.WriteString("\r\n")
	body := a.Title() + "\n\n" + a.Text() + "\n\nNode: " + a.NodeName + " (" + a.NodeID.String() + ")\nTime: " +
		a.At.UTC().Format(time.RFC3339) + "\n\n-- \nTowertail\n"
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return b.Bytes()
}

// smtpError makes a 5xx reply final. A 4xx reply is a temporary error.
func smtpError(err error) error {
	var te *textproto.Error
	if errors.As(err, &te) && te.Code >= 500 {
		return &sendError{err: err}
	}
	return &sendError{err: err, retry: true}
}
