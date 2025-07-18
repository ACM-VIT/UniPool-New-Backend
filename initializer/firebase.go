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
	// err := godotenv.Load(".env")

	// if err != nil {
	// 	log.Fatal("Error loading .env file")
	// }

	credentialsVal := os.Getenv("SERVICE_CREDS")

	opt := option.WithCredentialsJSON([]byte(credentialsVal))
	// log.Println(opt)
	app, err := firebase.NewApp(context.Background(), nil, opt)
	if err != nil {
		log.Fatalf("error initializing Firebase app: %v\n", err)
	}

	FirebaseApp = app
}
