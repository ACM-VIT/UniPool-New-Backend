package helpers

// Booking lifecycle emails.

import (
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ses"
)

type BookingEmailKind string

const (
	BookingEmailRequested BookingEmailKind = "requested"
	BookingEmailAccepted  BookingEmailKind = "accepted"
	BookingEmailRejected  BookingEmailKind = "rejected"
	BookingEmailWithdrawn BookingEmailKind = "withdrawn"
	BookingEmailRemoved   BookingEmailKind = "removed"
)

type BookingEmailParams struct {
	Kind          BookingEmailKind
	ToEmail       string
	ToName        string
	ActorName     string
	StartLocation string
	EndLocation   string
	StartTime     time.Time
	RideID        string
}

func SendBookingEmail(p BookingEmailParams) error {
	if p.ToEmail == "" {
		return nil
	}
	if err := ensureSES(); err != nil {
		return fmt.Errorf("ses init: %w", err)
	}

	subject, headline, subhead, cta := bookingEmailCopy(p)
	route := fmt.Sprintf("%s to %s", safeFirst(p.StartLocation, "Pickup"), safeFirst(p.EndLocation, "Destination"))
	when := p.StartTime.Format("Mon, 2 Jan 15:04")
	rideURL := bookingRideURL(p.RideID)

	textBody := fmt.Sprintf(
		"UniPool\n\n%s\n\n%s\n%s\n\n%s\n\nOpen UniPool: %s\n",
		headline,
		route,
		when,
		subhead,
		rideURL,
	)
	htmlBody := fmt.Sprintf(
		bookingEmailTemplate,
		emailWordmarkURL,
		htmlEscape(headline),
		htmlEscape(subhead),
		htmlEscape(route),
		htmlEscape(when),
		rideURL,
		htmlEscape(cta),
	)

	_, err := sesClient.SendEmail(&ses.SendEmailInput{
		Source: aws.String(sesVerifySender),
		Destination: &ses.Destination{
			ToAddresses: []*string{aws.String(p.ToEmail)},
		},
		Message: &ses.Message{
			Subject: &ses.Content{Data: aws.String(subject), Charset: aws.String("UTF-8")},
			Body: &ses.Body{
				Text: &ses.Content{Data: aws.String(textBody), Charset: aws.String("UTF-8")},
				Html: &ses.Content{Data: aws.String(htmlBody), Charset: aws.String("UTF-8")},
			},
		},
	})
	if err != nil {
		log.Printf("SES SendEmail (booking %s) to %s failed: %v", p.Kind, p.ToEmail, err)
		return err
	}
	return nil
}

func bookingEmailCopy(p BookingEmailParams) (subject, headline, subhead, cta string) {
	actor := safeFirst(FirstNameOf(p.ActorName), p.ActorName, "Someone")
	switch p.Kind {
	case BookingEmailRequested:
		return "New UniPool ride request",
			fmt.Sprintf("%s wants to join your ride", actor),
			"Open UniPool to review the request and message them before you decide.",
			"Review request"
	case BookingEmailAccepted:
		return "Your UniPool request was accepted",
			"Your ride request was accepted",
			"You're in. Open UniPool to check the trip chat and coordinate pickup details.",
			"Open trip chat"
	case BookingEmailRejected:
		return "Your UniPool request was declined",
			"Your ride request was declined",
			"This seat did not work out. You can search again for another ride on the same route.",
			"Find another ride"
	case BookingEmailWithdrawn:
		return "A passenger left your UniPool ride",
			fmt.Sprintf("%s can no longer make it", actor),
			"Their seat is open again. Open UniPool to review your ride and any pending requests.",
			"Open ride"
	case BookingEmailRemoved:
		return "You were removed from a UniPool ride",
			"You were removed from this ride",
			"The host removed your seat. You can search again for another ride on the same route.",
			"Find another ride"
	default:
		return "UniPool booking update",
			"Booking update",
			"Open UniPool to see the latest status for this ride.",
			"Open UniPool"
	}
}

func bookingRideURL(rideID string) string {
	if rideID == "" {
		return "https://unipool.in/app"
	}
	return "https://unipool.in/ride/" + rideID
}

const bookingEmailTemplate = `<!doctype html>
<html lang="en" xmlns="http://www.w3.org/1999/xhtml">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta http-equiv="X-UA-Compatible" content="IE=edge">
  <meta name="x-apple-disable-message-reformatting">
  <title>UniPool booking update</title>
</head>
<body style="margin:0;padding:0;background:#B5D750;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#263B33;-webkit-text-size-adjust:100%%;ms-text-size-adjust:100%%;">
  <div style="display:none;font-size:1px;line-height:1px;max-height:0;max-width:0;opacity:0;overflow:hidden;mso-hide:all;">
    Your UniPool booking has an update.
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
            <td style="background:#F9FFE8;border-radius:28px;padding:28px 24px 24px;box-shadow:0 18px 45px rgba(38,59,51,0.18);">
              <h1 style="margin:0 0 10px;font-size:24px;line-height:30px;font-weight:800;color:#263B33;text-align:center;">%s</h1>
              <p style="margin:0 0 22px;font-size:15px;line-height:22px;color:#53665C;text-align:center;">%s</p>
              <div style="background:#FFFFFF;border:1px solid rgba(38,59,51,0.10);border-radius:18px;padding:16px;margin:0 0 22px;">
                <p style="margin:0 0 6px;font-size:16px;line-height:22px;font-weight:800;color:#263B33;text-align:center;">%s</p>
                <p style="margin:0;font-size:14px;line-height:20px;color:#53665C;text-align:center;">%s</p>
              </div>
              <table role="presentation" cellpadding="0" cellspacing="0" border="0" align="center" style="margin:0 auto;">
                <tr>
                  <td align="center" bgcolor="#263B33" style="border-radius:999px;">
                    <a href="%s" style="display:inline-block;padding:14px 22px;font-size:15px;line-height:18px;font-weight:800;text-decoration:none;color:#F9FFE8;border-radius:999px;">%s</a>
                  </td>
                </tr>
              </table>
            </td>
          </tr>
          <tr>
            <td align="center" style="padding:20px 10px 0;color:#53665C;font-size:12px;line-height:18px;">
              Sent by UniPool.
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>`
