package helpers

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ses"
)

// SES configuration. The AWS account this maps to has verified the
// `acmvit.in` domain. `unipool@acmvit.in` is the product-facing
// identity; replies hit a real-ish inbox instead of vanishing.
const (
	sesVerifySender = "UniPool <unipool@acmvit.in>"
	awsRegion       = "ap-south-1"

	// Brand assets. Hosted on Azure blob with a year-long SAS so
	// the email keeps rendering across iterations of the template.
	// If these expire, refresh via:
	//   az storage blob generate-sas --account-name examcookerdevsi …
	emailLogoURL = "https://examcookerdevsi.blob.core.windows.net/exam-assets/unipool-email/logo.png?se=2027-05-19T21%3A49Z&sp=r&sv=2026-02-06&sr=b&sig=Tpz2VtkyBak9aGWN1zFFlZLAC2d%2BFlG7kPqimdGTC5Q%3D"
	emailIllustrationURL = "https://examcookerdevsi.blob.core.windows.net/exam-assets/unipool-email/illustration.png?se=2027-05-19T21%3A49Z&sp=r&sv=2026-02-06&sr=b&sig=DlIM%2F5DrcoDjPBsKyG8UTXJsitAZc5VDUf2Y89a%2BkWk%3D"
)

var (
	sesOnce   sync.Once
	sesClient *ses.SES
	sesErr    error
)

// ensureSES does a lazy session bring-up the first time we need to
// send. Doing it in main() would couple the whole backend's startup
// to AWS being reachable; emails are best-effort and we'd rather
// fail one call than crash boot.
//
// Credential precedence:
//
//   1. AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY [+ AWS_SESSION_TOKEN]
//      from the environment / .env. Cheapest, no subprocess.
//   2. `aws configure export-credentials --format process` shell-out.
//      Works without static creds when `aws sso login` is fresh.
//   3. SDK default chain (instance profile, web identity, etc.).
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

// loadEnvCreds pulls static creds out of the process environment.
// Returns (_, false) if AccessKeyID is unset — SessionToken is
// optional for permanent IAM-user keys.
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

// SendVerificationCode emails the user a one-tap magic link and the
// matching numeric code as a fallback. Either path lands on the same
// row in `email_verifications` — confirm by token or by code.
//
// `magicLink` should be the fully-formed deeplink (`unipool://verify?t=<token>`)
// so the email body just renders it verbatim into the CTA's href.
func SendVerificationCode(toEmail, code, magicLink string) error {
	if err := ensureSES(); err != nil {
		return fmt.Errorf("ses init: %w", err)
	}

	subject := "Verify your academic status on UniPool"
	textBody := fmt.Sprintf(
		"Verify your UniPool academic status.\n\nTap to verify: %s\n\nOr enter this code in the app: %s\n\nThis link and code expire in 10 minutes. If you didn't ask to verify, you can ignore this email.",
		magicLink, code,
	)
	htmlBody := fmt.Sprintf(emailTemplate, emailLogoURL, toEmail, magicLink, code, emailIllustrationURL)

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

// Email template. Five `%s` slots in order:
//
//   1. logo image URL
//   2. recipient email (shown bold inside the body copy)
//   3. magic-link URL (href on the CTA + the raw link below it)
//   4. 6-digit code (rendered in the OTP block)
//   5. illustration image URL
//
// Inline-styled because most email clients (notably Gmail) strip
// <style> blocks. Forest + lime palette mirrors the in-app look.
const emailTemplate = `<!doctype html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Verify your UniPool academic status</title>
</head>
<body style="margin:0;padding:0;background:#F1F4EE;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;color:#263B33;">
  <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%%" style="background:#F1F4EE;padding:36px 16px;">
    <tr>
      <td align="center">
        <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="520" style="max-width:520px;width:100%%;background:#FFFFFF;border-radius:22px;overflow:hidden;box-shadow:0 8px 24px rgba(38,59,51,0.08);">
          <!-- Header band: lime canvas with the brand mark -->
          <tr>
            <td style="background:#B5D750;padding:28px 32px 22px;">
              <table role="presentation" cellpadding="0" cellspacing="0" border="0">
                <tr>
                  <td style="vertical-align:middle;">
                    <img src="%s" alt="UniPool" width="44" height="44" style="display:block;border-radius:11px;border:0;">
                  </td>
                  <td style="vertical-align:middle;padding-left:12px;">
                    <div style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;font-weight:800;font-size:18px;color:#263B33;letter-spacing:-0.3px;">UniPool</div>
                    <div style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;font-weight:600;font-size:12px;color:#263B33;opacity:0.7;letter-spacing:0.2px;">Student carpools</div>
                  </td>
                </tr>
              </table>
            </td>
          </tr>

          <!-- Body -->
          <tr>
            <td style="padding:32px 32px 8px;">
              <h1 style="margin:0 0 8px;font-size:22px;font-weight:800;color:#263B33;letter-spacing:-0.5px;line-height:30px;">Verify your academic status</h1>
              <p style="margin:0 0 22px;font-size:15px;line-height:23px;color:#52786A;">
                Confirm that <b style="color:#263B33;">%s</b> belongs to you and we'll add a verified badge to your UniPool profile.
              </p>

              <!-- Primary CTA: deeplink button. Bg + text both inline-styled
                   so Gmail / Apple Mail / Outlook all render the pill. -->
              <table role="presentation" cellpadding="0" cellspacing="0" border="0" style="margin:0 0 24px;">
                <tr>
                  <td align="center" style="border-radius:14px;background:#263B33;">
                    <a href="%s" target="_blank" style="display:inline-block;padding:14px 28px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;font-size:15px;font-weight:800;letter-spacing:0.3px;color:#B5D750;text-decoration:none;border-radius:14px;">
                      Tap to verify →
                    </a>
                  </td>
                </tr>
              </table>

              <!-- Fallback code. Some email clients (notably Gmail web in
                   strict mode) will strip the unipool:// deeplink; in
                   that case the recipient enters the code manually. -->
              <div style="margin:0 0 8px;font-size:13px;color:#86988F;font-weight:600;letter-spacing:0.3px;text-transform:uppercase;">Or enter this code</div>
              <div style="margin:0 0 24px;padding:18px 16px;background:#EBF1ED;border-radius:14px;text-align:center;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;font-weight:800;font-size:30px;letter-spacing:0.4em;color:#263B33;">
                %s
              </div>

              <p style="margin:0 0 4px;font-size:12.5px;line-height:18px;color:#86988F;">
                The link and code expire in 10 minutes. If you didn't ask to verify, you can ignore this email — nothing changes on your account.
              </p>
            </td>
          </tr>

          <!-- Footer with the brand illustration -->
          <tr>
            <td style="padding:18px 32px 32px;text-align:center;">
              <img src="%s" alt="" width="180" style="display:inline-block;max-width:60%%;border:0;opacity:0.9;">
              <p style="margin:18px 0 0;font-size:12px;line-height:18px;color:#86988F;">
                UniPool · ACM-VIT · Vellore Institute of Technology
              </p>
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>`
