package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Repo is where releases are published. It matches the module path by
// construction; spelled out here so the URL is greppable.
const Repo = "pwshehan/device-status-monitor"

// defaultBaseURL is the GitHub API root. The tests point a Checker at an
// httptest server instead, which is the only reason this is a field on the
// client rather than a constant in the URL.
const defaultBaseURL = "https://api.github.com"

// SumsAsset is the checksum file every release publishes.
const SumsAsset = "SHA256SUMS"

// maxInstaller caps a download. The installer is around 15 MB; this is not a
// tuning knob but a refusal to write an unbounded stream from the network into
// ProgramData because a URL returned something unexpected.
const maxInstaller = 200 << 20

// maxMetadata caps the release JSON and the checksum file, which are a few
// kilobytes and a few hundred bytes respectively.
const maxMetadata = 1 << 20

// SetupAsset is the installer's name for a version. It has to match
// installer/setup.iss's OutputBaseFilename exactly: the release job builds that
// name and this looks it up by it.
func SetupAsset(version string) string {
	return fmt.Sprintf("LocalMonitor-Setup-%s.exe", version)
}

// release is the part of GitHub's release JSON this needs.
type release struct {
	TagName     string    `json:"tag_name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []asset   `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

// Version is the tag without its leading v.
func (r release) Version() string { return strings.TrimPrefix(r.TagName, "v") }

// asset finds one by exact name. Exact rather than by suffix or pattern: the
// name is what the release job produced and what the checksum file lists, and a
// loose match here would be a loose match on which file eventually gets run.
func (r release) asset(name string) (asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return asset{}, false
}

// client talks to GitHub.
type client struct {
	http    *http.Client
	baseURL string
	agent   string
}

func newClient(currentVersion, baseURL string) *client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &client{
		// No Client.Timeout: metadata calls and a multi-megabyte download share
		// this client, and one fixed ceiling cannot suit both. Each call sets
		// its own deadline on the context instead.
		http:    &http.Client{},
		baseURL: strings.TrimSuffix(baseURL, "/"),
		agent:   "LocalMonitor/" + currentVersion + " (+https://github.com/" + Repo + ")",
	}
}

// errNoNews means there is nothing new to look at: GitHub answered 304, or has
// no published release, or the newest one is not finished. None of those are
// failures and none of them should reach the user as one.
var errNoNews = fmt.Errorf("no new release")

// latest asks for the newest published release.
//
// /releases/latest excludes drafts and prereleases, so nothing unfinished is
// offered. etag is the one from the previous call: a steady state then costs a
// 304 rather than the whole payload, which matters because this runs
// unauthenticated against a 60-request hourly budget shared by everything else
// using the machine's address.
func (c *client) latest(ctx context.Context, etag string) (release, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	url := c.baseURL + "/repos/" + Repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return release{}, "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", c.agent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return release{}, "", err
	}
	defer func() { _ = res.Body.Close() }()

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotModified:
		return release{}, etag, errNoNews
	case http.StatusNotFound:
		// Nothing published yet. Not something to show anyone.
		return release{}, "", errNoNews
	default:
		return release{}, "", fmt.Errorf("github said %s", res.Status)
	}

	var rel release
	if err := json.NewDecoder(io.LimitReader(res.Body, maxMetadata)).Decode(&rel); err != nil {
		return release{}, "", fmt.Errorf("read release: %w", err)
	}
	if rel.Draft || rel.Prerelease {
		// /releases/latest should never return one. An unfinished release must
		// not be offered on the strength of "should".
		return release{}, "", errNoNews
	}
	return rel, res.Header.Get("ETag"), nil
}

// fetchSum downloads a release's SHA256SUMS and returns the hash it records for
// one file.
//
// This does not prove the installer is genuine: the checksums travel the same
// channel as the installer they describe, so TLS to GitHub is the real trust
// anchor. What it does prove is that the bytes written to disk are the bytes
// GitHub served, and — with the re-check before launch — that the file the
// service runs is the same one it checked.
func (c *client) fetchSum(ctx context.Context, url, file string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", c.agent)

	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: %s", SumsAsset, res.Status)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxMetadata))
	if err != nil {
		return "", err
	}
	sum, ok := parseSums(string(body), file)
	if !ok {
		return "", fmt.Errorf("%s does not list %s", SumsAsset, file)
	}
	return sum, nil
}

// parseSums reads the "<hash>  <name>" lines the release job writes.
func parseSums(body, file string) (string, bool) {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != file {
			continue
		}
		sum := strings.ToLower(fields[0])
		if len(sum) != hex.EncodedLen(sha256.Size) {
			return "", false
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return "", false
		}
		return sum, true
	}
	return "", false
}

// download streams url to dst, hashing as it goes, and fails unless the result
// matches want.
//
// Hashed in the same pass rather than by re-reading the finished file: a second
// read is a second opportunity for what is on disk to differ from what was
// checked.
func (c *client) download(ctx context.Context, url, dst, want string, done *atomic.Int64) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.agent)

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s", res.Status)
	}

	// Written beside the destination and renamed only on success, so a download
	// cut off halfway can never be mistaken for a staged installer.
	tmp := dst + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	sum := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, sum, counter{done}), io.LimitReader(res.Body, maxInstaller))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}

	if got := hex.EncodeToString(sum.Sum(nil)); got != want {
		_ = os.Remove(tmp)
		return fmt.Errorf("checksum mismatch: %s lists %s, downloaded %s", SumsAsset, want, got)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// counter reports progress to the checker, which is what turns a stalled
// download into something visible on the page rather than a spinner that never
// resolves.
type counter struct{ n *atomic.Int64 }

func (c counter) Write(p []byte) (int, error) {
	if c.n != nil {
		c.n.Add(int64(len(p)))
	}
	return len(p), nil
}

// sha256File hashes a file already on disk. Used to re-check a staged installer
// immediately before running it, and on startup to decide whether one staged
// before a restart is still the file it was.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxInstaller)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
