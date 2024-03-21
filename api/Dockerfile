FROM golang:1.22-alpine

ADD . /workspace
WORKDIR /workspace
RUN go mod download
RUN GOOS=linux GOARCH=amd64 go build -o api .
RUN mv /workspace/api /usr/local/bin

CMD ["api"]