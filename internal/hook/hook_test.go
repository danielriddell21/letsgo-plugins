package hook

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type in struct {
	Name string `json:"name"`
}

type out struct {
	Greeting string `json:"greeting"`
}

func greet(i in) (out, error) {
	return out{Greeting: "hello " + i.Name}, nil
}

// call runs the hook with the given arguments and stdin, and returns what it
// wrote to stdout. Both streams are swapped for pipes: the contract is what
// crosses them, so that is what the test drives.
func call(t *testing.T, args []string, stdin string) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = r.Close() }()

	input, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	if _, err := input.WriteString(stdin); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatalf("seek: %v", err)
	}

	oldArgs, oldIn, oldOut := os.Args, os.Stdin, os.Stdout
	os.Args, os.Stdin, os.Stdout = args, input, w
	runErr := run("greet", greet)
	os.Args, os.Stdin, os.Stdout = oldArgs, oldIn, oldOut

	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var written strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		written.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return written.String(), runErr
}

func TestRunAnswersItsHook(t *testing.T) {
	stdout, err := call(t, []string{"letsgo-greet", "greet"}, `{"name":"world"}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	var got out
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not one JSON object: %q", stdout)
	}
	if got.Greeting != "hello world" {
		t.Errorf("got %q", got.Greeting)
	}
}

// A plugin asked for a hook it does not implement says so, rather than
// answering the wrong question with a plausible-looking result.
func TestRunRefusesAnotherHook(t *testing.T) {
	_, err := call(t, []string{"letsgo-greet", "ldflags"}, `{"name":"world"}`)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "ldflags") {
		t.Errorf("the error should name the hook asked for, got %v", err)
	}
}

func TestRunNeedsExactlyOneArgument(t *testing.T) {
	for _, args := range [][]string{
		{"letsgo-greet"},
		{"letsgo-greet", "greet", "extra"},
	} {
		if _, err := call(t, args, `{}`); err == nil {
			t.Errorf("%v should not be accepted", args)
		}
	}
}

func TestRunRejectsInputThatIsNotJSON(t *testing.T) {
	if _, err := call(t, []string{"letsgo-greet", "greet"}, "not json"); err == nil {
		t.Error("expected a decode failure")
	}
}
