// Package tap publishes a generated file to a Homebrew tap.
//
// It writes through the repository contents API rather than through a
// checkout. That is not a shortcut: the API takes the blob SHA of the file
// being replaced, so a write that raced another one fails instead of silently
// clobbering it. A `git push` from a checkout taken minutes earlier has no
// such guard, and the "nothing changed, skip the commit" test it relies on is
// computed against a tree that may already be stale.
//
// This is deliberately a subset of what letsgo's own publisher does — enough
// to write one file conditionally — and it mirrors its behaviour exactly, so
// that a formula and a cask reach the same tap the same way. If letsgo ever
// exports that code, this should collapse onto it.
package tap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// prefix is the naming convention Homebrew enforces: `brew tap you/foo` looks
// for a repository called `homebrew-foo`.
const prefix = "homebrew-"

// Repo identifies a repository.
type Repo struct {
	Owner string
	Name  string
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

// Parse reads a tap reference such as "you/homebrew-tap" or "you/tap".
//
// Both spellings are accepted because both are things people write, and
// because letsgo accepts both: a repository should not have to spell its tap
// one way in letsgo.mod and another on this command line.
func Parse(s string) (Repo, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return Repo{}, fmt.Errorf("tap %q must be owner/repo", s)
	}
	if !strings.HasPrefix(name, prefix) {
		name = prefix + name
	}
	return Repo{Owner: owner, Name: name}, nil
}

// Status says what publishing did.
type Status string

const (
	Created   Status = "created"
	Updated   Status = "updated"
	Unchanged Status = "unchanged"
)

// API is the part of a forge that publishing one file needs. An interface so
// that tests can reach every decision without a network.
type API interface {
	ReadFile(ctx context.Context, repo Repo, path string) (*File, error)
	WriteFile(ctx context.Context, repo Repo, in FileInput) error
}

// File is a file in a repository.
type File struct {
	Path string

	// SHA is the blob's identifier. Updating a file requires it, and that is
	// the mechanism that makes a write fail rather than silently clobber
	// somebody else's change made in between.
	SHA string

	Content []byte
}

// Committer names who a commit is recorded as having made.
type Committer struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Identity is the author a cask is published under.
//
// Fixed rather than taken from the token, and the same identity letsgo stamps
// on a formula: a tap shared by several projects should have one author per
// tool, not one per credential. A repository publishing with an App token of
// its own still leaves an entry that says letsgo published it.
//
// This names the letsgo-champ App. The number is the App's bot user id, not its
// App id: the address GitHub resolves to an account is
// "<bot user id>+<login>@users.noreply.github.com", and the two are different
// namespaces — the App id there would look right and link to nothing.
//
// It must stay in step with letsgo's internal/brew.Committer, which is the
// cost of the two publishers not sharing code.
var Identity = Committer{
	Name:  "letsgo-champ[bot]",
	Email: "293666020+letsgo-champ[bot]@users.noreply.github.com",
}

// FileInput describes a file to write.
type FileInput struct {
	Path    string
	Message string
	Content []byte

	// SHA is the blob being replaced. Empty creates the file, and the write
	// then fails if it already exists.
	SHA string

	// Author records who the commit is attributed to. Nil leaves it to the
	// forge, which uses the token's own identity.
	//
	// No committer is ever sent: GitHub records itself as the committer of a
	// contents-API commit and signs it, which is what makes these commits
	// Verified. Supplying one replaces that field and forfeits the signature,
	// and the author is the field GitHub displays as who made the commit.
	Author *Committer
}

// Publish writes content to path in the tap, unless it is already exactly
// right.
//
// Skipping an identical file is not an optimisation. Rendering is
// deterministic, so re-running a release — after a failed upload, or because
// the notes changed — would otherwise leave a commit in someone else's
// repository saying nothing happened.
func Publish(ctx context.Context, api API, repo Repo, path, message string, content []byte) (Status, error) {
	existing, err := api.ReadFile(ctx, repo, path)
	if err != nil {
		return "", err
	}

	status, sha := Created, ""
	if existing != nil {
		if bytes.Equal(existing.Content, content) {
			return Unchanged, nil
		}
		status, sha = Updated, existing.SHA
	}

	author := Identity
	if err := api.WriteFile(ctx, repo, FileInput{
		Path:    path,
		Message: message,
		Content: content,
		SHA:     sha,
		Author:  &author,
	}); err != nil {
		return "", fmt.Errorf("publishing %s to %s: %w", path, repo, err)
	}
	return status, nil
}

const (
	defaultAPI = "https://api.github.com"

	// The version header pins the API's behaviour. Without it GitHub is free
	// to change response shapes under us.
	apiVersion = "2022-11-28"
)

// Client talks to the GitHub REST API. Two endpoints are needed, which is a
// poor reason to inherit an SDK's dependency tree into a release tool.
type Client struct {
	token string
	api   string
	http  *http.Client
}

// NewClient returns a client authenticated with token.
func NewClient(token string) *Client {
	return &Client{token: token, api: defaultAPI, http: &http.Client{Timeout: time.Minute}}
}

// SetEndpoint overrides the API host. Used by tests, and by GitHub Enterprise
// installations.
func (c *Client) SetEndpoint(api string) { c.api = strings.TrimSuffix(api, "/") }

// ReadFile fetches a file's contents. A missing file returns nil without an
// error: "not there yet" is the normal state of a cask's first publication.
func (c *Client) ReadFile(ctx context.Context, repo Repo, path string) (*File, error) {
	var result struct {
		SHA      string `json:"sha"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}

	err := c.do(ctx, http.MethodGet, c.contentsURL(repo, path), nil, &result)
	switch {
	case isNotFound(err):
		return nil, nil
	case err != nil:
		return nil, err
	}

	if result.Encoding != "base64" {
		return nil, fmt.Errorf("%s/%s came back as %q, which this client cannot decode",
			repo, path, result.Encoding)
	}
	// The API wraps base64 at 60 columns, which the strict decoder rejects.
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(result.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("decoding %s/%s: %w", repo, path, err)
	}
	return &File{Path: path, SHA: result.SHA, Content: content}, nil
}

// WriteFile creates or replaces a file, as a commit on the default branch.
func (c *Client) WriteFile(ctx context.Context, repo Repo, in FileInput) error {
	body := struct {
		Message string     `json:"message"`
		Content string     `json:"content"`
		SHA     string     `json:"sha,omitempty"`
		Author  *Committer `json:"author,omitempty"`
	}{
		Message: in.Message,
		Content: base64.StdEncoding.EncodeToString(in.Content),
		SHA:     in.SHA,
		Author:  in.Author,
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("github: encoding %s: %w", in.Path, err)
	}
	return c.do(ctx, http.MethodPut, c.contentsURL(repo, in.Path), encoded, nil)
}

func (c *Client) contentsURL(repo Repo, path string) string {
	return fmt.Sprintf("%s/repos/%s/contents/%s", c.api, repo, escapePath(path))
}

// escapePath escapes a repository path for a URL while leaving the separators
// alone, since url.PathEscape would turn them into %2F and address a file
// whose name contains slashes rather than a file in a directory.
func escapePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// statusError carries the forge's own explanation, which is almost always
// more useful than anything this could say instead.
type statusError struct {
	code    int
	message string
}

func (e *statusError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("github: %d", e.code)
	}
	return fmt.Sprintf("github: %s (%d)", e.message, e.code)
}

func isNotFound(err error) bool {
	var se *statusError
	return errors.As(err, &se) && se.code == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, url string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "letsgo-cask")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Bounded so a hostile or broken endpoint cannot exhaust memory.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("github: reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &payload)
		return &statusError{code: resp.StatusCode, message: payload.Message}
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("github: parsing response from %s: %w", req.URL, err)
	}
	return nil
}
