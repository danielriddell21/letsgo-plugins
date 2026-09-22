package tap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
