# Build stage for SSP
FROM golang:1.27-alpine AS builder

WORKDIR /src

# Cache module files
COPY go.mod ./
RUN go mod download

# Copy source
COPY . .

# Build SSP binary (static)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /ssp ./cmd/ssp/main.go

# Runtime stage
FROM alpine:3.18

COPY --from=builder /ssp /ssp

EXPOSE 8080
ENTRYPOINT ["/ssp"]

# Dev stage: live source mounts with air hot reload (compose target: dev).
# The baked production binary is NOT used; air watches ./cmd and rebuilds.
FROM golang:1.27-alpine AS dev
RUN go install github.com/air-verse/air@v1.52.3
WORKDIR /src
EXPOSE 8080
ENTRYPOINT ["air"]