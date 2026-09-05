package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pwshehan/device-status-monitor/internal/appdir"
	"github.com/pwshehan/device-status-monitor/internal/store"
)

// installerBody stands in for the real setup exe. Its contents do not matter;
// what matters is that the checker only accepts it when the checksum it is
// given describes these exact bytes.
var installerBody = []byte("this is not really an installer, but it hashes like one")

// fakeGitHub serves a releases API and the two assets a release publishes.
type fakeGitHub struct {
	srv *httptest.Server

	// version is the release it reports. Empty means "no releases yet".
	version string

	// sum is what SHA256SUMS claims about the installer. Empty means the real
	// hash of installerBody; set it to something else to fake a bad download.
	sum string

	// omitSums drops the SHA256SUMS asset from the release.
	omitSums bool

	// etag is what it returns and what it honours in If-None-Match.
	etag string

	calls   atomic.Int64
	notMod  atomic.Int64
	fetched atomic.Int64
}

func newFakeGitHub(t *testing.T, version string) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{version: version, etag: `W/"v1"`}

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		g.calls.Add(1)
		if g.version == "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("If-None-Match") == g.etag {
			g.notMod.Add(1)
			w.Header().Set("ETag", g.etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		assets := []asset{{
			Name: SetupAsset(g.version),
			Size: int64(len(installerBody)),
			URL:  g.srv.URL + "/assets/setup",
		}}
		if !g.omitSums {
			assets = append(assets, asset{
				Name: SumsAsset,
				Size: 128,
				URL:  g.srv.URL + "/assets/sums",
			})
		}
		w.Header().Set("ETag", g.etag)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(release{
			TagName:     "v" + g.version,
			HTMLURL:     "https://github.com/" + Repo + "/releases/tag/v" + g.version,
			PublishedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
			Assets:      assets,
		})
	})
	mux.HandleFunc("/assets/sums", func(w http.ResponseWriter, r *http.Request) {
		sum := g.sum
		if sum == "" {
			sum = hashOf(installerBody)
		}
		// The exact shape the release job writes: two spaces, lowercase hash.
		_, _ = io.WriteString(w, sum+"  "+SetupAsset(g.version)+"\n")
		_, _ = io.WriteString(w, hashOf([]byte("something else"))+"  monitor-service.exe\n")
	})
	mux.HandleFunc("/assets/setup", func(w http.ResponseWriter, r *http.Request) {
		g.fetched.Add(1)
		_, _ = w.Write(installerBody)
	})

	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newChecker wires a Checker to a real store in a temp directory, which is how
// every other package here tests: the settings table is where update state
// lives, and a fake of it would not exercise that.
func newChecker(t *testing.T, g *fakeGitHub, current string) *Checker {
	t.Helper()
	ctx := context.Background()

	root := t.TempDir()
	dirs := appdir.Dirs{Root: root}
	if err := dirs.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dirs.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SeedSettings(ctx); err != nil {
		t.Fatal(err)
	}

	c := &Checker{
		Store:        st,
		Dirs:         dirs,
		Current:      current,
		Log:          quiet(),
		StartupDelay: -1,
	}
	if g != nil {
		c.BaseURL = g.srv.URL
	}
	return c
}

func TestANewerReleaseIsStagedAndChecked(t *testing.T) {
	ctx := context.Background()
	g := newFakeGitHub(t, "1.1.0")
	c := newChecker(t, g, "1.0.0")

	got := c.Once(ctx)
	if got.State != StateReady {
		t.Fatalf("state = %q (%s), want %q", got.State, got.Err, StateReady)
	}
	if got.Latest != "1.1.0" {
		t.Errorf("latest = %q, want 1.1.0", got.Latest)
	}
	if got.NotesURL == "" {
		t.Error("notes url is empty; the page has nothing to link to")
	}
	if got.LastCheckedAt.IsZero() {
		t.Error("last checked was not recorded")
	}

	staged := filepath.Join(c.Dirs.UpdateDir(), SetupAsset("1.1.0"))
	body, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("staged installer: %v", err)
	}
	if string(body) != string(installerBody) {
		t.Error("staged installer is not what the server served")
	}

	// The hash has to be recorded, because it is what the file is re-checked
	// against immediately before it is executed.
	all, err := c.Store.AllSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if all[store.KeyUpdatesReadyVer] != "1.1.0" {
		t.Errorf("ready_version = %q, want 1.1.0", all[store.KeyUpdatesReadyVer])
	}
	if all[store.KeyUpdatesReadySHA256] != hashOf(installerBody) {
		t.Errorf("ready_sha256 = %q, want the installer's hash", all[store.KeyUpdatesReadySHA256])
	}
}

func TestAnInstallerThatDoesNotMatchItsChecksumIsRefused(t *testing.T) {
	ctx := context.Background()
	g := newFakeGitHub(t, "1.1.0")
	g.sum = hashOf([]byte("a different installer entirely"))
	c := newChecker(t, g, "1.0.0")

	got := c.Once(ctx)
	if got.State != StateError {
		t.Fatalf("state = %q, want %q", got.State, StateError)
	}
	if got.Err == "" {
		t.Error("no message explaining the refusal")
	}

	// Nothing may be left behind — not the finished file and not the partial.
	entries, err := os.ReadDir(c.Dirs.UpdateDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("left behind after a bad download: %s", e.Name())
	}

	all, _ := c.Store.AllSettings(ctx)
	if all[store.KeyUpdatesReadyVer] != "" {
		t.Errorf("ready_version = %q after a failed check, want empty", all[store.KeyUpdatesReadyVer])
	}
}

func TestAReleaseWithNoChecksumsIsRefused(t *testing.T) {
	ctx := context.Background()
	g := newFakeGitHub(t, "1.1.0")
	g.omitSums = true
	c := newChecker(t, g, "1.0.0")

	got := c.Once(ctx)
	if got.State != StateError {
		t.Fatalf("state = %q, want %q", got.State, StateError)
	}
	if g.fetched.Load() != 0 {
		t.Error("the installer was downloaded even though nothing could check it")
	}
}

func TestNothingNewerMeansIdle(t *testing.T) {
	ctx := context.Background()
	g := newFakeGitHub(t, "1.0.0")
	c := newChecker(t, g, "1.0.0")

	if got := c.Once(ctx); got.State != StateIdle {
		t.Fatalf("state = %q, want %q", got.State, StateIdle)
	}
	if g.fetched.Load() != 0 {
		t.Error("an installer was downloaded for a version that is already running")
	}
}

func TestAnOlderReleaseIsNotOffered(t *testing.T) {
	ctx := context.Background()
	g := newFakeGitHub(t, "0.9.0")
	c := newChecker(t, g, "1.0.0")

	if got := c.Once(ctx); got.State != StateIdle {
		t.Fatalf("state = %q, want %q", got.State, StateIdle)
	}
}

func TestASecondCheckCostsAConditionalRequest(t *testing.T) {
	ctx := context.Background()
	g := newFakeGitHub(t, "1.1.0")
	c := newChecker(t, g, "1.0.0")

	c.Once(ctx)
	before := c.Status()
	c.Once(ctx)

	if g.notMod.Load() != 1 {
		t.Errorf("conditional requests = %d, want 1; the etag is not being sent", g.notMod.Load())
	}
	if g.fetched.Load() != 1 {
		t.Errorf("installer downloads = %d, want 1; it was fetched again", g.fetched.Load())
	}
	if after := c.Status(); after.State != before.State {
		t.Errorf("state moved from %q to %q on a 304", before.State, after.State)
	}
}

func TestAutomaticDownloadCanBeTurnedOff(t *testing.T) {
	ctx := context.Background()
	g := newFakeGitHub(t, "1.1.0")
	c := newChecker(t, g, "1.0.0")
	put(t, c, store.KeyUpdatesAutoDownload, "0")

	got := c.Once(ctx)
	if got.State != StateAvailable {
		t.Fatalf("state = %q, want %q", got.State, StateAvailable)
	}
	if g.fetched.Load() != 0 {
		t.Error("the installer was downloaded with automatic downloads off")
	}

	// Asking explicitly still works.
	if err := c.Download(ctx); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := c.Status(); got.State != StateReady {
		t.Fatalf("state after Download = %q, want %q", got.State, StateReady)
	}
}

func TestCheckingCanBeTurnedOffEntirely(t *testing.T) {
	ctx := context.Background()
	g := newFakeGitHub(t, "1.1.0")
	c := newChecker(t, g, "1.0.0")
	put(t, c, store.KeyUpdatesEnabled, "0")

	got := c.Once(ctx)
	if g.calls.Load() != 0 {
		t.Errorf("github was contacted %d time(s) with updates disabled", g.calls.Load())
	}
	if got.Enabled {
		t.Error("status says enabled with the setting off")
	}
	if got.State != StateIdle {
		t.Errorf("state = %q, want %q", got.State, StateIdle)
	}
}

func TestADevBuildIsNeverOfferedAnUpgrade(t *testing.T) {
	ctx := context.Background()
	g := newFakeGitHub(t, "9.9.9")
	c := newChecker(t, g, Dev)

	got := c.Once(ctx)
	if g.calls.Load() != 0 {
		t.Errorf("github was contacted %d time(s) for a dev build", g.calls.Load())
	}
	if got.Supported {
		t.Error("a dev build reports itself as updatable")
	}
	if got.State != StateIdle {
		t.Errorf("state = %q, want %q", got.State, StateIdle)
	}
}

func TestAnUnreachableGitHubIsNotAFailure(t *testing.T) {
	ctx := context.Background()
	c := newChecker(t, nil, "1.0.0")
	// A port nothing is listening on stands in for a machine with no route out,
	// which the README is explicit is a supported way to run this.
	c.BaseURL = "http://127.0.0.1:1"

	got := c.Once(ctx)
	if got.State == StateError {
		t.Error("a failed check moved the state to error; it should only be noted")
	}
	if got.Err == "" {
		t.Error("the failure was not recorded anywhere the page could show it")
	}
	if got.LastCheckedAt.IsZero() {
		t.Error("the attempt was not recorded, so the next one cannot be spaced from it")
	}
}

func TestAFailedCheckLeavesAStagedUpdateAlone(t *testing.T) {
	ctx := context.Background()
	g := newFakeGitHub(t, "1.1.0")
	c := newChecker(t, g, "1.0.0")

	if got := c.Once(ctx); got.State != StateReady {
		t.Fatalf("setup: state = %q (%s)", got.State, got.Err)
	}

	c.BaseURL = "http://127.0.0.1:1"
	got := c.Once(ctx)
	if got.State != StateReady {
		t.Errorf("state = %q after a failed check, want the staged update to survive", got.State)
	}
	if _, err := os.Stat(filepath.Join(c.Dirs.UpdateDir(), SetupAsset("1.1.0"))); err != nil {
		t.Errorf("the staged installer was removed by a failed check: %v", err)
	}
}

func TestAStagedUpdateSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	g := newFakeGitHub(t, "1.1.0")
	c := newChecker(t, g, "1.0.0")
	if got := c.Once(ctx); got.State != StateReady {
		t.Fatalf("setup: state = %q (%s)", got.State, got.Err)
	}

	// A second Checker over the same store and directory is what a restart of
	// the service looks like from here.
	restarted := &Checker{
		Store: c.Store, Dirs: c.Dirs, Current: "1.0.0",
		Log: quiet(), BaseURL: g.srv.URL,
	}
	restarted.reconcile(ctx)

	if got := restarted.Status(); got.State != StateReady {
		t.Fatalf("state after restart = %q, want %q", got.State, StateReady)
	}
	if got := restarted.Status(); got.Latest != "1.1.0" {
		t.Errorf("latest after restart = %q, want 1.1.0", got.Latest)
	}
}

func TestATamperedStagedInstallerIsDiscardedOnStartup(t *testing.T) {
	ctx := context.Background()
	g := newFakeGitHub(t, "1.1.0")
	c := newChecker(t, g, "1.0.0")
	if got := c.Once(ctx); got.State != StateReady {
		t.Fatalf("setup: state = %q (%s)", got.State, got.Err)
	}

	staged := filepath.Join(c.Dirs.UpdateDir(), SetupAsset("1.1.0"))
	if err := os.WriteFile(staged, []byte("something a local user would rather run"), 0o600); err != nil {
		t.Fatal(err)
	}

	restarted := &Checker{Store: c.Store, Dirs: c.Dirs, Current: "1.0.0", Log: quiet()}
	restarted.reconcile(ctx)

	if got := restarted.Status(); got.State == StateReady {
		t.Error("a swapped installer was still reported as ready to run")
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Errorf("the swapped installer is still on disk: %v", err)
	}
}

func TestAnAppliedUpdateTidiesUpAfterItself(t *testing.T) {
	ctx := context.Background()
	g := newFakeGitHub(t, "1.1.0")
	c := newChecker(t, g, "1.0.0")
	if got := c.Once(ctx); got.State != StateReady {
		t.Fatalf("setup: state = %q (%s)", got.State, got.Err)
	}
	put(t, c, store.KeyUpdatesInstallAt, time.Now().Format(time.RFC3339))

	// The installer ran: the service comes back as the new version.
	upgraded := &Checker{Store: c.Store, Dirs: c.Dirs, Current: "1.1.0", Log: quiet()}
	upgraded.reconcile(ctx)

	if got := upgraded.Status(); got.State != StateIdle {
		t.Errorf("state after a successful upgrade = %q, want %q", got.State, StateIdle)
	}
	entries, err := os.ReadDir(c.Dirs.UpdateDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("the applied installer was left behind: %s", e.Name())
	}

	all, _ := c.Store.AllSettings(ctx)
	for _, k := range []string{
		store.KeyUpdatesReadyVer, store.KeyUpdatesReadySHA256, store.KeyUpdatesInstallAt,
	} {
		if all[k] != "" {
			t.Errorf("%s = %q after a successful upgrade, want empty", k, all[k])
		}
	}
}

func TestAnInstallThatDidNotTakeIsReported(t *testing.T) {
	ctx := context.Background()
	g := newFakeGitHub(t, "1.1.0")
	c := newChecker(t, g, "1.0.0")
	if got := c.Once(ctx); got.State != StateReady {
		t.Fatalf("setup: state = %q (%s)", got.State, got.Err)
	}
	put(t, c, store.KeyUpdatesInstallAt, time.Now().Format(time.RFC3339))

	// The installer ran and the version did not change. Without this the only
	// evidence is a log file nobody would think to open.
	same := &Checker{Store: c.Store, Dirs: c.Dirs, Current: "1.0.0", Log: quiet()}
	same.reconcile(ctx)

	got := same.Status()
	if got.State != StateReady {
		t.Errorf("state = %q, want the update still offered as %q", got.State, StateReady)
	}
	if got.Err == "" {
		t.Error("a silent install that did nothing was not reported anywhere")
	}
}

func TestInstallRefusesWhenNothingIsStaged(t *testing.T) {
	ctx := context.Background()
	c := newChecker(t, nil, "1.0.0")

	err := c.Install(ctx)
	if err == nil {
		t.Fatal("Install succeeded with nothing staged")
	}
	if Supported && err != ErrNotReady {
		t.Errorf("Install = %v, want %v", err, ErrNotReady)
	}
}

func TestInstallRefusesAnInstallerThatChangedUnderIt(t *testing.T) {
	if !Supported {
		t.Skip("only the Windows build can apply an update")
	}
	ctx := context.Background()
	g := newFakeGitHub(t, "1.1.0")
	c := newChecker(t, g, "1.0.0")
	if got := c.Once(ctx); got.State != StateReady {
		t.Fatalf("setup: state = %q (%s)", got.State, got.Err)
	}

	// The staging directory has an ACL that should make this impossible for
	// anyone but SYSTEM and Administrators. The re-check exists because "should
	// be impossible" is not the standard for what LocalSystem executes.
	staged := filepath.Join(c.Dirs.UpdateDir(), SetupAsset("1.1.0"))
	if err := os.WriteFile(staged, []byte("not the file that was checked"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.Install(ctx); err == nil {
		t.Fatal("Install ran an installer whose hash had changed")
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Error("the rejected installer was left on disk")
	}
	if got := c.Status(); got.State == StateInstalling {
		t.Error("state moved to installing despite the refusal")
	}
}

func TestParseSumsReadsTheReleaseFormat(t *testing.T) {
	good := hashOf(installerBody)
	body := "" +
		good + "  LocalMonitor-Setup-1.1.0.exe\n" +
		hashOf([]byte("x")) + "  monitor-service.exe\n" +
		hashOf([]byte("y")) + "  monitor-gui.exe\n"

	if got, ok := parseSums(body, "LocalMonitor-Setup-1.1.0.exe"); !ok || got != good {
		t.Errorf("parseSums = %q, %v; want %q, true", got, ok, good)
	}
	if _, ok := parseSums(body, "LocalMonitor-Setup-9.9.9.exe"); ok {
		t.Error("parseSums found a file the checksums do not list")
	}
	if _, ok := parseSums("not a checksum  file.exe\n", "file.exe"); ok {
		t.Error("parseSums accepted something that is not a hash")
	}
	if _, ok := parseSums("", "file.exe"); ok {
		t.Error("parseSums accepted an empty checksum file")
	}
}

func put(t *testing.T, c *Checker, key, value string) {
	t.Helper()
	if err := c.Store.PutSettings(context.Background(), map[string]string{key: value}); err != nil {
		t.Fatal(err)
	}
}
