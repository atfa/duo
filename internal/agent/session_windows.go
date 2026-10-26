//go:build windows

package agent

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"unsafe"

	"github.com/atfa/duo/internal/driver"
	"golang.org/x/sys/windows"
)

// conptyTerminal is a Windows pseudo console. Unlike a Unix PTY it is not one file
// descriptor: the host writes into one pipe and reads the console's output from
// another, and the console is a handle that has to be closed to end it.
type conptyTerminal struct {
	in  *os.File
	out *os.File

	mu      sync.Mutex
	console windows.Handle
}

func (t *conptyTerminal) Read(p []byte) (int, error)  { return t.out.Read(p) }
func (t *conptyTerminal) Write(p []byte) (int, error) { return t.in.Write(p) }

func (t *conptyTerminal) Resize(cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.console == 0 {
		return errors.New("terminal is closed")
	}
	return windows.ResizePseudoConsole(t.console, cellSize(cols, rows))
}

// Close ends the console and the pipes. Closing the console is what releases a
// blocked reader: conhost exits and the output pipe reaches EOF.
func (t *conptyTerminal) Close() error {
	t.mu.Lock()
	console := t.console
	t.console = 0
	t.mu.Unlock()
	if console != 0 {
		windows.ClosePseudoConsole(console)
	}
	_ = t.in.Close()
	return t.out.Close()
}

// consolePointer is the value PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE carries: the
// console handle itself, not its address. Handing over the address instead leaves
// roughly one child in a hundred failing its own initialisation with
// STATUS_DLL_INIT_FAILED (0xc0000142), which is why this is a cast of the handle
// bits rather than a pointer to a variable that holds them.
func consolePointer(console windows.Handle) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&console))
}

func cellSize(cols, rows int) windows.Coord {
	return windows.Coord{X: int16(cols), Y: int16(rows)}
}

type windowsProcess struct {
	process windows.Handle
	job     windows.Handle
	pty     *conptyTerminal
}

func (p *windowsProcess) term() terminal { return p.pty }

func (p *windowsProcess) wait() error {
	if _, err := windows.WaitForSingleObject(p.process, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(p.process, &code); err != nil {
		return err
	}
	if code == 0 {
		return nil
	}
	return fmt.Errorf("exit status %d", code)
}

// interrupt types Ctrl+C into the pseudo console, which is how a Windows console
// program is asked to stop: there is no signal to send, so the console host
// delivers CTRL_C_EVENT to the process attached to it.
func (p *windowsProcess) interrupt() {
	_, _ = p.pty.Write([]byte{0x03})
}

// kill ends the job the agent was started in, so a command that spawned its own
// children does not leave them behind. A Duo that could not take a job falls back
// to the one process it owns.
func (p *windowsProcess) kill() {
	if p.job != 0 {
		_ = windows.TerminateJobObject(p.job, 1)
		return
	}
	_ = windows.TerminateProcess(p.process, 1)
}

func (p *windowsProcess) close() {
	if p.job != 0 {
		_ = windows.CloseHandle(p.job)
		p.job = 0
	}
	if p.process != 0 {
		_ = windows.CloseHandle(p.process)
		p.process = 0
	}
}

// startProcess runs command in cmd.exe attached to a new pseudo console. The
// command line is rendered for cmd.exe by the driver SDK, so the same quote rules
// apply here as to any other command Duo runs on Windows.
func startProcess(command, dir string, env []string, cols, rows int) (runningProcess, error) {
	// The console host reads agent input from one pipe and writes agent output to
	// the other; the host ends are the files Core reads and writes.
	consoleIn, in, err := createConsolePipe(true)
	if err != nil {
		return nil, err
	}
	consoleOut, out, err := createConsolePipe(false)
	if err != nil {
		windows.CloseHandle(consoleIn)
		_ = in.Close()
		return nil, err
	}

	var console windows.Handle
	if err := windows.CreatePseudoConsole(cellSize(cols, rows), consoleIn, consoleOut, 0, &console); err != nil {
		windows.CloseHandle(consoleIn)
		windows.CloseHandle(consoleOut)
		_ = in.Close()
		_ = out.Close()
		return nil, fmt.Errorf("create pseudo console: %w", err)
	}
	line := driver.WindowsShellLine(driver.ComSpec(), command)
	process, err := createConsoleProcess(line, dir, env, console)
	// The console's own pipe ends stay open until the child exists: the console
	// host connects while the child starts, and closing them earlier is what makes
	// a child fail to initialise.
	windows.CloseHandle(consoleIn)
	windows.CloseHandle(consoleOut)
	if err != nil {
		windows.ClosePseudoConsole(console)
		_ = in.Close()
		_ = out.Close()
		return nil, err
	}
	return &windowsProcess{
		process: process,
		job:     assignJob(process),
		pty:     &conptyTerminal{in: in, out: out, console: console},
	}, nil
}

// createConsolePipe creates one pipe for a pseudo console and returns the end the
// console host uses plus Core's own end as a file. The handles are not inheritable:
// the console host receives them through CreatePseudoConsole, not through
// CreateProcess, and Core must not leak them into the agent.
func createConsolePipe(hostWrites bool) (console windows.Handle, host *os.File, err error) {
	var read, write windows.Handle
	if err := windows.CreatePipe(&read, &write, nil, 0); err != nil {
		return 0, nil, err
	}
	console, hostHandle := read, write
	if !hostWrites {
		console, hostHandle = write, read
	}
	file := os.NewFile(uintptr(hostHandle), "conpty")
	if file == nil {
		windows.CloseHandle(read)
		windows.CloseHandle(write)
		return 0, nil, errors.New("wrap pseudo console pipe")
	}
	return console, file, nil
}

// createConsoleProcess starts commandLine with the pseudo console attached.
//
// The console is a creation attribute, not a handle passed down: it travels in a
// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE entry of a STARTUPINFOEX, which is why this
// cannot go through os/exec and is built with CreateProcess directly.
func createConsoleProcess(commandLine, dir string, env []string, console windows.Handle) (windows.Handle, error) {
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, fmt.Errorf("prepare process attributes: %w", err)
	}
	defer attributes.Delete()

	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, consolePointer(console), unsafe.Sizeof(console)); err != nil {
		return 0, fmt.Errorf("attach pseudo console: %w", err)
	}

	// STARTF_USESTDHANDLES is what makes the child a client of the pseudo console
	// instead of an heir to Core's own stdin/stdout: without it Windows passes the
	// parent's handles down and the agent reads a closed pipe, so its output never
	// reaches the terminal Core renders. The handles themselves stay zero — the
	// console supplies the real ones.
	startup := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:    uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags: windows.STARTF_USESTDHANDLES,
		},
		ProcThreadAttributeList: attributes.List(),
	}
	line, err := windows.UTF16PtrFromString(commandLine)
	if err != nil {
		return 0, err
	}
	var cwd *uint16
	if strings.TrimSpace(dir) != "" {
		if cwd, err = windows.UTF16PtrFromString(dir); err != nil {
			return 0, err
		}
	}
	envBlock, err := environmentBlock(env)
	if err != nil {
		return 0, err
	}

	var info windows.ProcessInformation
	if err := windows.CreateProcess(nil, line, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		&envBlock[0], cwd, &startup.StartupInfo, &info); err != nil {
		return 0, fmt.Errorf("start %s: %w", commandLine, err)
	}
	_ = windows.CloseHandle(info.Thread)
	return info.Process, nil
}

// assignJob gives the agent its own job object so Stop can end its whole tree. A
// process already in a job that forbids nesting cannot be added; kill then falls
// back to terminating the process itself.
func assignJob(process windows.Handle) windows.Handle {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0
	}
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(job)
		return 0
	}
	return job
}
