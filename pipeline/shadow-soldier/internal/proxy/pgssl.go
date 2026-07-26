package proxy

import (
	"bytes"
	"encoding/binary"
	"io"
)

// Postgres startup packets that request an encrypted transport. Both are
// 8 bytes: int32 length (always 8) followed by an int32 request code.
const (
	sslRequestCode    = 80877103
	gssEncRequestCode = 80877104

	startupProbeBytes = 8
)

// negotiatePostgresPlaintext answers the opportunistic-encryption handshake a
// PostgreSQL driver performs before it sends anything else.
//
// The driver's very first act is an 8-byte SSLRequest. A server that supports TLS
// replies 'S'; one that does not replies 'N', and the driver continues in plain
// text on the same socket. Shadow dependencies are provisioned plaintext by
// Monarch, so shadow-soldier answers 'N' itself and never forwards the probe —
// the real Postgres is left believing the client simply started with a plain
// StartupMessage, which is exactly what it then receives.
//
// Answering here rather than relaying is what makes the proxy transparent: if the
// probe were forwarded, the upstream's own 'N' would have to be relayed back
// before the connection could be piped, and the two sockets would be a handshake
// out of step.
//
// The returned reader is the client stream to forward upstream. When the client
// did not ask for encryption its first 8 bytes are not consumed — they are the
// head of the StartupMessage and are pushed back in front of the connection.
func negotiatePostgresPlaintext(rw io.ReadWriter) (io.Reader, error) {
	var head [startupProbeBytes]byte
	if _, err := io.ReadFull(rw, head[:]); err != nil {
		return nil, err
	}
	switch binary.BigEndian.Uint32(head[4:8]) {
	case sslRequestCode, gssEncRequestCode:
		if _, err := rw.Write([]byte{'N'}); err != nil {
			return nil, err
		}
		// Probe consumed: nothing of it goes upstream.
		return rw, nil
	default:
		return io.MultiReader(bytes.NewReader(head[:]), rw), nil
	}
}
