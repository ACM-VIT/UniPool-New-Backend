FROM golang:1.21-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git

COPY go.mod go.sum ./

RUN go mod download && go mod verify

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o /go-app .

FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /go-app .

ENV DB_URL=""
ENV SERVICE_CREDS=""
ENV SHOULD_MIGRATE="FALSE"

EXPOSE $PORT

CMD ["./go-app"]
