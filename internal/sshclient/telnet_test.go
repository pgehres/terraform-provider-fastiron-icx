package sshclient

import (
	"bytes"
	"io"
	"testing"
)

// scriptedConn is a one-shot io.ReadWriteCloser: Read serves the scripted
// bytes (chunkSize at a time, to exercise state carried across reads) and
// Write records everything sent.
type scriptedConn struct {
	in        *bytes.Reader
	chunkSize int
	out       bytes.Buffer
	closed    bool
}

func newScriptedConn(in []byte, chunkSize int) *scriptedConn {
	return &scriptedConn{in: bytes.NewReader(in), chunkSize: chunkSize}
}

func (s *scriptedConn) Read(p []byte) (int, error) {
	if s.chunkSize > 0 && len(p) > s.chunkSize {
		p = p[:s.chunkSize]
	}
	return s.in.Read(p)
}

func (s *scriptedConn) Write(p []byte) (int, error) { return s.out.Write(p) }
func (s *scriptedConn) Close() error                { s.closed = true; return nil }

// drain reads a telnetConn to EOF and returns the decoded data.
func drain(t *testing.T, tc *telnetConn) []byte {
	t.Helper()
	data, err := io.ReadAll(tc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return data
}

func TestTelnetDecodeRefusesEveryOption(t *testing.T) {
	raw := []byte{
		telnetIAC, telnetDO, 24, // DO TERMINAL-TYPE → WONT
		telnetIAC, telnetWILL, 1, // WILL ECHO → DONT
		telnetIAC, telnetWILL, 3, // WILL SUPPRESS-GO-AHEAD → DONT
		telnetIAC, telnetDONT, 5, // DONT STATUS → no reply (would loop)
		telnetIAC, telnetWONT, 1, // WONT ECHO → no reply
		'o', 'k',
	}
	want := []byte{
		telnetIAC, telnetWONT, 24,
		telnetIAC, telnetDONT, 1,
		telnetIAC, telnetDONT, 3,
	}
	for _, chunk := range []int{0, 1, 2} { // whole, byte-by-byte, odd splits
		conn := newScriptedConn(raw, chunk)
		tc := newTelnetConn(conn)
		if got := drain(t, tc); string(got) != "ok" {
			t.Errorf("chunk %d: data = %q, want %q", chunk, got, "ok")
		}
		if !bytes.Equal(conn.out.Bytes(), want) {
			t.Errorf("chunk %d: replies = % x, want % x", chunk, conn.out.Bytes(), want)
		}
	}
}

func TestTelnetDecodeEscapesAndSubnegotiation(t *testing.T) {
	raw := []byte{
		'a',
		telnetIAC, telnetIAC, // escaped 0xFF data byte
		'b',
		telnetIAC, telnetSB, 24, 1, telnetIAC, telnetIAC, 'x', telnetIAC, telnetSE, // subnegotiation, with an escaped IAC inside
		'c',
		telnetIAC, 241, // NOP: two-byte command, no payload
		'd',
	}
	for _, chunk := range []int{0, 1, 3} {
		conn := newScriptedConn(raw, chunk)
		got := drain(t, newTelnetConn(conn))
		if want := []byte{'a', 0xff, 'b', 'c', 'd'}; !bytes.Equal(got, want) {
			t.Errorf("chunk %d: data = % x, want % x", chunk, got, want)
		}
		if conn.out.Len() != 0 {
			t.Errorf("chunk %d: unexpected replies % x", chunk, conn.out.Bytes())
		}
	}
}

// An io.Reader must not return (0, nil): a read that consumed only
// negotiation keeps going until data or an error arrives.
func TestTelnetReadSkipsPureNegotiation(t *testing.T) {
	raw := []byte{telnetIAC, telnetDO, 24, telnetIAC, telnetWILL, 1, 'p', 'r', 'o', 'm', 'p', 't', '>'}
	tc := newTelnetConn(newScriptedConn(raw, 3))
	buf := make([]byte, 1)
	n, err := tc.Read(buf)
	if err != nil || n != 1 || buf[0] != 'p' {
		t.Fatalf("Read = (%d, %v, %q), want the first data byte", n, err, buf[:n])
	}
}

func TestTelnetWriteNVT(t *testing.T) {
	tests := []struct{ in, want string }{
		{"show version\n", "show version\r\n"},
		{"already\r\nterminated\r\n", "already\r\nterminated\r\n"},
		{"a\xffb", "a\xff\xffb"},
		{"\n\n", "\r\n\r\n"},
	}
	for _, tt := range tests {
		conn := newScriptedConn(nil, 0)
		tc := newTelnetConn(conn)
		n, err := tc.Write([]byte(tt.in))
		if err != nil || n != len(tt.in) {
			t.Errorf("Write(%q) = (%d, %v)", tt.in, n, err)
		}
		if got := conn.out.String(); got != tt.want {
			t.Errorf("Write(%q) sent %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTelnetCloseClosesConn(t *testing.T) {
	conn := newScriptedConn(nil, 0)
	tc := newTelnetConn(conn)
	if err := tc.Close(); err != nil || !conn.closed {
		t.Fatalf("Close = %v, closed = %v", err, conn.closed)
	}
}
