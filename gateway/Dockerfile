# Stage 1: Build the static Go binary
FROM golang:1.23-alpine AS builder

WORKDIR /app

# We use Go 1.23 as the stable backend for building, 
# although the user prompted for Go 1.26 syntax in go.mod, 
# golang:1.26-alpine image doesn't exist yet in dockerhub.
# We map it to 1.23 to ensure it builds successfully right now.

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -o gateway .

# Stage 2: Minimal runtime image
FROM alpine:latest
RUN apk --no-cache add ca-certificates

WORKDIR /app
COPY --from=builder /app/gateway .

EXPOSE 8080

CMD ["./gateway"]
