package ingest

// Frame wire format from Siphon (5-byte header + payload).
const (
	DirRequest       = 'R'
	DirResponse      = 'S'
	FrameHeaderSize  = 5
	DefaultMaxFrame  = 5 << 20
)
