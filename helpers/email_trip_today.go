package helpers

// Trip reminder email with an .ics calendar attachment.

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"mime"
	"net/textproto"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ses"
)

// TripTodayEmailParams is the data envelope for trip reminder email.
type TripTodayEmailParams struct {
	ToEmail       string
	ToName        string
	RideID        string
	StartLocation string
	EndLocation   string
	StartTime     time.Time
	HostName      string
	HostFirst     string
	TotalPrice    uint
	// IsHost selects host-specific copy while reusing the same template.
	IsHost bool
}

// SendTripTodayEmail composes and sends the trip reminder over SES SendRawEmail.
// SendRawEmail is required for the .ics attachment.
//
// Email delivery is best-effort; trip state is not affected by send failures.
func SendTripTodayEmail(p TripTodayEmailParams) error {
	if err := ensureSES(); err != nil {
		return fmt.Errorf("ses init: %w", err)
	}

	subject := "Your UniPool ride is tomorrow"
	if !p.StartTime.After(time.Now()) {
		// Reuse the template if the scheduler sends on the day of the trip.
		subject = "Your UniPool ride is today"
	}

	deepLink := fmt.Sprintf("https://unipool.acmvit.in/ride/%s", p.RideID)
	timeLine := p.StartTime.Format("Mon, 2 Jan · 15:04")

	headline := "Your ride is tomorrow"
	subhead := fmt.Sprintf(
		"You're carpooling with %s. Add it to your calendar and we'll see you at the pickup.",
		safeFirst(p.HostFirst, p.HostName, "your host"),
	)
	if p.IsHost {
		headline = "You're hosting tomorrow"
		subhead = "Your passengers are riding with you. Add it to your calendar — we'll send a reminder the morning of."
	}

	textBody := fmt.Sprintf(
		"UniPool\n\n%s\n\n%s → %s\n%s\n\nFare: ₹%d\nHost: %s\n\nOpen in UniPool: %s\n",
		headline,
		p.StartLocation,
		p.EndLocation,
		timeLine,
		p.TotalPrice,
		safeFirst(p.HostName, p.HostFirst, "your host"),
		deepLink,
	)

	htmlBody := fmt.Sprintf(
		tripTodayEmailTemplate,
		emailWordmarkURL,
		headline,
		subhead,
		htmlEscape(p.StartLocation),
		htmlEscape(p.EndLocation),
		timeLine,
		p.TotalPrice,
		htmlEscape(safeFirst(p.HostName, p.HostFirst, "Your host")),
		deepLink,
	)

	ics := buildRideICS(p)

	raw, err := buildMultipartEmail(sesVerifySender, p.ToEmail, subject, textBody, htmlBody, ics, "unipool-ride.ics")
	if err != nil {
		return fmt.Errorf("build raw: %w", err)
	}

	_, err = sesClient.SendRawEmail(&ses.SendRawEmailInput{
		Source:       aws.String(sesVerifySender),
		Destinations: []*string{aws.String(p.ToEmail)},
		RawMessage:   &ses.RawMessage{Data: raw},
	})
	if err != nil {
		log.Printf("SES SendRawEmail (trip-today) to %s failed: %v", p.ToEmail, err)
		return err
	}
	return nil
}

// buildRideICS renders a minimal RFC5545 VEVENT for the ride.
// UniPool does not store end times, so DTEND defaults to start + 90 minutes.
func buildRideICS(p TripTodayEmailParams) string {
	// Calendar times are UTC with trailing Z. The short generated lines stay
	// readable and work with the clients we target.
	utcStart := p.StartTime.UTC()
	utcEnd := utcStart.Add(90 * time.Minute)
	stamp := time.Now().UTC()
	fmtTime := func(t time.Time) string {
		return t.Format("20060102T150405Z")
	}
	summary := fmt.Sprintf("UniPool: %s -> %s", p.StartLocation, p.EndLocation)
	desc := fmt.Sprintf(
		"Carpooling with %s.\\nFare: INR %d\\nOpen in UniPool: https://unipool.acmvit.in/ride/%s",
		safeFirst(p.HostName, p.HostFirst, "your host"),
		p.TotalPrice,
		p.RideID,
	)
	uid := fmt.Sprintf("ride-%s@unipool.acmvit.in", p.RideID)

	return strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//UniPool//Ride//EN",
		"CALSCALE:GREGORIAN",
		"METHOD:PUBLISH",
		"BEGIN:VEVENT",
		"UID:" + uid,
		"DTSTAMP:" + fmtTime(stamp),
		"DTSTART:" + fmtTime(utcStart),
		"DTEND:" + fmtTime(utcEnd),
		"SUMMARY:" + icsEscape(summary),
		"DESCRIPTION:" + desc,
		"LOCATION:" + icsEscape(p.StartLocation),
		"URL:https://unipool.acmvit.in/ride/" + p.RideID,
		"STATUS:CONFIRMED",
		"END:VEVENT",
		"END:VCALENDAR",
		"",
	}, "\r\n")
}

// buildMultipartEmail assembles the RFC822/MIME message SES SendRawEmail expects:
//
//	multipart/mixed
//	├── multipart/alternative
//	│   ├── text/plain
//	│   └── text/html
//	└── text/calendar  (the .ics attachment)
//
// Hand assembly keeps the fixed nested shape explicit.
func buildMultipartEmail(from, to, subject, text, html, icsBody, icsName string) ([]byte, error) {
	mixedBoundary := mimeBoundary("mixed")
	altBoundary := mimeBoundary("alt")

	headers := textproto.MIMEHeader{}
	headers.Set("From", from)
	headers.Set("To", to)
	headers.Set("Subject", mime.QEncoding.Encode("UTF-8", subject))
	headers.Set("MIME-Version", "1.0")
	headers.Set("Content-Type", fmt.Sprintf(`multipart/mixed; boundary="%s"`, mixedBoundary))

	var buf bytes.Buffer
	for k, vs := range headers {
		for _, v := range vs {
			buf.WriteString(k)
			buf.WriteString(": ")
			buf.WriteString(v)
			buf.WriteString("\r\n")
		}
	}
	buf.WriteString("\r\n")

	fmt.Fprintf(&buf, "--%s\r\n", mixedBoundary)
	fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", altBoundary)

	fmt.Fprintf(&buf, "--%s\r\n", altBoundary)
	buf.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	buf.WriteString(text)
	buf.WriteString("\r\n")

	fmt.Fprintf(&buf, "--%s\r\n", altBoundary)
	buf.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	buf.WriteString(html)
	buf.WriteString("\r\n")

	fmt.Fprintf(&buf, "--%s--\r\n\r\n", altBoundary)

	fmt.Fprintf(&buf, "--%s\r\n", mixedBoundary)
	buf.WriteString("Content-Type: text/calendar; charset=UTF-8; method=PUBLISH; name=\"" + icsName + "\"\r\n")
	buf.WriteString("Content-Disposition: attachment; filename=\"" + icsName + "\"\r\n")
	buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	buf.WriteString(icsBody)
	buf.WriteString("\r\n")

	fmt.Fprintf(&buf, "--%s--\r\n", mixedBoundary)

	return buf.Bytes(), nil
}

// mimeBoundary returns a unique boundary suitable for MIME multipart
// separators. Base64 of 9 random bytes (12 chars, URL-safe) keeps
// the body bytes from accidentally matching the boundary.
func mimeBoundary(label string) string {
	var b [9]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("unipool_%s_%s", label, base64.RawURLEncoding.EncodeToString(b[:]))
}

// icsEscape applies RFC5545 §3.3.11 text-escaping: commas, semicolons,
// backslashes get a leading backslash; newlines become literal \n.
func icsEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// htmlEscape applies minimal entity escaping for values placed in template text.
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// safeFirst returns the first non-empty string from the args.
func safeFirst(opts ...string) string {
	for _, o := range opts {
		if strings.TrimSpace(o) != "" {
			return o
		}
	}
	return ""
}

// Template slots (in order): wordmark URL, headline, subhead,
// start location, end location, time line, fare, host name,
// deep link.
const tripTodayEmailTemplate = `<!doctype html>
<html lang="en" xmlns="http://www.w3.org/1999/xhtml">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta http-equiv="X-UA-Compatible" content="IE=edge">
  <meta name="x-apple-disable-message-reformatting">
  <title>Your UniPool ride is tomorrow</title>
</head>
<body style="margin:0;padding:0;background:#B5D750;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#263B33;-webkit-text-size-adjust:100%%;ms-text-size-adjust:100%%;">
  <div style="display:none;font-size:1px;line-height:1px;max-height:0;max-width:0;opacity:0;overflow:hidden;mso-hide:all;">
    Your UniPool ride is tomorrow. Add it to your calendar.
  </div>
  <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%%" style="background:#B5D750;padding:48px 20px 32px;">
    <tr>
      <td align="center">
        <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="440" style="max-width:440px;width:100%%;">
          <tr>
            <td align="center" style="padding:0 0 32px;">
              <img src="%s" alt="UniPool" width="152" style="display:block;margin:0 auto;border:0;height:auto;max-width:70%%;">
            </td>
          </tr>
          <tr>
            <td style="background:#FFFDF4;border-radius:20px;padding:32px 28px;box-shadow:0 4px 24px rgba(38,59,51,0.1);">
              <h1 style="margin:0 0 12px;font-size:24px;font-weight:800;color:#263B33;letter-spacing:-0.5px;line-height:30px;text-align:center;">%s</h1>
              <p style="margin:0 0 28px;font-size:15px;line-height:23px;color:rgba(38,59,51,0.7);text-align:center;">
                %s
              </p>

              <!-- Route block: filled dot, dashed connector, ring dot.
                   Mirrors the in-app ride card so the email feels like
                   the same UniPool object the user will see in the
                   app tomorrow. -->
              <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%%" style="margin:0 0 22px;background:rgba(38,59,51,0.04);border-radius:16px;">
                <tr>
                  <td style="padding:18px 18px 16px;">
                    <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%%">
                      <tr>
                        <td valign="top" width="20" style="padding:6px 0 0;">
                          <div style="width:10px;height:10px;background:#263B33;border-radius:999px;margin:0 0 4px;"></div>
                          <div style="width:2px;height:18px;background:rgba(38,59,51,0.25);margin:0 0 4px 4px;"></div>
                          <div style="width:10px;height:10px;border:2px solid #263B33;border-radius:999px;box-sizing:border-box;"></div>
                        </td>
                        <td valign="top" style="padding:0 0 0 10px;">
                          <div style="font-size:13px;font-weight:700;color:rgba(38,59,51,0.65);line-height:18px;margin:0 0 14px;">%s</div>
                          <div style="font-size:16px;font-weight:800;color:#263B33;line-height:20px;letter-spacing:-0.3px;">%s</div>
                        </td>
                      </tr>
                    </table>
                  </td>
                </tr>
              </table>

              <!-- Meta row -->
              <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%%" style="margin:0 0 22px;">
                <tr>
                  <td style="font-size:11px;font-weight:700;letter-spacing:0.5px;text-transform:uppercase;color:rgba(38,59,51,0.45);padding:0 0 4px;">When</td>
                  <td align="right" style="font-size:11px;font-weight:700;letter-spacing:0.5px;text-transform:uppercase;color:rgba(38,59,51,0.45);padding:0 0 4px;">Per seat</td>
                </tr>
                <tr>
                  <td style="font-size:15px;font-weight:800;color:#263B33;letter-spacing:-0.2px;padding:0 0 10px;">%s</td>
                  <td align="right" style="font-size:16px;font-weight:800;color:#263B33;letter-spacing:-0.2px;padding:0 0 10px;">&#8377;%d</td>
                </tr>
                <tr>
                  <td colspan="2" style="border-top:1px solid rgba(38,59,51,0.08);padding:10px 0 0;font-size:13px;color:rgba(38,59,51,0.65);">
                    <span style="font-weight:700;color:#263B33;">Host:</span>&nbsp;%s
                  </td>
                </tr>
              </table>

              <!-- Open in UniPool CTA -->
              <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%%" style="margin:0 0 14px;">
                <tr>
                  <td align="center">
                    <a href="%s" target="_blank" style="display:inline-block;width:100%%;max-width:280px;background:#263B33;color:#B5D750;font-size:15px;font-weight:800;line-height:50px;text-decoration:none;border-radius:14px;text-align:center;">
                      Open in UniPool
                    </a>
                  </td>
                </tr>
              </table>

              <p style="margin:0;font-size:12px;line-height:18px;color:rgba(38,59,51,0.55);text-align:center;">
                Calendar invite attached. Tap to add it to your calendar.
              </p>
            </td>
          </tr>
          <tr>
            <td style="padding:28px 12px 8px;text-align:center;">
              <p style="margin:0;font-size:13px;font-weight:700;color:rgba(38,59,51,0.65);letter-spacing:0.2px;">
                Sent with <span style="color:#263B33;">&#9829;</span> by ACM-VIT
              </p>
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>`
