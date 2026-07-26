package parsers

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// redisParser decodes RESP commands.
//
// RESP is hand-decoded rather than pulled from a library: a client command is
// always either an array of bulk strings or a space-separated inline line, and
// both fit in the few dozen lines below.
//
// Note the protocol-level limitation this parser cannot fix: RESP has no comment
// or metadata field, so there is nowhere for a W3C traceparent to ride. Redis
// commands are therefore reported untraced and dropped by the reporter unless the
// application happens to embed a traceparent in a key or argument value.
type redisParser struct{}

func (p *redisParser) Run(r io.Reader, emit func(QueryReport)) error {
	br := bufio.NewReader(r)
	for {
		args, err := readRESPCommand(br)
		if err != nil {
			return err
		}
		if len(args) == 0 {
			continue
		}
		emit(redisReport(args))
	}
}

func redisReport(args []string) QueryReport {
	var target string
	if len(args) > 1 {
		target = args[1]
	}
	raw := strings.Join(args, " ")
	return QueryReport{
		TraceID:   TraceIDFrom([]byte(raw)),
		Protocol:  ProtocolRedis,
		Operation: strings.ToLower(args[0]),
		Target:    target,
		RawQuery:  raw,
		Params:    args[1:],
	}
}

func readRESPCommand(br *bufio.Reader) ([]string, error) {
	prefix, err := br.Peek(1)
	if err != nil {
		return nil, err
	}
	if prefix[0] != '*' {
		// Inline command, e.g. "PING\r\n" — telnet-style clients and some health
		// checks use this form.
		line, err := readRESPLine(br)
		if err != nil {
			return nil, err
		}
		return strings.Fields(line), nil
	}

	line, err := readRESPLine(br)
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(line[1:])
	if err != nil {
		return nil, fmt.Errorf("resp: bad array header %q: %w", line, err)
	}
	if n <= 0 {
		return nil, nil
	}
	if n > MaxFrameBytes {
		return nil, ErrFrameTooLarge
	}

	args := make([]string, 0, n)
	budget := MaxFrameBytes
	for range n {
		head, err := readRESPLine(br)
		if err != nil {
			return nil, err
		}
		if len(head) == 0 || head[0] != '$' {
			return nil, fmt.Errorf("resp: expected bulk string, got %q", head)
		}
		size, err := strconv.Atoi(head[1:])
		if err != nil {
			return nil, fmt.Errorf("resp: bad bulk header %q: %w", head, err)
		}
		if size < 0 {
			args = append(args, "")
			continue
		}
		if size > budget {
			// Drain the oversized value so framing survives, then abandon the
			// command rather than hold megabytes of it in memory.
			if _, err := io.CopyN(io.Discard, br, int64(size)+2); err != nil {
				return nil, err
			}
			return nil, ErrFrameTooLarge
		}
		buf := make([]byte, size+2) // value + CRLF
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil, err
		}
		budget -= size
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

func readRESPLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
