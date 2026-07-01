package helpers

// "Wants to UniPool with you" invite email.
//
// Sent when a UniPool user taps Contact on an external ride (a ride we found
// on another platform, linked to the host by their email). It reuses the same
// SES sender + brand template as the verification and trip-today emails so the
// invite lands looking like a first-class UniPool message.

import (
	"fmt"
	"log"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ses"
)

// InviteEmailParams is the data envelope for the invite email.
type InviteEmailParams struct {
	ToEmail       string // external host's email (the address their rides are linked by)
	InviterName   string // the UniPool user reaching out
	InviterFirst  string
	StartLocation string
	EndLocation   string
	JoinURL       string // where "Join UniPool" points (download / app link)
}

// SendUnipoolInvite composes and sends the invite over SES.
//
// Delivery is best-effort; a send failure is logged and returned but never
// blocks the caller's request path.
func SendUnipoolInvite(p InviteEmailParams) error {
	if err := ensureSES(); err != nil {
		return fmt.Errorf("ses init: %w", err)
	}

	inviterFirst := safeFirst(p.InviterFirst, FirstNameOf(p.InviterName), "A student")
	joinURL := safeFirst(p.JoinURL, "https://unipool.in/download")
	start := safeFirst(p.StartLocation, "their pickup")
	end := safeFirst(p.EndLocation, "their destination")

	subject := fmt.Sprintf("%s wants to ride with you", inviterFirst)

	textBody := fmt.Sprintf(
		"UniPool\n\n"+
			"%s wants to ride with you.\n\n"+
			"They found your ride from %s to %s on UniPool and would like to come along.\n\n"+
			"Join UniPool to say yes: %s\n\n"+
			"Sign up with this email (%s) and your ride is already here.\n\n"+
			"Either way, we hope we help you find someone to ride with.\n\n"+
			"Sent with love by ACM-VIT",
		inviterFirst, start, end, joinURL, p.ToEmail,
	)

	htmlBody := fmt.Sprintf(
		unipoolInviteEmailTemplate,
		emailWordmarkURL,         // logo
		htmlEscape(inviterFirst), // headline name
		htmlEscape(start),        // route start
		htmlEscape(end),          // route end
		joinURL,                  // CTA href (mso)
		joinURL,                  // CTA href (non-mso)
		htmlEscape(p.ToEmail),    // same-email line
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
		log.Printf("SES SendEmail (invite) to %s failed: %v", p.ToEmail, err)
		return err
	}
	return nil
}

// FirstNameOf returns the leading token of a display name.
func FirstNameOf(name string) string {
	fields := strings.Fields(strings.TrimSpace(name))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// Template slots (in order): wordmark URL, headline name, route start,
// route end, join URL (mso), join URL (non-mso), recipient email.
const unipoolInviteEmailTemplate = `<!doctype html>
<html lang="en" xmlns="http://www.w3.org/1999/xhtml">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta http-equiv="X-UA-Compatible" content="IE=edge">
  <meta name="x-apple-disable-message-reformatting">
  <title>Someone wants to ride with you</title>
</head>
<body style="margin:0;padding:0;background:#B5D750;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#263B33;-webkit-text-size-adjust:100%%;ms-text-size-adjust:100%%;">
  <div style="display:none;font-size:1px;line-height:1px;max-height:0;max-width:0;opacity:0;overflow:hidden;mso-hide:all;">
    A student found your ride on UniPool and wants to come along.
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
              <h1 style="margin:0 0 12px;font-size:23px;font-weight:800;color:#263B33;letter-spacing:-0.4px;line-height:29px;text-align:center;">%s wants to ride with you</h1>
              <p style="margin:0 0 24px;font-size:15px;line-height:23px;color:rgba(38,59,51,0.7);text-align:center;">
                They found your ride on UniPool and would like to come along.
              </p>

              <!-- Route block: filled dot, dashed connector, ring dot. Same
                   shape as the in-app ride card. -->
              <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%%" style="margin:0 0 24px;background:rgba(38,59,51,0.04);border-radius:16px;">
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
                          <div style="font-size:16px;font-weight:800;color:#263B33;line-height:20px;letter-spacing:-0.3px;margin:0 0 14px;">%s</div>
                          <div style="font-size:16px;font-weight:800;color:#263B33;line-height:20px;letter-spacing:-0.3px;">%s</div>
                        </td>
                      </tr>
                    </table>
                  </td>
                </tr>
              </table>

              <!-- Join CTA -->
              <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%%" style="margin:0 0 18px;">
                <tr>
                  <td align="center">
                    <!--[if mso]>
                    <v:roundrect xmlns:v="urn:schemas-microsoft-com:vml" xmlns:w="urn:schemas-microsoft-com:office:word" href="%s" style="height:50px;v-text-anchor:middle;width:240px;" arcsize="28%%" strokecolor="#263B33" fillcolor="#263B33">
                      <w:anchorlock/>
                      <center style="color:#B5D750;font-family:sans-serif;font-size:15px;font-weight:bold;">Join UniPool to say yes</center>
                    </v:roundrect>
                    <![endif]-->
                    <!--[if !mso]><!-->
                    <a href="%s" target="_blank" style="display:inline-block;width:100%%;max-width:280px;background:#263B33;color:#B5D750;font-size:15px;font-weight:800;line-height:50px;text-decoration:none;border-radius:14px;text-align:center;">
                      Join UniPool to say yes
                    </a>
                    <!--<![endif]-->
                  </td>
                </tr>
              </table>

              <p style="margin:0 0 6px;font-size:13px;line-height:20px;color:rgba(38,59,51,0.6);text-align:center;">
                Sign up with this email (<strong style="color:#263B33;">%s</strong>) and your ride is already here.
              </p>
              <p style="margin:0;font-size:13px;line-height:20px;color:rgba(38,59,51,0.5);text-align:center;">
                Either way, we hope we help you find someone to ride with.
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
