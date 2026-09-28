FROM golang:1.26-alpine@sha256:8ac98ca534ac3f51e1f420a1dd2c15e74c75cfa0f23f3ad27eb5d7236c349a0c

ADD . /workspace
WORKDIR /workspace
RUN go mod download
RUN GOOS=linux GOARCH=amd64 go build -o api ./cmd/api
RUN mv /workspace/api /usr/local/bin

CMD ["api"]
