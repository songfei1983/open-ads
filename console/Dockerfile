# Build stage for the console UI server (dependency-free static server)
FROM golang:1.27-alpine AS builder

WORKDIR /src

COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /console .

# Runtime stage: binaries are static and the console UI files are baked in;
# development mounts override the baked copy for live editing.
FROM alpine:3.18

COPY --from=builder /console /console
COPY web /app/web

ENV CONSOLE_WEB_DIR=/app/web
EXPOSE 8083
WORKDIR /app
ENTRYPOINT ["/console"]
