package tap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in   string
		want string
		bad  bool
	}{
		{in: "you/tap", want: "you/homebrew-tap"},
		{in: "you/homebrew-tap", want: "you/homebrew-tap"},
		{in: "  you/brew  ", want: "you/homebrew-brew"},
		{in: "you", bad: true},
		{in: "/tap", bad: true},
		{in: "you/", bad: true},
		{in: "you/a/b", bad: true},
	}

	for _, tt := range tests {
		got, err := Parse(tt.in)
		if tt.bad {
			if err == nil {
				t.Errorf("Parse(%q) = %s, want an error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.in, err)
			continue
		}
		if got.String() != tt.want {
			t.Errorf("Parse(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

// fake records what publishing asked of the forge.
type fake struct {
	existing *File
	wrote    *FileInput
}

func (f *fake) ReadFile(context.Context, Repo, string) (*File, error) { return f.existing, nil }

func (f *fake) WriteFile(_ context.Context, _ Repo, in FileInput) error {
	f.wrote = &in
	return nil
}

func TestPublish(t *testing.T) {
	repo := Repo{Owner: "you", Name: "homebrew-tap"}
	content := []byte("cask \"thing\"\n")

	t.Run("a missing file is created, with no SHA", func(t *testing.T) {
		f := &fake{}
		status, err := Publish(context.Background(), f, repo, "Casks/thing.rb", "thing 1.0.0", content)
		if err != nil {
			t.Fatal(err)
		}
		if status != Created {
			t.Errorf("status = %q, want %q", status, Created)
		}
		if f.wrote == nil {
			t.Fatal("nothing was written")
		}
		if f.wrote.SHA != "" {
			t.Errorf("SHA = %q, want empty: an empty SHA is what makes the create fail if the file appeared", f.wrote.SHA)
		}
	})

	t.Run("a changed file is updated with the SHA it read", func(t *testing.T) {
		f := &fake{existing: &File{SHA: "abc123", Content: []byte("old\n")}}
		status, err := Publish(context.Background(), f, repo, "Casks/thing.rb", "thing 1.0.0", content)
		if err != nil {
			t.Fatal(err)
		}
		if status != Updated {
			t.Errorf("status = %q, want %q", status, Updated)
		}
		if f.wrote.SHA != "abc123" {
			t.Errorf("SHA = %q, want abc123: the conditional write is the whole guard against clobbering", f.wrote.SHA)
		}
	})

	// The property the shell version approximated with `git diff --quiet`
	// against a possibly stale checkout.
	t.Run("an identical file is not committed again", func(t *testing.T) {
		f := &fake{existing: &File{SHA: "abc123", Content: content}}
		status, err := Publish(context.Background(), f, repo, "Casks/thing.rb", "thing 1.0.0", content)
		if err != nil {
			t.Fatal(err)
		}
		if status != Unchanged {
			t.Errorf("status = %q, want %q", status, Unchanged)
		}
		if f.wrote != nil {
			t.Errorf("wrote %+v, want no write at all", f.wrote)
		}
	})
}

func TestClientReadFile(t *testing.T) {
	t.Run("a file comes back decoded", func(t *testing.T) {
		// Wrapped at 60 columns, as the API does and as the strict decoder
		// rejects.
		body := base64.StdEncoding.EncodeToString([]byte("cask \"thing\"\n"))
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer t" {
				t.Errorf("Authorization = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"sha": "abc123", "encoding": "base64", "content": body[:4] + "\n" + body[4:],
			})
		}))
		defer server.Close()

		c := NewClient("t")
		c.SetEndpoint(server.URL)
		file, err := c.ReadFile(context.Background(), Repo{"you", "homebrew-tap"}, "Casks/thing.rb")
		if err != nil {
			t.Fatal(err)
		}
		if string(file.Content) != "cask \"thing\"\n" || file.SHA != "abc123" {
			t.Errorf("got %q sha %q", file.Content, file.SHA)
		}
	})

	t.Run("a missing file is not an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
		}))
		defer server.Close()

		c := NewClient("t")
		c.SetEndpoint(server.URL)
		file, err := c.ReadFile(context.Background(), Repo{"you", "homebrew-tap"}, "Casks/thing.rb")
		if err != nil || file != nil {
			t.Errorf("got %v, %v; want nil, nil", file, err)
		}
	})

	t.Run("any other failure is reported", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Resource not accessible by integration"})
		}))
		defer server.Close()

		c := NewClient("t")
		c.SetEndpoint(server.URL)
		if _, err := c.ReadFile(context.Background(), Repo{"you", "homebrew-tap"}, "Casks/thing.rb"); err == nil {
			t.Error("want an error for 403")
		}
	})
}

func TestClientWriteFile(t *testing.T) {
	var got struct {
		Message string `json:"message"`
		Content string `json:"content"`
		SHA     string `json:"sha"`
	}
	var path string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	c := NewClient("t")
	c.SetEndpoint(server.URL)
	err := c.WriteFile(context.Background(), Repo{"you", "homebrew-tap"}, FileInput{
		Path: "Casks/thing.rb", Message: "thing 1.0.0", Content: []byte("body\n"), SHA: "abc123",
	})
	if err != nil {
		t.Fatal(err)
	}

	if want := "/repos/you/homebrew-tap/contents/Casks/thing.rb"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	decoded, err := base64.StdEncoding.DecodeString(got.Content)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != "body\n" || got.SHA != "abc123" || got.Message != "thing 1.0.0" {
		t.Errorf("body = %+v, content %q", got, decoded)
	}
}

// A path is escaped per segment: the separators address directories, and
// escaping them would address one file with slashes in its name.
func TestEscapePath(t *testing.T) {
	if got := escapePath("Casks/my thing.rb"); got != "Casks/my%20thing.rb" {
		t.Errorf("escapePath = %q", got)
	}
}

// The forge's own explanation is almost always more useful than anything this
// could say instead, so it has to survive into the error text.
func TestStatusError(t *testing.T) {
	tests := []struct {
		name string
		err  *statusError
		want string
	}{
		{
			name: "with a message", err: &statusError{code: 403, message: "Resource not accessible by integration"},
			want: "github: Resource not accessible by integration (403)",
		},
		{name: "without one", err: &statusError{code: 500}, want: "github: 500"},
	}

	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("%s: Error() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// failing serves the same status to everything, so both halves of publishing
// can be made to fail in turn.
func failing(t *testing.T, on string, code int) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != on {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"sha": "abc123", "encoding": "base64",
				"content": base64.StdEncoding.EncodeToString([]byte("old\n")),
			})
			return
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "nope"})
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestPublishReportsForgeFailures(t *testing.T) {
	repo := Repo{Owner: "you", Name: "homebrew-tap"}

	// A read that fails is not "the file is not there": returning nil would
	// turn a 403 into a create, and the create would fail too — one step later
	// and with a worse message.
	t.Run("a failed read", func(t *testing.T) {
		c := NewClient("t")
		c.SetEndpoint(failing(t, http.MethodGet, http.StatusForbidden))
		if _, err := Publish(context.Background(), c, repo, "Casks/t.rb", "m", []byte("x")); err == nil {
			t.Error("want an error")
		}
	})

	// The message has to name the file and the tap: a release log that says
	// only "403" leaves somebody guessing which of the two writes failed.
	t.Run("a failed write", func(t *testing.T) {
		c := NewClient("t")
		c.SetEndpoint(failing(t, http.MethodPut, http.StatusForbidden))
		_, err := Publish(context.Background(), c, repo, "Casks/t.rb", "m", []byte("x"))
		if err == nil {
			t.Fatal("want an error")
		}
		if !strings.Contains(err.Error(), "Casks/t.rb") || !strings.Contains(err.Error(), repo.String()) {
			t.Errorf("err = %v, want it to name the file and the tap", err)
		}
	})
}

func TestReadFileRejectsWhatItCannotDecode(t *testing.T) {
	tests := []struct {
		name string
		body map[string]string
	}{
		{
			// The API has one other encoding, "none", for a file too large to
			// inline. Guessing at it would hand back empty content and publish
			// a cask over a file this never read.
			name: "an encoding this client does not know",
			body: map[string]string{"sha": "a", "encoding": "none", "content": ""},
		},
		{
			name: "base64 that does not decode",
			body: map[string]string{"sha": "a", "encoding": "base64", "content": "!!!not base64!!!"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(tt.body)
			}))
			defer server.Close()

			c := NewClient("t")
			c.SetEndpoint(server.URL)
			if _, err := c.ReadFile(context.Background(), Repo{"you", "homebrew-tap"}, "Casks/t.rb"); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestDoReportsTransportFailures(t *testing.T) {
	t.Run("an unreachable host", func(t *testing.T) {
		// A server that is closed before the request, so dialling fails.
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := server.URL
		server.Close()

		c := NewClient("t")
		c.SetEndpoint(url)
		if _, err := c.ReadFile(context.Background(), Repo{"you", "homebrew-tap"}, "Casks/t.rb"); err == nil {
			t.Error("want an error")
		}
	})

	t.Run("a request that cannot be built", func(t *testing.T) {
		c := NewClient("t")
		c.SetEndpoint("http://\x7f invalid")
		if _, err := c.ReadFile(context.Background(), Repo{"you", "homebrew-tap"}, "Casks/t.rb"); err == nil {
			t.Error("want an error")
		}
	})

	// A 2xx carrying something that is not JSON. Distinct from a truncated
	// body, which fails while being read rather than while being parsed, and
	// distinct again from an error status — all three have to say so rather
	// than hand back a zero-valued file that publishing would treat as real.
	t.Run("a successful response that is not JSON", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html>a proxy said hello</html>"))
		}))
		defer server.Close()

		c := NewClient("t")
		c.SetEndpoint(server.URL)
		_, err := c.ReadFile(context.Background(), Repo{"you", "homebrew-tap"}, "Casks/t.rb")
		if err == nil {
			t.Fatal("want an error")
		}
		// The URL has to be in it: this is the failure that happens when
		// something between here and GitHub answers instead of GitHub.
		if !strings.Contains(err.Error(), "parsing response") {
			t.Errorf("err = %v, want it to say what failed", err)
		}
	})

	// A body that stops early: the response is unreadable rather than absent,
	// which is a different failure from a status code.
	t.Run("a truncated body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "64")
			_, _ = w.Write([]byte("{"))
		}))
		defer server.Close()

		c := NewClient("t")
		c.SetEndpoint(server.URL)
		if _, err := c.ReadFile(context.Background(), Repo{"you", "homebrew-tap"}, "Casks/t.rb"); err == nil {
			t.Error("want an error")
		}
	})
}

// The identity is letsgo's, not the token's, and it matches what letsgo stamps
// on a formula. A tap shared by several projects should have one author per
// tool rather than one per credential.
func TestPublishCommitsAsLetsgo(t *testing.T) {
	f := &fake{}
	repo := Repo{Owner: "you", Name: "homebrew-tap"}
	if _, err := Publish(context.Background(), f, repo, "Casks/t.rb", "t 1.0.0", []byte("x")); err != nil {
		t.Fatal(err)
	}

	got := f.wrote.Author
	if got == nil {
		t.Fatal("no committer was sent, so the forge would attribute the commit to the token")
	}
	if *got != Identity {
		t.Errorf("committer = %+v, want %+v", *got, Identity)
	}
	if got.Name == "" || got.Email == "" {
		t.Errorf("committer = %+v, want both fields set: GitHub rejects a partial one", *got)
	}
}

func TestPublishHonoursAnOverriddenIdentity(t *testing.T) {
	original := Identity
	t.Cleanup(func() { Identity = original })
	Identity = Committer{Name: "tap-bot", Email: "bot@example.com"}

	f := &fake{}
	repo := Repo{Owner: "you", Name: "homebrew-tap"}
	if _, err := Publish(context.Background(), f, repo, "Casks/t.rb", "t 1.0.0", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := f.wrote.Author; got == nil || got.Name != "tap-bot" {
		t.Errorf("committer = %+v, want tap-bot", got)
	}
}

// The author is sent and the committer is not: GitHub records itself as the
// committer of a contents-API commit and signs it, which is what makes these
// commits Verified.
func TestWriteFileSendsTheAuthorAndNotTheCommitter(t *testing.T) {
	var got struct {
		Committer *Committer `json:"committer"`
		Author    *Committer `json:"author"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	c := NewClient("t")
	c.SetEndpoint(server.URL)
	want := Committer{Name: "letsgo-champ[bot]", Email: "293666020+letsgo-champ[bot]@users.noreply.github.com"}
	err := c.WriteFile(context.Background(), Repo{"you", "homebrew-tap"}, FileInput{
		Path: "Casks/t.rb", Message: "t 1.0.0", Content: []byte("x"), Author: &want,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Author == nil || *got.Author != want {
		t.Errorf("author = %+v, want %+v", got.Author, want)
	}
	if got.Committer != nil {
		t.Errorf("committer = %+v, want none: sending one loses GitHub's signature", got.Committer)
	}
}

// Nil means "leave it to the forge", and the key has to be absent rather than
// empty: GitHub rejects an author with no name.
func TestWriteFileOmitsAnAbsentAuthor(t *testing.T) {
	var raw map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	c := NewClient("t")
	c.SetEndpoint(server.URL)
	err := c.WriteFile(context.Background(), Repo{"you", "homebrew-tap"}, FileInput{
		Path: "Casks/t.rb", Message: "t 1.0.0", Content: []byte("x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"committer", "author"} {
		if _, present := raw[key]; present {
			t.Errorf("%q was sent as %v, want the key absent", key, raw[key])
		}
	}
}
