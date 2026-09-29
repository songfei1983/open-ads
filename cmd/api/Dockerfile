FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414

ADD . /workspace
WORKDIR /workspace
RUN go mod download
RUN GOOS=linux GOARCH=amd64 go build -o api ./cmd/api
RUN mv /workspace/api /usr/local/bin

CMD ["api"]
