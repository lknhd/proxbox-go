FROM golang:1.25-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o proxbox-go .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata openssh-client

WORKDIR /app
COPY --from=builder /build/proxbox-go .
COPY public/ ./public/

EXPOSE 2222

ENTRYPOINT ["/app/proxbox-go"]
