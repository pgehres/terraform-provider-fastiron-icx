package sshclient

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Telnet protocol bytes (RFC 854).
const (
	telnetSE   = 240 // end of subnegotiation
	telnetSB   = 250 // start of subnegotiation
	telnetWILL = 251
	telnetWONT = 252
	telnetDO   = 253
	telnetDONT = 254
	telnetIAC  = 255
)

// telnetConn is a minimal telnet NVT layer over a raw connection. It strips
// option negotiation out of the byte stream, refuses every option the switch
// proposes (DO → WONT, WILL → DONT — the same stance as Python's telnetlib,
// which is what the Ansible telnet module that provisions these switches
// uses), skips subnegotiations, unescapes IAC IAC, and on the way out escapes
// 0xFF and sends the NVT line ending CR LF for every "\n" the session layer
// writes. It never requests options itself, and never answers DONT/WONT, so
// negotiation cannot loop. What remains is a plain character terminal that
// the session layer drives exactly like an SSH shell.
type telnetConn struct {
	conn io.ReadWriteCloser

	// wmu serialises negotiation replies written from Read with the
	// commands written by the session layer.
	wmu sync.Mutex

	// Decoder state carried across Read calls, since a negotiation sequence
	// may be split between two TCP segments.
	state telnetState
	cmd   byte // the DO/DONT/WILL/WONT awaiting its option byte
	raw   []byte
}

type telnetState int

const (
	stateData   telnetState = iota // plain characters
	stateIAC                       // saw IAC, next byte is a command
	stateOption                    // saw IAC DO/DONT/WILL/WONT, next byte is the option
	stateSB                        // inside a subnegotiation
	stateSBIAC                     // saw IAC inside a subnegotiation
)

func newTelnetConn(conn io.ReadWriteCloser) *telnetConn {
	return &telnetConn{conn: conn, raw: make([]byte, 4096)}
}

// Read fills p with decoded terminal data. Negotiation consumes raw bytes
// without producing any, so Read keeps reading until at least one data byte
// is available or the connection fails — an io.Reader may not return (0, nil).
func (t *telnetConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		// Decoding never expands the stream, so reading at most len(p) raw
		// bytes guarantees the decoded result fits in p.
		want := len(p)
		if want > len(t.raw) {
			want = len(t.raw)
		}
		n, err := t.conn.Read(t.raw[:want])
		out, werr := t.decode(t.raw[:n], p)
		if werr != nil && err == nil {
			err = werr
		}
		if out > 0 || err != nil {
			return out, err
		}
	}
}

// decode runs the state machine over in, writing data bytes to out and
// sending negotiation replies. It returns the number of data bytes written
// and any error from writing a reply.
func (t *telnetConn) decode(in, out []byte) (int, error) {
	n := 0
	var replyErr error
	for _, b := range in {
		switch t.state {
		case stateData:
			if b == telnetIAC {
				t.state = stateIAC
				continue
			}
			out[n] = b
			n++
		case stateIAC:
			switch b {
			case telnetIAC: // escaped 0xFF data byte
				out[n] = b
				n++
				t.state = stateData
			case telnetDO, telnetDONT, telnetWILL, telnetWONT:
				t.cmd = b
				t.state = stateOption
			case telnetSB:
				t.state = stateSB
			default: // NOP, GA, AYT, BRK, ... — two-byte commands with no payload
				t.state = stateData
			}
		case stateOption:
			var reply byte
			switch t.cmd {
			case telnetDO:
				reply = telnetWONT
			case telnetWILL:
				reply = telnetDONT
			}
			// DONT and WONT are acknowledgements of the state we are already
			// in; answering them is what creates negotiation loops (RFC 1143).
			if reply != 0 {
				if err := t.writeRaw([]byte{telnetIAC, reply, b}); err != nil && replyErr == nil {
					replyErr = fmt.Errorf("telnet negotiation reply: %w", err)
				}
			}
			t.state = stateData
		case stateSB:
			if b == telnetIAC {
				t.state = stateSBIAC
			}
		case stateSBIAC:
			switch b {
			case telnetSE:
				t.state = stateData
			case telnetIAC: // escaped 0xFF inside the subnegotiation payload
				t.state = stateSB
			default:
				// Malformed; treat as the end of the subnegotiation rather
				// than swallowing the rest of the stream.
				t.state = stateData
			}
		}
	}
	return n, replyErr
}

// Write sends p as NVT text: 0xFF is escaped as IAC IAC and a bare "\n"
// becomes CR LF, the telnet line ending (what a terminal client sends for
// Enter). The session layer writes "<command>\n", so this is the only place
// the line ending is decided.
func (t *telnetConn) Write(p []byte) (int, error) {
	buf := make([]byte, 0, len(p)+8)
	for i, b := range p {
		switch {
		case b == telnetIAC:
			buf = append(buf, telnetIAC, telnetIAC)
		case b == '\n' && (i == 0 || p[i-1] != '\r'):
			buf = append(buf, '\r', '\n')
		default:
			buf = append(buf, b)
		}
	}
	if err := t.writeRaw(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (t *telnetConn) writeRaw(b []byte) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	for len(b) > 0 {
		n, err := t.conn.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

func (t *telnetConn) Close() error { return t.conn.Close() }

// NewTelnetClient connects to the switch over telnet (plaintext, no server
// authentication — see Options.Transport), answers a "User Name:" /
// "Password:" login if the switch presents one, enters enable mode if needed,
// learns the prompt, disables paging and returns a ready-to-use Client.
func NewTelnetClient(opts Options) (*Client, error) {
	if opts.Port == 0 {
		opts.Port = 23
	}
	if opts.TimeoutSeconds == 0 {
		opts.TimeoutSeconds = 30
	}

	addr := net.JoinHostPort(opts.Host, fmt.Sprint(opts.Port))
	conn, err := net.DialTimeout("tcp", addr, time.Duration(opts.TimeoutSeconds)*time.Second)
	if err != nil {
		return nil, fmt.Errorf("telnet dial: %w", err)
	}
	return newTelnetSession(conn, opts)
}

// newTelnetSession runs the telnet login and session start-up over an
// established connection. Split from NewTelnetClient so tests can drive it
// over an in-memory pipe.
func newTelnetSession(conn io.ReadWriteCloser, opts Options) (*Client, error) {
	tc := newTelnetConn(conn)
	c := newSessionClient(tc, tc, tc.Close, opts)

	initialOutput, err := c.readUntilPromptInitial(true)
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("waiting for telnet login or prompt: %w", err)
	}
	if err := c.start(initialOutput); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}
