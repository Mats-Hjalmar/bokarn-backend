package notify

import (
	"context"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTP delivers over plain SMTP.
//
// No authentication and no TLS: this is the shape a local mail catcher and an
// in-cluster relay both take. A relay on the public internet needs both, and
// wiring them in as optional would leave a production deployment silently
// sending in the clear — so that is a deliberate change to make, not a flag to
// forget to set.
type SMTP struct {
	Addr string
	From string
}

// NewSMTP constructs a transport.
func NewSMTP(host string, port int, from string) *SMTP {
	return &SMTP{
		Addr: net.JoinHostPort(host, strconv.Itoa(port)),
		From: from,
	}
}

// smtpTimeout bounds a send. The dispatcher holds a database transaction open
// across it, so an unbounded dial would hold a connection and a row lock for as
// long as the network cared to.
const smtpTimeout = 10 * time.Second

// Send delivers one message.
func (t *SMTP) Send(ctx context.Context, m Message) error {
	dialer := net.Dialer{Timeout: smtpTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", t.Addr)
	if err != nil {
		return fmt.Errorf("dial smtp %s: %w", t.Addr, err)
	}
	if err := conn.SetDeadline(time.Now().Add(smtpTimeout)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("set smtp deadline: %w", err)
	}

	host, _, _ := net.SplitHostPort(t.Addr)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.Mail(t.From); err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	if err := client.Rcpt(m.To); err != nil {
		return fmt.Errorf("smtp to %s: %w", m.To, err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write([]byte(wire(t.From, m))); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close body: %w", err)
	}
	return client.Quit()
}

// wire assembles the message. The body is UTF-8 text: a Swedish confirmation
// is full of å, ä and ö, and a header claiming us-ascii turns them into
// mojibake in the guest's client.
func wire(from string, m Message) string {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + m.To + "\r\n")
	b.WriteString("Subject: " + encodeHeader(m.Subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(strings.ReplaceAll(m.Body, "\n", "\r\n"))
	return b.String()
}

// encodeHeader wraps a subject in RFC 2047 when it is not pure ASCII. Header
// fields have no charset of their own, so "Din bokning är bekräftad" has to be
// encoded or it arrives mangled.
func encodeHeader(s string) string {
	return mime.QEncoding.Encode("utf-8", s)
}
