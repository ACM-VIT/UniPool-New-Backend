# Step 1: Build the Go application
FROM golang:1.21-alpine as builder

# Set the working directory inside the container
WORKDIR /app

# Copy the Go module files (go.mod and go.sum) to the container
COPY go.mod go.sum ./

# Download the Go dependencies
RUN go mod tidy

# Copy the rest of the application source code to the container
COPY . .

# Build the Go application
RUN go build -o /go-app .

# Step 2: Set up a minimal image for running the app
FROM alpine:latest

# Install required libraries (e.g., ca-certificates)
RUN apk --no-cache add ca-certificates

# Set the working directory inside the container
WORKDIR /root/

# Copy the Go app from the builder stage
COPY --from=builder /go-app .

# Expose the port your Go app will run on (3000 as per your main function)
EXPOSE 3000

# Run the Go application
CMD ["./go-app"]
