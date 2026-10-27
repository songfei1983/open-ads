# Build stage for DMP
FROM golang:1.27-alpine AS builder

WORKDIR /src

# The shared identity policy is a sibling module (go.mod replace ../identity), so
# the build context is the repository root.
COPY dmp/go.mod dmp/go.sum ./
RUN go mod download

COPY dmp/ ./
COPY identity/ /identity/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /dmp ./cmd/dmp/main.go

FROM alpine:3.18

COPY --from=builder /dmp /dmp

EXPOSE 8082
ENTRYPOINT ["/dmp"]

# Dev stage: live source mounts with air hot reload (compose target: dev).
FROM golang:1.27-alpine AS dev
RUN go install github.com/air-verse/air@v1.52.3
WORKDIR /src
EXPOSE 8082
ENTRYPOINT ["air"]