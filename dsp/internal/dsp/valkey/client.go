// Package valkey contains the small RESP client used by the DSP config plane.
package valkey

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	Address  string
	Username string
	Password string
	TLS      bool
	Timeout  time.Duration
}

func (c *Client) Do(ctx context.Context, args ...string) (any, error) {
	if len(args) == 0 || c.Address == "" {
		return nil, errors.New("valkey address and command are required")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	var conn net.Conn
	var err error
	if c.TLS {
		host, _, splitErr := net.SplitHostPort(c.Address)
		if splitErr != nil {
			return nil, fmt.Errorf("split valkey address: %w", splitErr)
		}
		tlsDialer := tls.Dialer{NetDialer: &dialer, Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}}
		conn, err = tlsDialer.DialContext(ctx, "tcp", c.Address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", c.Address)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to valkey: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if requestDeadline, ok := ctx.Deadline(); ok && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set valkey deadline: %w", err)
	}
	reader := bufio.NewReader(conn)
	if c.Password != "" || c.Username != "" {
		auth := []string{"AUTH", c.Username, c.Password}
		if c.Username == "" {
			auth = []string{"AUTH", c.Password}
		}
		if err := writeCommand(conn, auth); err != nil {
			return nil, err
		}
		if _, err := readValue(reader); err != nil {
			return nil, fmt.Errorf("authenticate to valkey: %w", err)
		}
	}
	if err := writeCommand(conn, args); err != nil {
		return nil, err
	}
	return readValue(reader)
}

func writeCommand(w io.Writer, args []string) error {
	var b strings.Builder
	b.WriteByte('*')
	b.WriteString(strconv.Itoa(len(args)))
	b.WriteString("\r\n")
	for _, arg := range args {
		b.WriteByte('$')
		b.WriteString(strconv.Itoa(len(arg)))
		b.WriteString("\r\n")
		b.WriteString(arg)
		b.WriteString("\r\n")
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write valkey command: %w", err)
	}
	return nil
}

func readValue(r *bufio.Reader) (any, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("read valkey response: %w", err)
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read valkey response line: %w", err)
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	switch prefix {
	case '+':
		return line, nil
	case '-':
		return nil, errors.New(line)
	case ':':
		return strconv.ParseInt(line, 10, 64)
	case '$':
		size, err := strconv.Atoi(line)
		if err != nil {
			return nil, fmt.Errorf("invalid valkey bulk length: %w", err)
		}
		if size < 0 {
			return nil, nil
		}
		if size > 64<<20 {
			return nil, errors.New("valkey response exceeds size limit")
		}
		data := make([]byte, size+2)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, fmt.Errorf("read valkey bulk response: %w", err)
		}
		return string(data[:size]), nil
	case '*':
		size, err := strconv.Atoi(line)
		if err != nil || size < 0 || size > 1<<16 {
			return nil, errors.New("invalid valkey array length")
		}
		values := make([]any, size)
		for i := range values {
			values[i], err = readValue(r)
			if err != nil {
				return nil, err
			}
		}
		return values, nil
	default:
		return nil, fmt.Errorf("unsupported valkey response prefix %q", prefix)
	}
}
