package helpers

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ses"
)

// SES sender configuration for UniPool verification email.
const (
	sesVerifySender = "UniPool <unipool@acmvit.in>"
	awsRegion       = "ap-south-1"

	// Public wordmark asset used by email templates.
	emailWordmarkURL = "https://examcookerdevsi.blob.core.windows.net/exam-assets/unipool-email/wordmark.png?se=2027-05-20T00%3A00Z&sp=r&spr=https&sv=2026-02-06&sr=b&sig=7XpecwI5sxlV2UQxtHfoH%2FrS6c8w3TYs%2Fyms%2F7ZvSeY%3D"
)

var (
	sesOnce   sync.Once
	sesClient *ses.SES
	sesErr    error
)

// ensureSES initializes the SES client lazily so email availability does not
// control service startup.
//
// Credential precedence:
//
//  1. AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY [+ AWS_SESSION_TOKEN]
//     from the environment / .env.
//  2. `aws configure export-credentials --format process` shell-out.
//     Works without static creds when `aws sso login` is fresh.
//  3. SDK default chain (instance profile, web identity, etc.).
func ensureSES() error {
	sesOnce.Do(func() {
		region := os.Getenv("AWS_REGION")
		if region == "" {
			region = awsRegion
		}
		cfg := aws.Config{Region: aws.String(region)}
		if envCreds, ok := loadEnvCreds(); ok {
			cfg.Credentials = credentials.NewStaticCredentials(
				envCreds.AccessKeyID, envCreds.SecretAccessKey, envCreds.SessionToken,
			)
		} else if exported, err := loadAWSCLICreds(); err == nil {
			cfg.Credentials = credentials.NewStaticCredentials(
				exported.AccessKeyID, exported.SecretAccessKey, exported.SessionToken,
			)
		}
		sess, err := session.NewSessionWithOptions(session.Options{
			SharedConfigState: session.SharedConfigEnable,
			Config:            cfg,
		})
		if err != nil {
			sesErr = err
			return
		}
		sesClient = ses.New(sess)
	})
	return sesErr
}

// loadEnvCreds pulls static credentials out of the process environment.
// SessionToken is optional for permanent IAM-user keys.
func loadEnvCreds() (*awsCLICreds, bool) {
	ak := os.Getenv("AWS_ACCESS_KEY_ID")
	sk := os.Getenv("AWS_SECRET_ACCESS_KEY")
	if ak == "" || sk == "" {
		return nil, false
	}
	return &awsCLICreds{
		AccessKeyID:     ak,
		SecretAccessKey: sk,
		SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
	}, true
}

type awsCLICreds struct {
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
}

func loadAWSCLICreds() (*awsCLICreds, error) {
	out, err := exec.Command("aws", "configure", "export-credentials", "--format", "process").Output()
	if err != nil {
		return nil, err
	}
	var c awsCLICreds
	if err := json.Unmarshal(out, &c); err != nil {
		return nil, err
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return nil, fmt.Errorf("incomplete creds from aws CLI")
	}
	return &c, nil
}

// SendVerificationCode emails a magic link and fallback numeric code.
//
// `magicLink` should be the fully-formed deeplink (`unipool://verify?t=<token>`)
// so the email body just renders it verbatim into the CTA's href.
func SendVerificationCode(toEmail, code, magicLink string) error {
	if err := ensureSES(); err != nil {
		return fmt.Errorf("ses init: %w", err)
	}

	subject := "Verify your email on UniPool"
	textBody := fmt.Sprintf(
		"UniPool\n\n"+
			"We need to confirm %s is yours.\n\n"+
			"Verify in the app:\n%s\n\n"+
			"Or enter this code: %s\n\n"+
			"Expires in 10 minutes. Didn't request this? Ignore this email.",
		toEmail, magicLink, formatCodeSpaced(code),
	)
	htmlBody := fmt.Sprintf(
		emailTemplate,
		emailWordmarkURL, toEmail, magicLink, magicLink, buildOTPDigitsHTML(code),
	)

	_, err := sesClient.SendEmail(&ses.SendEmailInput{
		Source: aws.String(sesVerifySender),
		Destination: &ses.Destination{
			ToAddresses: []*string{aws.String(toEmail)},
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
		log.Printf("SES SendEmail to %s failed: %v", toEmail, err)
		return err
	}
	return nil
}

// formatCodeSpaced inserts a space after the third digit for plain-text
// readability (e.g. "482 193").
func formatCodeSpaced(code string) string {
	code = strings.TrimSpace(code)
	if len(code) == 6 {
		return code[:3] + " " + code[3:]
	}
	return code
}

// buildOTPDigitsHTML renders the code as six boxed digits. Table-based
// layout survives Gmail/Outlook better than letter-spacing on one span.
func buildOTPDigitsHTML(code string) string {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return fmt.Sprintf(
			`<table role="presentation" cellpadding="0" cellspacing="0" border="0" align="center"><tr><td style="padding:16px 20px;background:#FFFFFF;border:1.5px solid rgba(38,59,51,0.12);border-radius:14px;font-family:'SF Mono',SFMono-Regular,Consolas,'Liberation Mono',Menlo,monospace;font-size:28px;font-weight:700;letter-spacing:0.35em;color:#263B33;">%s</td></tr></table>`,
			code,
		)
	}
	var b strings.Builder
	b.WriteString(`<table role="presentation" cellpadding="0" cellspacing="0" border="0" align="center" style="margin:0 auto;"><tr>`)
	for i, ch := range code {
		pad := "padding:0 3px;"
		if i == 0 {
			pad = "padding:0 3px 0 0;"
		} else if i == 5 {
			pad = "padding:0 0 0 3px;"
		}
		b.WriteString(fmt.Sprintf(
			`<td style="%s"><div style="width:42px;height:50px;line-height:50px;text-align:center;background:#FFFFFF;border:1.5px solid rgba(38,59,51,0.12);border-radius:12px;font-family:'SF Mono',SFMono-Regular,Consolas,'Liberation Mono',Menlo,monospace;font-size:26px;font-weight:700;color:#263B33;">%c</div></td>`,
			pad, ch,
		))
	}
	b.WriteString(`</tr></table>`)
	return b.String()
}

// Email template slots: wordmark URL, recipient email, magic link twice, OTP HTML.
const emailTemplate = `<!doctype html>
<html lang="en" xmlns="http://www.w3.org/1999/xhtml">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta http-equiv="X-UA-Compatible" content="IE=edge">
  <meta name="x-apple-disable-message-reformatting">
  <title>Verify your email on UniPool</title>
</head>
<body style="margin:0;padding:0;background:#B5D750;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#263B33;-webkit-text-size-adjust:100%%;ms-text-size-adjust:100%%;">
  <div style="display:none;font-size:1px;line-height:1px;max-height:0;max-width:0;opacity:0;overflow:hidden;mso-hide:all;">
    Your UniPool verification code. Tap the button or enter the code in the app.
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
              <h1 style="margin:0 0 12px;font-size:22px;font-weight:800;color:#263B33;letter-spacing:-0.4px;line-height:28px;text-align:center;">Verify your email</h1>
              <p style="margin:0 0 28px;font-size:15px;line-height:23px;color:rgba(38,59,51,0.7);text-align:center;">
                Sent to <strong style="color:#263B33;">%s</strong>. Tap below or enter the code in UniPool.
              </p>
              <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%%" style="margin:0 0 20px;">
                <tr>
                  <td align="center">
                    <!--[if mso]>
                    <v:roundrect xmlns:v="urn:schemas-microsoft-com:vml" xmlns:w="urn:schemas-microsoft-com:office:word" href="%s" style="height:50px;v-text-anchor:middle;width:240px;" arcsize="28%%" strokecolor="#263B33" fillcolor="#263B33">
                      <w:anchorlock/>
                      <center style="color:#B5D750;font-family:sans-serif;font-size:15px;font-weight:bold;">Verify in app</center>
                    </v:roundrect>
                    <![endif]-->
                    <!--[if !mso]><!-->
                    <a href="%s" target="_blank" style="display:inline-block;width:100%%;max-width:280px;background:#263B33;color:#B5D750;font-size:15px;font-weight:800;line-height:50px;text-decoration:none;border-radius:14px;text-align:center;">
                      Verify in app
                    </a>
                    <!--<![endif]-->
                  </td>
                </tr>
              </table>
              <p style="margin:0 0 24px;font-size:13px;line-height:18px;color:rgba(38,59,51,0.5);text-align:center;">
                Open on your phone.
              </p>
              <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%%" style="margin:0 0 20px;">
                <tr>
                  <td style="border-top:1px solid rgba(38,59,51,0.1);width:40%%;">&nbsp;</td>
                  <td style="padding:0 10px;font-size:12px;font-weight:600;color:rgba(38,59,51,0.4);text-align:center;white-space:nowrap;">or use code</td>
                  <td style="border-top:1px solid rgba(38,59,51,0.1);width:40%%;">&nbsp;</td>
                </tr>
              </table>
              <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%%" style="margin:0 0 20px;">
                <tr>
                  <td align="center">
                    %s
                  </td>
                </tr>
              </table>
              <p style="margin:0;font-size:13px;line-height:18px;color:rgba(38,59,51,0.5);text-align:center;">
                Expires in 10 minutes.
              </p>
            </td>
          </tr>
          <tr>
            <td style="padding:28px 12px 8px;text-align:center;">
              <p style="margin:0 0 10px;font-size:12px;line-height:18px;color:rgba(38,59,51,0.55);">
                Didn't request this? Ignore this email.
              </p>
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
