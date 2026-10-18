package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/term"
)

func runNativeAttach(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("native", flag.ContinueOnError)
	socket := flags.String("socket", "", "private Duo native socket")
	agent := flags.String("agent", "", "agent id")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *socket == "" || (*agent != "austin" && *agent != "tony") {
		return fmt.Errorf("usage: duo native --socket <path> --agent <austin|tony>")
	}
	conn, err := net.Dial("unix", *socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("native attach requires a terminal: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), state)

	var writeMu sync.Mutex
	send := func(kind byte, payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		var header [5]byte
		header[0] = kind
		binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
		if err := writeAll(conn, header[:]); err != nil {
			return err
		}
		return writeAll(conn, payload)
	}
	resize := func() {
		cols, rows, err := term.GetSize(int(os.Stdin.Fd()))
		if err != nil {
			return
		}
		cols, rows = nativeTerminalSize(cols, rows)
		var payload [8]byte
		binary.BigEndian.PutUint32(payload[:4], uint32(cols))
		binary.BigEndian.PutUint32(payload[4:], uint32(rows))
		_ = send('R', payload[:])
	}
	resize()

	outputErr := make(chan error, 1)
	go func() {
		for {
			kind, payload, err := readNativeClientFrame(conn)
			if err != nil {
				outputErr <- err
				return
			}
			if kind == 'E' {
				outputErr <- fmt.Errorf("native attach failed: %s", payload)
				return
			}
			if kind == 'C' && len(payload) == 0 {
				outputErr <- nil
				return
			}
			if kind != 'O' {
				outputErr <- fmt.Errorf("invalid Duo native frame %q", kind)
				return
			}
			if err := writeAll(os.Stdout, payload); err != nil {
				outputErr <- err
				return
			}
		}
	}()
	inputErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				for i, b := range chunk {
					if b == 0x1c || b == 0x1d {
						if i > 0 {
							if e := send('D', chunk[:i]); e != nil {
								inputErr <- e
								return
							}
						}
						_ = send('Q', nil)
						inputErr <- nil
						return
					}
				}
				if e := send('D', chunk); e != nil {
					inputErr <- e
					return
				}
			}
			if err != nil {
				if err == io.EOF {
					_ = send('Q', nil)
				}
				inputErr <- err
				return
			}
		}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH, syscall.SIGHUP, syscall.SIGTERM)
	defer signal.Stop(signals)
	for {
		select {
		case err := <-outputErr:
			if err != nil {
				return fmt.Errorf("native terminal disconnected: %w", err)
			}
			return nil
		case err := <-inputErr:
			if err == nil || err == io.EOF {
				return nil
			}
			return err
		case sig := <-signals:
			if sig == syscall.SIGWINCH {
				resize()
			} else {
				_ = send('Q', nil)
				return nil
			}
		case <-ctx.Done():
			_ = send('Q', nil)
			return nil
		}
	}
}

func nativeTerminalSize(cols, rows int) (int, int) {
	if cols <= 0 || rows <= 0 {
		return 80, 24
	}
	return cols, rows
}

func readNativeClientFrame(r io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(header[1:])
	if n > 1<<20 {
		return 0, nil, fmt.Errorf("native frame exceeds 1 MiB")
	}
	payload := make([]byte, int(n))
	_, err := io.ReadFull(r, payload)
	return header[0], payload, err
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
