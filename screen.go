package screen

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Screen represents a GNU screen instance.
type Screen struct {
	Name    string
	Mutex   *sync.Mutex
	Process *os.Process
}

const screenExec = "/usr/bin/screen"

var screenDir = "/var/run/screen"
var username = ""
var mutexes sync.Map

// init will get called automatically when the library is used
func init() {
	// Check if new screendir is defined
	var isSet bool
	if screenDir, isSet = os.LookupEnv("SCREENDIR"); !isSet {
		screenDir = "/run/screen"
	}

	// Stat screendir
	_, err := os.Stat(screenDir)
	if err != nil {
		panic(err)
	}

	// Get user
	u, err := user.Current()
	if err != nil {
		panic(err)
	}
	username = u.Username
}

// New will create a screen with the given name. It waits until the system starts the screen, then returns.
func New(ctx context.Context, name string) (s Screen, err error) {
	// Check for existing screen
	if _, err = Get(name); !os.IsNotExist(err) {
		return
	}

	// Prematurely create/get and lock mutex
	m, _ := mutexes.LoadOrStore(name, new(sync.Mutex))
	mutex, _ := m.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()

	// Create new screen with name
	var out []byte
	out, err = exec.Command(screenExec, "-dmS", name).CombinedOutput()
	if err != nil {
		err = errors.New(string(out))
		return
	}

	// Wait for screen to come up
	waiting := make(chan (struct{}))
	go func() {
		for {
			time.Sleep(time.Millisecond * 100)

			s, err = Get(name)
			switch err {
			case os.ErrNotExist:
				continue
			default:
				return
			case nil:
				waiting <- struct{}{}
				break
			}
		}
	}()

	select {
	case <-ctx.Done():
		err = ctx.Err()
		return
	case <-waiting:
		return
	}
}

// Get will retrieve an existing screen, and return a Screen struct. If no screen is found, ErrNotExist type is returned.
func Get(name string) (s Screen, err error) {
	if name == "" {
		err = &os.SyscallError{Syscall: os.ErrInvalid.Error(), Err: errors.New("screen name cannot be empty")}
		return
	}

	// Run the screen -ls, check if existing screen has same name
	out, _ := exec.Command("screen", "-ls", name).CombinedOutput() // Run screen list
	if strings.Contains(string(out), "No Sockets found in") {
		err = os.ErrNotExist
		return
	}

	// Parse pid and name
	secondLine := strings.Split(string(out), "\n")[1] // Screen will always be on second line
	subject := strings.Fields(secondLine)[0]          // First part will be "<PID>.mcscreen-<name>"
	nameAndPID := strings.Split(subject, ".")         // Dissect
	s.Name = strings.TrimPrefix(nameAndPID[1], "mcscreen-")
	if i, err := strconv.Atoi(nameAndPID[0]); err == nil {
		s.Process, _ = os.FindProcess(i)
	}

	// Load mutex from global map.
	m, _ := mutexes.LoadOrStore(name, new(sync.Mutex))
	mutex, _ := m.(*sync.Mutex)
	s.Mutex = mutex

	return
}

// GetAll returns all existing screens.
func GetAll() (res []Screen) {
	out, _ := exec.Command("screen", "-ls", "mcscreen-").CombinedOutput() // Run screen list
	if strings.Contains(string(out), "No Sockets found in") {
		return nil
	}

	parsed := strings.Split(string(out), "\n")
	for i := 1; i < len(parsed)-1; i++ { // We want to skip first and last lines
		subject := strings.Fields(parsed[i])[0]   // First part will be "<PID>.mcscreen-<name>"
		nameAndPID := strings.Split(subject, ".") // Dissect

		var s Screen
		if i, err := strconv.Atoi(nameAndPID[0]); err == nil {
			s.Process, _ = os.FindProcess(i)
		}
		s.Name = nameAndPID[1]

		// Load mutex from global map.
		m, _ := mutexes.LoadOrStore(s.Name, new(sync.Mutex))
		mutex, _ := m.(*sync.Mutex)
		s.Mutex = mutex

		res = append(res, s)
	}

	return
}

// =========================================================
// ================== Builtin functions ====================
// =========================================================

// Quit will stop the screen.
func (s Screen) Quit() error {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	if !s.isOnline() {
		return &os.SyscallError{Syscall: os.ErrNotExist.Error(), Err: errors.New("screen not found")}
	}

	// Add newline to end of commands, "enter"
	out, err := exec.Command(screenExec, "-S", s.Name, "-X", "quit").CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}

	return nil
}

// Stuff will paste the given text inside stdin for the screen. You might also want to append "\n" to "Enter" the text.
func (s Screen) Stuff(commands ...string) error {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	if !s.isOnline() {
		return &os.SyscallError{Syscall: os.ErrNotExist.Error(), Err: errors.New("screen not found")}
	}

	// Add newline to end of commands, "enter"
	out, err := exec.Command(screenExec, "-S", s.Name, "-X", "stuff", strings.Join(commands, " ")).CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}

	return nil
}

// Chdir will move the screens directory.
func (s Screen) Chdir(path string) error {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	if !s.isOnline() {
		return &os.SyscallError{Syscall: os.ErrNotExist.Error(), Err: errors.New("screen not found")}
	}

	// Check path
	if _, err := os.Stat(path); err != nil {
		return err
	}

	out, err := exec.Command(screenExec, "-S", s.Name, "-X", "chdir", path).CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}

	return nil
}

// Exec starts a new process in the same screen. Multiple processes will run independently, but share stdin, stderr, and stdout, unless specified with the fdpat.
// fdpat is a small 1-4 character string that follows the pattern "/[.!:]{0,3}\|?$". See the "exec" section of "man screen" for more info. If you don't know, set as empty string.
func (s Screen) Exec(fdpat string, command string, args ...string) error {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	if !s.isOnline() {
		return &os.SyscallError{Syscall: os.ErrNotExist.Error(), Err: errors.New("screen not found")}
	}

	// Check fdpat
	if fdpat == "" {
		fdpat = ":::"
	} else if match, err := regexp.MatchString("/[.!:]{0,3}\\|?$", fdpat); err != nil {
		return err
	} else if !match {
		return &os.SyscallError{Syscall: os.ErrInvalid.Error(), Err: errors.New("invalid fdpat")}
	}

	params := append([]string{"-S", s.Name, "-X", "exec", fdpat, command}, args...)
	out, err := exec.Command(screenExec, params...).CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}

	return nil
}

// Hardcopy copies the screen's scrollback buffer into the specified file.
func (s Screen) Hardcopy(path string, append bool) error {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	if !s.isOnline() {
		return &os.SyscallError{Syscall: os.ErrNotExist.Error(), Err: errors.New("screen not found")}
	}

	// Set append option
	appendString := "off"
	if append {
		appendString = "on"
	}
	out, err := exec.Command(screenExec, "-S", s.Name, "-X", "hardcopy_append", appendString).CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}

	// Hardcopy
	out, err = exec.Command(screenExec, "-S", s.Name, "-X", "hardcopy", path).CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}

	return nil
}

// Log will enable logging for a specific session. Set path to an empty string to disable logging. Default flushInterval is 10 (seconds).
func (s Screen) Log(path string, append bool, flushInterval uint) error {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	if !s.isOnline() {
		return &os.SyscallError{Syscall: os.ErrNotExist.Error(), Err: errors.New("screen not found")}
	}

	// Logging doesn't normally append, but that's inconsistent with Hardcopy, so I'm providing the option here.
	if _, err := os.Stat(path); err != nil && append {
		os.Truncate(path, 0)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}

	out, err := exec.Command(screenExec, "-S", s.Name, "-X", "logfile", path).CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}

	out, err = exec.Command(screenExec, "-S", s.Name, "-X", "logfile", "flush", strconv.Itoa(int(flushInterval))).CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}

	// It's worth nothing that by default, passing "", to "log" (not "logfile") toggles it, which I think isn't very useful, so "" in path means turn off.
	toggle := "on"
	if path == "" {
		toggle = "off"
	}
	out, err = exec.Command(screenExec, "-S", s.Name, "-X", "log", toggle).CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}

	return nil
}

// Clear erases the screen's scrollback buffer.
func (s Screen) Clear() error {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	if !s.isOnline() {
		return &os.SyscallError{Syscall: os.ErrNotExist.Error(), Err: errors.New("screen not found")}
	}

	out, err := exec.Command(screenExec, "-S", s.Name, "-X", "clear").CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}

	return nil
}

// =========================================================
// ================== Custom functions =====================
// =========================================================

// Signal all subprocesses of the screen, and the screen itself.
func (s Screen) Signal(signal syscall.Signal) error {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	if !s.isOnline() {
		return &os.SyscallError{Syscall: os.ErrNotExist.Error(), Err: errors.New("screen not found")}
	}

	// Get session ID
	out, err := exec.Command("ps", "--no-headers", "-p", strconv.Itoa(s.Process.Pid), "-o", "sess:1").CombinedOutput()
	if err != nil {
		return err
	}

	// Walk and kill all processes that share session ID
	out, err = exec.Command("pkill", "-s", string(out), ("-" + signal.String())).CombinedOutput()
	if err != nil && len(out) > 0 {
		return err
	}

	return nil
}

// HardcopyString copies the screen's scrollback buffer the specified file.
func (s Screen) HardcopyString() (string, error) {
	// Create a temp file
	f, err := os.CreateTemp("", "*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()

	s.Hardcopy(f.Name(), false)
	b, err := os.ReadFile(f.Name())
	if err != nil {
		return "", err
	}

	return string(b), nil
}

// StuffReturnGetOutput is for a very specific case. When executing the command (through Exec), you can specify a pipe to get the returned output of said command.
// However, if you're running a program that takes certain commands into stdin (you might want to use Stuff w/ a "\n"), you have no good way of getting the output.
// This function attempts to recreate that functionality to the best of its ability. NOTE: this function will send "\n", so you don't have to. Also, this function
// should be used cautiously, with a long wait, then search the resulting string for your desired result.
func (s Screen) StuffReturnGetOutput(ctx context.Context, commands ...string) (string, error) {
	// Create a temp file
	f, err := os.CreateTemp("", "*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(f.Name())
	defer f.Close()

	err = s.Log(f.Name(), false, 1)
	if err != nil {
		return "", err
	}
	time.Sleep(time.Second * 2)

	// Run command
	commands = append(commands, "\n")
	s.Stuff(commands...)

	// Wait for output
	var output string
	waiting := make(chan (struct{}))
	go func() {
		for {
			time.Sleep(time.Second)

			b, err := os.ReadFile(f.Name())
			if err != nil || len(b) == 0 {
				continue
			}

			output = string(b)
			break
		}
		waiting <- struct{}{}
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-waiting:
		return output, nil
	}
}

// isOnline is a quick helper function to check if a screen is still currently running.
func (s Screen) isOnline() bool {
	_, err := New(context.Background(), s.Name)
	return err == nil
}
