# Build stage
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o macr-router main.go

# Production stage
FROM alpine:3.20
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/macr-router .
EXPOSE 8080
USER 1000:1000
ENTRYPOINT ["/app/macr-router"]
