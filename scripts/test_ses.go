//go:build ignore

// Smoke test for the SES helper. Run with the env loaded:
//
//	go run scripts/test_ses.go <to-email>
//
// Sends a fake verification code so we can verify the
// unipool@acmvit.in sender + branded template land in an inbox
// without going through the full /verify/start flow.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"

	"unipool-backend/helpers"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("usage: go run scripts/test_ses.go <to-email>")
	}
	to := os.Args[1]
	if err := godotenv.Load(); err != nil {
		log.Fatalf("godotenv: %v", err)
	}
	code := "123456"
	link := "unipool://verify?t=demo-token"
	if err := helpers.SendVerificationCode(to, code, link); err != nil {
		log.Fatalf("send failed: %v", err)
	}
	fmt.Println("sent OK to", to)
}
