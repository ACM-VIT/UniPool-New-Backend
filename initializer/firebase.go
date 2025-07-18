package initializer

import (
	"context"
	"log"
	"os"

	firebase "firebase.google.com/go/v4"
	"google.golang.org/api/option"
	"github.com/joho/godotenv"
)

var FirebaseApp *firebase.App

func InitFirebase() {
	err := godotenv.Load(".env")
	if err != nil {
		log.Println("No .env file found, using environment variables")
	}

	credentialsVal := os.Getenv("SERVICE_CREDS")
	if credentialsVal == "" {
		log.Fatal("SERVICE_CREDS environment variable is required")
	}

	opt := option.WithCredentialsJSON([]byte(credentialsVal))
	app, err := firebase.NewApp(context.Background(), nil, opt)
	if err != nil {
		log.Fatalf("error initializing Firebase app: %v\n", err)
	}

	FirebaseApp = app
}
