# Build stage
FROM golang:1.27-alpine AS builder

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Install Air for live reload (correct module path)
RUN go install github.com/air-verse/air@latest && \
	chmod +x $(go env GOPATH)/bin/air

# Build the DSP binary (statically linked) – used for fallback if Air not used
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /dsp ./cmd/dsp/main.go

# Runtime stage
FROM alpine:3.18

# Install necessary ca-certificates and bash (optional for Air)
RUN apk add --no-cache ca-certificates && update-ca-certificates

# Ensure the mount point for prototype UI exists (Docker will mount here).
RUN mkdir -p /src/docs

# Copy the built binary (optional)
COPY --from=builder /dsp /dsp

# Copy Air binary
# The Air binary is installed to /go/bin/air in the builder stage.
COPY --from=builder /go/bin/air /usr/local/bin/air
# copy Air config so `air -c .air.toml` can find it
COPY --from=builder /src/.air.toml /src/.air.toml

# Expose the default DSP port (8081)
EXPOSE 8081

# Default command uses the baked binary (air available via `target: dev`)
WORKDIR /src
ENV AIR_DISABLED=1
ENTRYPOINT ["/dsp"]

# Dev stage: live source mounts with air hot reload (compose target: dev).
FROM golang:1.27-alpine AS dev
RUN go install github.com/air-verse/air@v1.52.3
RUN apk add --no-cache ca-certificates
WORKDIR /src
EXPOSE 8081
ENTRYPOINT ["air"]