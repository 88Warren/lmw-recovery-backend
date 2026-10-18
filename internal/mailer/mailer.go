package mailer

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"html/template"
	"log"
	"net/smtp"
	"strings"
	"time"
)

// studioLocation is the timezone slots are displayed in. Slots are stored in
// UTC, so every human-facing time must be converted to this zone before
// formatting — otherwise a 9:00 BST slot (stored as 08:00Z) prints as 8am.
var studioLocation = mustLoadLocation("Europe/London")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		log.Printf("mailer: WARNING could not load location %q, falling back to UTC: %v", name, err)
		return time.UTC
	}
	return loc
}

// localTime converts a stored (UTC) time into the studio's local timezone.
func localTime(t time.Time) time.Time { return t.In(studioLocation) }

// Mailer sends email via Ionos SMTP (STARTTLS on port 587).
type Mailer struct {
	host     string
	port     int
	user     string
	password string
	from     string
	dryRun   bool
}

// New creates a Mailer.
func New(host string, port int, user, password, from string, dryRun bool) *Mailer {
	return &Mailer{host: host, port: port, user: user, password: password, from: from, dryRun: dryRun}
}

// Message is a simple email message.
type Message struct {
	To      string
	Subject string
	HTML    string
}

// Send delivers a single message.
func (m *Mailer) Send(msg Message) error {
	if m.dryRun {
		log.Printf("MAIL DRY-RUN to=%s subject=%q (not sent — MAIL_DRY_RUN=true)", msg.To, msg.Subject)
		return nil
	}
	addr := fmt.Sprintf("%s:%d", m.host, m.port)

	auth := smtp.PlainAuth("", m.user, m.password, m.host)

	// Build RFC 2822 message.
	var buf bytes.Buffer
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString(fmt.Sprintf("From: %s\r\n", m.from))
	buf.WriteString(fmt.Sprintf("To: %s\r\n", msg.To))
	buf.WriteString(fmt.Sprintf("Subject: %s\r\n", msg.Subject))
	buf.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	buf.WriteString("\r\n")
	buf.WriteString(msg.HTML)

	// Ionos requires STARTTLS on 587.
	client, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsCfg := &tls.Config{ServerName: m.host}
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}

	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}

	// Extract bare address for MAIL FROM / RCPT TO.
	fromAddr := extractAddr(m.from)
	if err := client.Mail(fromAddr); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := client.Rcpt(msg.To); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}

	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := buf.WriteTo(wc); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("smtp close: %w", err)
	}

	return client.Quit()
}

// extractAddr pulls the bare email from "Display Name <email@example.com>" or
// returns the string as-is if it contains no angle brackets.
func extractAddr(s string) string {
	start := strings.Index(s, "<")
	end := strings.Index(s, ">")
	if start != -1 && end != -1 && end > start {
		return s[start+1 : end]
	}
	return s
}

// ── Pre-built email templates ─────────────────────────────────────────────────

// BookingConfirmationData is the data passed to the client confirmation template.
type BookingConfirmationData struct {
	ClientName string
	Treatment  string
	StartsAt   time.Time
	Notes      string

	// Location details. When StudioAddress is empty the email falls back to the
	// generic "full address sent separately" line.
	StudioAddress    string
	StudioWhat3Words string

	// ManageURL, when set, links the client to the self-service page where they
	// can reschedule or request a cancellation. Omitted on cancellation emails.
	ManageURL string
}

// LocalStartsAt renders the booking start time in the studio's timezone. Used in
// the template so a 9am BST slot (stored as 08:00Z) displays as 9am, not 8am.
func (d BookingConfirmationData) LocalStartsAt() string {
	return localTime(d.StartsAt).Format("Monday 2 January 2006, 3:04pm")
}

// What3WordsClean returns the w3w address without a leading "///" so the
// template can render it consistently and build a map link.
func (d BookingConfirmationData) What3WordsClean() string {
	return strings.TrimPrefix(strings.TrimSpace(d.StudioWhat3Words), "///")
}

var bookingConfirmationTmpl = template.Must(template.New("booking_confirm").Parse(`
<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <meta name="color-scheme" content="light only">
  <meta name="supported-color-schemes" content="light">
</head>
<body style="font-family:'Montserrat',Helvetica,sans-serif;background:#f4f0eb;color:#2a2622;margin:0;padding:0;">
  <table width="100%" cellpadding="0" cellspacing="0" style="background:#f4f0eb;">
    <tr><td align="center" style="padding:40px 20px;">
      <table width="560" cellpadding="0" cellspacing="0" style="background:#ffffff;border-top:3px solid #C9A15B;">
        <tr><td style="padding:40px 40px 20px;">
          <p style="font-size:11px;letter-spacing:0.3em;text-transform:uppercase;color:#b8904a;margin:0 0 16px;">LMW Recovery</p>
          <h1 style="font-family:Georgia,serif;font-size:28px;font-weight:400;color:#2a2622;margin:0 0 24px;line-height:1.2;">
            You're booked in, {{.ClientName}}.
          </h1>
          <p style="font-size:13px;color:#555150;line-height:1.8;margin:0 0 32px;">
            Your session has been received and is now confirmed. Here are the details:
          </p>
          <table width="100%" cellpadding="0" cellspacing="0" style="border-top:1px solid #e7ddc9;margin-bottom:32px;">
            <tr>
              <td style="padding:12px 0;border-bottom:1px solid #efe8da;font-size:10px;letter-spacing:0.15em;text-transform:uppercase;color:#b8904a;width:140px;">Treatment</td>
              <td style="padding:12px 0;border-bottom:1px solid #efe8da;font-size:13px;color:#2a2622;">{{.Treatment}}</td>
            </tr>
            <tr>
              <td style="padding:12px 0;border-bottom:1px solid #efe8da;font-size:10px;letter-spacing:0.15em;text-transform:uppercase;color:#b8904a;">Date &amp; Time</td>
              <td style="padding:12px 0;border-bottom:1px solid #efe8da;font-size:13px;color:#2a2622;">{{.LocalStartsAt}}</td>
            </tr>
            <tr>
              <td style="padding:12px 0;border-bottom:1px solid #efe8da;font-size:10px;letter-spacing:0.15em;text-transform:uppercase;color:#b8904a;vertical-align:top;">Location</td>
              <td style="padding:12px 0;border-bottom:1px solid #efe8da;font-size:13px;color:#2a2622;">
                Private studio, Llantrisant RCT
                {{if .StudioAddress}}<br><span style="color:#555150;font-size:12px;line-height:1.6;">{{.StudioAddress}}</span>{{else}}<br><span style="color:#8a8178;font-size:11px;">Full address sent separately.</span>{{end}}
                {{with .What3WordsClean}}<br><span style="color:#8a8178;font-size:11px;">what3words: <a href="https://what3words.com/{{.}}" style="color:#b8904a;text-decoration:none;">///{{.}}</a></span>{{end}}
              </td>
            </tr>
            {{if .Notes}}
            <tr>
              <td style="padding:12px 0;font-size:10px;letter-spacing:0.15em;text-transform:uppercase;color:#b8904a;">Your Notes</td>
              <td style="padding:12px 0;font-size:13px;color:#555150;">{{.Notes}}</td>
            </tr>
            {{end}}
          </table>
          <p style="font-size:12px;color:#8a8178;line-height:1.8;margin:0 0 16px;">
            You'll receive a separate email with a short health questionnaire — please complete it before your session.
          </p>
          {{if .ManageURL}}
          <p style="font-size:12px;color:#8a8178;line-height:1.8;margin:0 0 16px;">
            Need to change your plans? You can <a href="{{.ManageURL}}" style="color:#b8904a;">reschedule or request a cancellation here</a> (up to 24 hours before your session).
          </p>
          {{end}}
          <p style="font-size:12px;color:#8a8178;line-height:1.8;margin:0 0 40px;">
            See you soon,<br>
            <span style="color:#b8904a;">Laura</span>
          </p>
          <p style="font-size:10px;color:#b3a99c;margin:0;">
            LMW Recovery · Llantrisant, RCT · info@lmwrecovery.co.uk
          </p>
        </td></tr>
      </table>
    </td></tr>
  </table>
</body>
</html>
`))

// BookingNotificationData is the data sent to Laura when a new booking arrives.
type BookingNotificationData struct {
	ClientName  string
	ClientEmail string
	ClientPhone string
	Treatment   string
	StartsAt    time.Time
	Notes       string
}

// LocalStartsAt renders the booking start in the studio's timezone.
func (d BookingNotificationData) LocalStartsAt() string {
	return localTime(d.StartsAt).Format("Monday 2 January 2006, 3:04pm")
}

var bookingNotificationTmpl = template.Must(template.New("booking_notify").Parse(`
<!DOCTYPE html>
<html>
<body style="font-family:'Montserrat',Helvetica,sans-serif;background:#f5f5f5;color:#171717;padding:40px 20px;">
  <div style="max-width:560px;margin:0 auto;background:#fff;border-top:3px solid #C9A15B;padding:32px;">
    <p style="font-size:10px;letter-spacing:0.25em;text-transform:uppercase;color:#C9A15B;margin:0 0 12px;">New Booking</p>
    <h2 style="font-family:Georgia,serif;font-size:22px;font-weight:400;margin:0 0 24px;">{{.ClientName}} has booked a session</h2>
    <table width="100%" cellpadding="0" cellspacing="0" style="border-top:1px solid #e5e5e5;">
      <tr><td style="padding:10px 0;border-bottom:1px solid #f0f0f0;font-size:10px;letter-spacing:0.15em;text-transform:uppercase;color:#C9A15B;width:130px;">Treatment</td><td style="padding:10px 0;border-bottom:1px solid #f0f0f0;font-size:13px;">{{.Treatment}}</td></tr>
      <tr><td style="padding:10px 0;border-bottom:1px solid #f0f0f0;font-size:10px;letter-spacing:0.15em;text-transform:uppercase;color:#C9A15B;">Date &amp; Time</td><td style="padding:10px 0;border-bottom:1px solid #f0f0f0;font-size:13px;">{{.LocalStartsAt}}</td></tr>
      <tr><td style="padding:10px 0;border-bottom:1px solid #f0f0f0;font-size:10px;letter-spacing:0.15em;text-transform:uppercase;color:#C9A15B;">Email</td><td style="padding:10px 0;border-bottom:1px solid #f0f0f0;font-size:13px;"><a href="mailto:{{.ClientEmail}}">{{.ClientEmail}}</a></td></tr>
      <tr><td style="padding:10px 0;border-bottom:1px solid #f0f0f0;font-size:10px;letter-spacing:0.15em;text-transform:uppercase;color:#C9A15B;">Phone</td><td style="padding:10px 0;border-bottom:1px solid #f0f0f0;font-size:13px;">{{if .ClientPhone}}{{.ClientPhone}}{{else}}—{{end}}</td></tr>
      {{if .Notes}}<tr><td style="padding:10px 0;font-size:10px;letter-spacing:0.15em;text-transform:uppercase;color:#C9A15B;">Notes</td><td style="padding:10px 0;font-size:13px;color:#555;">{{.Notes}}</td></tr>{{end}}
    </table>
  </div>
</body>
</html>
`))

// BookingCancellationData is sent to a client when their booking is cancelled.
type BookingCancellationData struct {
	ClientName string
	Treatment  string
	StartsAt   time.Time
	Refunded   bool
	// IsSubscription true when this cancels the whole Maintenance Plan rather
	// than a single session.
	IsSubscription bool
	// BookURL links back to the site to book again in future.
	BookURL string
}

func (d BookingCancellationData) LocalStartsAt() string {
	return localTime(d.StartsAt).Format("Monday 2 January 2006, 3:04pm")
}

var bookingCancellationTmpl = template.Must(template.New("booking_cancel").Parse(`
<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <meta name="color-scheme" content="light only">
  <meta name="supported-color-schemes" content="light">
</head>
<body style="font-family:'Montserrat',Helvetica,sans-serif;background:#f4f0eb;color:#2a2622;margin:0;padding:0;">
  <table width="100%" cellpadding="0" cellspacing="0" style="background:#f4f0eb;">
    <tr><td align="center" style="padding:40px 20px;">
      <table width="560" cellpadding="0" cellspacing="0" style="background:#ffffff;border-top:3px solid #C9A15B;">
        <tr><td style="padding:40px 40px 20px;">
          <p style="font-size:11px;letter-spacing:0.3em;text-transform:uppercase;color:#b8904a;margin:0 0 16px;">LMW Recovery</p>
          <h1 style="font-family:Georgia,serif;font-size:28px;font-weight:400;color:#2a2622;margin:0 0 24px;line-height:1.2;">
            {{if .IsSubscription}}Your plan has been cancelled{{else}}Your session has been cancelled{{end}}, {{.ClientName}}.
          </h1>
          {{if .IsSubscription}}
          <p style="font-size:13px;color:#555150;line-height:1.8;margin:0 0 24px;">
            Your Maintenance Plan subscription has been cancelled and you won't be charged again. Any sessions already booked and paid for within your plan still stand — we'll see you for those. Only sessions beyond your paid period have been released.
          </p>
          {{else}}
          <table width="100%" cellpadding="0" cellspacing="0" style="border-top:1px solid #e7ddc9;margin-bottom:24px;">
            <tr>
              <td style="padding:12px 0;border-bottom:1px solid #efe8da;font-size:10px;letter-spacing:0.15em;text-transform:uppercase;color:#b8904a;width:140px;">Treatment</td>
              <td style="padding:12px 0;border-bottom:1px solid #efe8da;font-size:13px;color:#2a2622;">{{.Treatment}}</td>
            </tr>
            <tr>
              <td style="padding:12px 0;border-bottom:1px solid #efe8da;font-size:10px;letter-spacing:0.15em;text-transform:uppercase;color:#b8904a;">Was booked for</td>
              <td style="padding:12px 0;border-bottom:1px solid #efe8da;font-size:13px;color:#2a2622;">{{.LocalStartsAt}}</td>
            </tr>
          </table>
          {{end}}
          {{if .Refunded}}
          <p style="font-size:13px;color:#555150;line-height:1.8;margin:0 0 24px;">
            A full refund has been issued — it may take a few days to appear on your statement.
          </p>
          {{end}}
          {{if .BookURL}}
          <p style="margin:0 0 32px;">
            <a href="{{.BookURL}}" style="display:inline-block;font-size:12px;font-weight:600;letter-spacing:0.18em;text-transform:uppercase;color:#171717;background:#C9A15B;padding:14px 28px;text-decoration:none;">Book another session</a>
          </p>
          {{end}}
          <p style="font-size:12px;color:#8a8178;line-height:1.8;margin:0 0 40px;">
            We hope to see you again soon.<br>
            <span style="color:#b8904a;">Laura</span>
          </p>
          <p style="font-size:10px;color:#b3a99c;margin:0;">
            LMW Recovery · Llantrisant, RCT · info@lmwrecovery.co.uk
          </p>
        </td></tr>
      </table>
    </td></tr>
  </table>
</body>
</html>
`))

// SendBookingCancellation emails a client that their booking (or plan) was cancelled.
func (m *Mailer) SendBookingCancellation(to string, data BookingCancellationData) error {
	html, err := render(bookingCancellationTmpl, data)
	if err != nil {
		return err
	}
	subject := "Your LMW Recovery session has been cancelled"
	if data.IsSubscription {
		subject = "Your LMW Recovery plan has been cancelled"
	}
	return m.Send(Message{To: to, Subject: subject, HTML: html})
}

// ContactNotificationData is sent to Laura when someone submits the contact form.
type ContactNotificationData struct {
	Name    string
	Email   string
	Phone   string
	Message string
}

var contactNotificationTmpl = template.Must(template.New("contact_notify").Parse(`
<!DOCTYPE html>
<html>
<body style="font-family:'Montserrat',Helvetica,sans-serif;background:#f5f5f5;color:#171717;padding:40px 20px;">
  <div style="max-width:560px;margin:0 auto;background:#fff;border-top:3px solid #C9A15B;padding:32px;">
    <p style="font-size:10px;letter-spacing:0.25em;text-transform:uppercase;color:#C9A15B;margin:0 0 12px;">New Enquiry</p>
    <h2 style="font-family:Georgia,serif;font-size:22px;font-weight:400;margin:0 0 24px;">Message from {{.Name}}</h2>
    <table width="100%" cellpadding="0" cellspacing="0" style="border-top:1px solid #e5e5e5;margin-bottom:24px;">
      <tr><td style="padding:10px 0;border-bottom:1px solid #f0f0f0;font-size:10px;letter-spacing:0.15em;text-transform:uppercase;color:#C9A15B;width:100px;">Email</td><td style="padding:10px 0;border-bottom:1px solid #f0f0f0;font-size:13px;"><a href="mailto:{{.Email}}">{{.Email}}</a></td></tr>
      <tr><td style="padding:10px 0;border-bottom:1px solid #f0f0f0;font-size:10px;letter-spacing:0.15em;text-transform:uppercase;color:#C9A15B;">Phone</td><td style="padding:10px 0;border-bottom:1px solid #f0f0f0;font-size:13px;">{{if .Phone}}{{.Phone}}{{else}}—{{end}}</td></tr>
    </table>
    <p style="font-size:13px;color:#555;line-height:1.8;white-space:pre-wrap;">{{.Message}}</p>
  </div>
</body>
</html>
`))

// ContactAckData is sent to the person who submitted the contact form.
var contactAckTmpl = template.Must(template.New("contact_ack").Parse(`
<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <meta name="color-scheme" content="light only">
  <meta name="supported-color-schemes" content="light">
</head>
<body style="font-family:'Montserrat',Helvetica,sans-serif;background:#f4f0eb;color:#2a2622;margin:0;padding:0;">
  <table width="100%" cellpadding="0" cellspacing="0" style="background:#f4f0eb;">
    <tr><td align="center" style="padding:40px 20px;">
      <table width="560" cellpadding="0" cellspacing="0" style="background:#ffffff;border-top:3px solid #C9A15B;">
        <tr><td style="padding:40px;">
          <p style="font-size:11px;letter-spacing:0.3em;text-transform:uppercase;color:#b8904a;margin:0 0 16px;">LMW Recovery</p>
          <h1 style="font-family:Georgia,serif;font-size:26px;font-weight:400;color:#2a2622;margin:0 0 20px;">Thanks, {{.Name}}.</h1>
          <p style="font-size:13px;color:#555150;line-height:1.8;margin:0 0 24px;">
            I've received your message and will get back to you as soon as possible — usually within 24 hours.
          </p>
          <p style="font-size:12px;color:#8a8178;line-height:1.8;margin:0 0 40px;">
            Laura<br><span style="color:#b8904a;">LMW Recovery</span>
          </p>
          <p style="font-size:10px;color:#b3a99c;margin:0;">info@lmwrecovery.co.uk · lmwrecovery.co.uk</p>
        </td></tr>
      </table>
    </td></tr>
  </table>
</body>
</html>
`))

// ── Rendered send helpers ─────────────────────────────────────────────────────

func (m *Mailer) SendBookingConfirmation(to string, data BookingConfirmationData) error {
	html, err := render(bookingConfirmationTmpl, data)
	if err != nil {
		return err
	}
	return m.Send(Message{
		To:      to,
		Subject: fmt.Sprintf("Your LMW Recovery session — %s", localTime(data.StartsAt).Format("2 Jan 2006")),
		HTML:    html,
	})
}

func (m *Mailer) SendBookingNotification(to string, data BookingNotificationData) error {
	html, err := render(bookingNotificationTmpl, data)
	if err != nil {
		return err
	}
	return m.Send(Message{
		To:      to,
		Subject: fmt.Sprintf("New booking: %s — %s", data.ClientName, localTime(data.StartsAt).Format("2 Jan 3:04pm")),
		HTML:    html,
	})
}

func (m *Mailer) SendContactNotification(to string, data ContactNotificationData) error {
	html, err := render(contactNotificationTmpl, data)
	if err != nil {
		return err
	}
	return m.Send(Message{
		To:      to,
		Subject: fmt.Sprintf("New enquiry from %s", data.Name),
		HTML:    html,
	})
}

func (m *Mailer) SendContactAck(to string, data ContactNotificationData) error {
	html, err := render(contactAckTmpl, data)
	if err != nil {
		return err
	}
	return m.Send(Message{
		To:      to,
		Subject: "Thanks for getting in touch — LMW Recovery",
		HTML:    html,
	})
}

// memberLinkTmpl is the passwordless magic-link email sent to a member who wants
// to book their plan sessions. The link proves they control the inbox.
var memberLinkTmpl = template.Must(template.New("member_link").Parse(`
<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <meta name="color-scheme" content="light only">
  <meta name="supported-color-schemes" content="light">
</head>
<body style="font-family:'Montserrat',Helvetica,sans-serif;background:#f4f0eb;color:#2a2622;margin:0;padding:0;">
  <table width="100%" cellpadding="0" cellspacing="0" style="background:#f4f0eb;">
    <tr><td align="center" style="padding:40px 20px;">
      <table width="560" cellpadding="0" cellspacing="0" style="background:#ffffff;border-top:3px solid #C9A15B;">
        <tr><td style="padding:40px;">
          <p style="font-size:11px;letter-spacing:0.3em;text-transform:uppercase;color:#b8904a;margin:0 0 16px;">LMW Recovery</p>
          <h1 style="font-family:Georgia,serif;font-size:26px;font-weight:400;color:#2a2622;margin:0 0 20px;">Book your plan sessions</h1>
          <p style="font-size:13px;color:#555150;line-height:1.8;margin:0 0 28px;">
            Tap the button below to confirm it's you, then choose this month's two Maintenance Plan sessions. This link expires in 30 minutes and can only be used by you.
          </p>
          <p style="margin:0 0 32px;">
            <a href="{{.Link}}" style="display:inline-block;font-size:12px;font-weight:600;letter-spacing:0.18em;text-transform:uppercase;color:#171717;background:#C9A15B;padding:14px 28px;text-decoration:none;">Book my sessions</a>
          </p>
          <p style="font-size:11px;color:#8a8178;line-height:1.7;margin:0 0 40px;">
            If you didn't request this, you can ignore this email — nothing will happen.
          </p>
          <p style="font-size:10px;color:#b3a99c;margin:0;">info@lmwrecovery.co.uk · lmwrecovery.co.uk</p>
        </td></tr>
      </table>
    </td></tr>
  </table>
</body>
</html>
`))

// SendMemberLink emails a passwordless magic link to a member.
func (m *Mailer) SendMemberLink(to, link string) error {
	html, err := render(memberLinkTmpl, map[string]string{"Link": link})
	if err != nil {
		return err
	}
	return m.Send(Message{
		To:      to,
		Subject: "Book your LMW Recovery plan sessions",
		HTML:    html,
	})
}

func render(tmpl *template.Template, data any) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render template %s: %w", tmpl.Name(), err)
	}
	return buf.String(), nil
}
