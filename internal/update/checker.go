package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pwshehan/device-status-monitor/internal/appdir"
	"github.com/pwshehan/device-status-monitor/internal/store"
)

// State is where one installed copy has got to with the newest release.
type State string

const (
	// StateIdle is the ordinary case: nothing newer exists.
	StateIdle State = "idle"

	// StateAvailable means a newer release exists but nothing has been fetched
	// — either automatic downloads are off, or the download has not run yet.
	StateAvailable State = "available"

	StateDownloading State = "downloading"

	// StateReady means the installer is on disk and its hash matches what the
	// release published. This is where it stops on its own.
	StateReady State = "ready"

	// StateInstalling means the installer has been launched. The service is
	// about to be stopped by it, so this state is rarely observed for long.
	StateInstalling State = "installing"

	// StateError means the last attempt failed. The message says how.
	StateError State = "error"
)

// Defaults for the checking loop.
const (
	// DefaultInterval is how often GitHub is asked. Six hours is far more often
	// than releases happen; it is this short only so that a machine which was
	// off for a while notices soon after coming back.
	DefaultInterval = 6 * time.Hour

	// DefaultStartupDelay keeps the check out of the way of everything else a
	// machine does in its first couple of minutes. Nothing about an update is
	// urgent enough to compete with the first round of probes.
	DefaultStartupDelay = 2 * time.Minute
)

// Status is what the dashboard is told.
type Status struct {
	// Supported is false where an update could not be applied even if one
	// existed: a non-Windows build, or a binary with no version stamped into
	// it. The UI hides the whole section rather than offering something that
	// would fail.
	Supported bool

	Enabled      bool
	AutoDownload bool

	Current string
	State   State

	Latest      string
	NotesURL    string
	PublishedAt time.Time

	// SizeBytes and DownloadedBytes are only meaningful while downloading.
	SizeBytes       int64
	DownloadedBytes int64

	LastCheckedAt time.Time
	Err           string
}

// Checker keeps one installed copy aware of the newest release.
//
// The shape follows rollup.Janitor: exported configuration, no constructor, a
// Run that does one pass at startup and then ticks, and an exported Once so a
// test or a handler can force a pass without waiting for the ticker.
type Checker struct {
	Store   *store.Store
	Dirs    appdir.Dirs
	Current string

	Interval     time.Duration
	StartupDelay time.Duration
	Log          *slog.Logger

	// Now is injectable so tests can be explicit about time.
	Now func() time.Time

	// BaseURL points at something other than api.github.com. Tests only; there
	// is no setting for it, because "where does this machine take its updates
	// from" is not a question a monitoring tool's settings page should answer.
	BaseURL string

	// passMu serialises checks and downloads against each other: two passes
	// writing the same staged file would race over it.
	passMu sync.Mutex

	mu     sync.RWMutex
	status Status
	rel    release

	// Download progress, written from the copy loop.
	dlDone  atomic.Int64
	dlTotal atomic.Int64
}

// Run reconciles what the last run left behind, then checks on the interval.
func (c *Checker) Run(ctx context.Context) {
	c.reconcile(ctx)

	select {
	case <-ctx.Done():
		return
	case <-time.After(c.startupDelay()):
	}
	c.Once(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.nextInterval()):
			c.Once(ctx)
		}
	}
}

// nextInterval spreads checks out. Every machine installed from the same
// installer starts its service at the same point in a reboot, and without this
// they would all ask GitHub in lockstep for ever — the same reason the
// scheduler spreads a device's first probe across its interval.
func (c *Checker) nextInterval() time.Duration {
	interval := c.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	return interval + rand.N(interval/10)
}

func (c *Checker) startupDelay() time.Duration {
	if c.StartupDelay > 0 {
		return c.StartupDelay
	}
	if c.StartupDelay < 0 {
		return 0
	}
	return DefaultStartupDelay
}

// Status reports the current state.
func (c *Checker) Status() Status {
	c.mu.RLock()
	s := c.status
	c.mu.RUnlock()

	s.Supported = c.supported()
	s.Current = c.Current
	if s.State == StateDownloading {
		s.DownloadedBytes = c.dlDone.Load()
		s.SizeBytes = c.dlTotal.Load()
	}
	if s.State == "" {
		s.State = StateIdle
	}
	return s
}

// canCheck reports whether asking about updates makes sense.
//
// A dev-mode run is excluded because its version is not a release version, and
// so is any build whose version this cannot parse: comparing against a version
// it cannot reason about would be a guess, and the thing being guessed at
// replaces someone's binaries.
func (c *Checker) canCheck() bool {
	if c.Dirs.Dev {
		return false
	}
	_, err := ParseVersion(c.Current)
	return err == nil
}

// supported reports whether an update could actually be applied.
//
// Checking and applying are separated because they need different things:
// knowing a release exists needs only a version to compare, while running the
// installer needs to be on the platform the installer is for.
func (c *Checker) supported() bool { return Supported && c.canCheck() }

// reconcile works out, at startup, what the previous run left behind.
//
// This is what closes the loop. An update that was applied leaves an installer
// staged for a version this binary now *is*, and that is the only signal the
// new process gets that the old one's work succeeded.
func (c *Checker) reconcile(ctx context.Context) {
	log := c.logger()

	all, err := c.Store.AllSettings(ctx)
	if err != nil {
		log.Warn("read update state", "err", err)
		return
	}
	c.setStatus(func(s *Status) {
		s.Latest = all[store.KeyUpdatesLatest]
		s.NotesURL = all[store.KeyUpdatesNotesURL]
		s.PublishedAt = parseTime(all[store.KeyUpdatesPublishedAt])
		s.LastCheckedAt = parseTime(all[store.KeyUpdatesLastChecked])
		s.State = StateIdle
	})

	ready := all[store.KeyUpdatesReadyVer]
	installStarted := all[store.KeyUpdatesInstallAt] != ""

	// Nothing staged: the ordinary case, and the case after a clean upgrade
	// that already tidied up.
	if ready == "" {
		if installStarted {
			c.forget(ctx)
		}
		c.offerIfNewer(all[store.KeyUpdatesLatest])
		return
	}

	// The staged installer is for a version this binary is already at or past.
	// It did its job — or a newer one arrived another way.
	if !Newer(c.Current, ready) {
		log.Info("update applied, clearing staged installer", "version", c.Current)
		c.discardStaged(ctx)
		return
	}

	// Still older than what is staged, but an install was launched. The
	// installer ran and the version did not change, so it failed and left no
	// other trace anyone would think to look for.
	if installStarted {
		log.Warn("the previous update did not install",
			"staged", ready, "still_running", c.Current,
			"log", filepath.Join(c.Dirs.LogDir(), installLogName))
	}

	path := c.stagedPath(ready)
	want := all[store.KeyUpdatesReadySHA256]
	got, err := sha256File(path)
	switch {
	case err != nil:
		log.Info("staged installer is gone, will fetch it again", "version", ready)
		c.discardStaged(ctx)
	case got != want:
		log.Warn("staged installer no longer matches its checksum, discarding",
			"version", ready, "want", want, "got", got)
		c.discardStaged(ctx)
	default:
		c.setStatus(func(s *Status) {
			s.State = StateReady
			s.Latest = ready
			s.Err = ""
		})
		if installStarted {
			c.save(ctx, map[string]string{store.KeyUpdatesInstallAt: ""})
			c.setStatus(func(s *Status) {
				s.Err = "the last install did not complete; see " + installLogName
			})
		}
	}
}

// offerIfNewer restores an "available" state from what the last check recorded,
// so a restart does not forget that an update exists until the next check.
func (c *Checker) offerIfNewer(latest string) {
	if latest == "" || !Newer(c.Current, latest) {
		return
	}
	c.setStatus(func(s *Status) { s.State = StateAvailable })
}

// Once runs a single check.
func (c *Checker) Once(ctx context.Context) Status {
	c.passMu.Lock()
	defer c.passMu.Unlock()

	log := c.logger()

	policy, err := c.Store.UpdatePolicy(ctx)
	if err != nil {
		log.Warn("read update settings, using defaults", "err", err)
	}
	c.setStatus(func(s *Status) {
		s.Enabled = policy.Enabled
		s.AutoDownload = policy.AutoDownload
	})
	if !policy.Enabled || !c.canCheck() {
		return c.Status()
	}

	all, err := c.Store.AllSettings(ctx)
	if err != nil {
		log.Warn("read update state", "err", err)
		return c.Status()
	}

	rel, etag, err := c.client().latest(ctx, all[store.KeyUpdatesETag])
	now := c.now()
	switch {
	case errors.Is(err, errNoNews):
		// A 304, or nothing published. Both mean the recorded state still
		// stands; only the timestamp moves.
		c.save(ctx, map[string]string{
			store.KeyUpdatesLastChecked: now.Format(time.RFC3339),
			store.KeyUpdatesETag:        etag,
		})
		c.setStatus(func(s *Status) { s.LastCheckedAt = now; s.Err = "" })
		return c.Status()

	case err != nil:
		// A machine with no route to the internet is a supported way to run
		// this. A failed check is a log line and a note on the page, never an
		// alert, and it never disturbs an update already staged.
		if ctx.Err() == nil {
			log.Warn("check for updates", "err", err)
		}
		c.save(ctx, map[string]string{
			store.KeyUpdatesLastChecked: now.Format(time.RFC3339),
			store.KeyUpdatesError:       err.Error(),
		})
		c.setStatus(func(s *Status) { s.LastCheckedAt = now; s.Err = err.Error() })
		return c.Status()
	}

	latest := rel.Version()
	c.mu.Lock()
	c.rel = rel
	c.mu.Unlock()

	c.save(ctx, map[string]string{
		store.KeyUpdatesLastChecked: now.Format(time.RFC3339),
		store.KeyUpdatesETag:        etag,
		store.KeyUpdatesLatest:      latest,
		store.KeyUpdatesNotesURL:    rel.HTMLURL,
		store.KeyUpdatesPublishedAt: rel.PublishedAt.Format(time.RFC3339),
		store.KeyUpdatesError:       "",
	})
	c.setStatus(func(s *Status) {
		s.LastCheckedAt = now
		s.Latest = latest
		s.NotesURL = rel.HTMLURL
		s.PublishedAt = rel.PublishedAt
		s.Err = ""
	})

	if !Newer(c.Current, latest) {
		// Includes the case where a bad release was pulled and the latest is
		// now older than something already staged, so the staged one goes.
		if all[store.KeyUpdatesReadyVer] != "" {
			c.discardStaged(ctx)
		}
		c.setStatus(func(s *Status) { s.State = StateIdle })
		return c.Status()
	}

	if all[store.KeyUpdatesReadyVer] == latest && c.Status().State == StateReady {
		return c.Status()
	}

	log.Info("a newer release is available", "current", c.Current, "latest", latest)
	c.setStatus(func(s *Status) { s.State = StateAvailable })

	if policy.AutoDownload {
		if err := c.stage(ctx, rel); err != nil && ctx.Err() == nil {
			log.Warn("stage update", "version", latest, "err", err)
		}
	}
	return c.Status()
}

// Download stages the installer for the newest known release.
//
// Used when automatic downloads are off and someone has asked for it. It checks
// first if nothing is known yet, so it works on a machine that has just started.
func (c *Checker) Download(ctx context.Context) error {
	c.mu.RLock()
	rel := c.rel
	c.mu.RUnlock()

	if rel.TagName == "" {
		c.Once(ctx)
		c.mu.RLock()
		rel = c.rel
		c.mu.RUnlock()
	}
	if rel.TagName == "" || !Newer(c.Current, rel.Version()) {
		return fmt.Errorf("there is no newer release to download")
	}

	c.passMu.Lock()
	defer c.passMu.Unlock()
	return c.stage(ctx, rel)
}

// stage downloads the installer and checks it against the release's SHA256SUMS.
// The caller holds passMu.
func (c *Checker) stage(ctx context.Context, rel release) error {
	version := rel.Version()
	name := SetupAsset(version)

	fail := func(err error) error {
		c.save(ctx, map[string]string{store.KeyUpdatesError: err.Error()})
		c.setStatus(func(s *Status) { s.State = StateError; s.Err = err.Error() })
		return err
	}

	setup, ok := rel.asset(name)
	if !ok {
		return fail(fmt.Errorf("release %s has no %s", version, name))
	}
	sums, ok := rel.asset(SumsAsset)
	if !ok {
		// Refusing rather than downloading it anyway: an installer this cannot
		// check is one it has no business handing to LocalSystem.
		return fail(fmt.Errorf("release %s has no %s, so its installer cannot be checked", version, SumsAsset))
	}

	dir := c.Dirs.UpdateDir()
	if err := ensureStagingDir(dir); err != nil {
		return fail(err)
	}

	cl := c.client()
	want, err := cl.fetchSum(ctx, sums.URL, name)
	if err != nil {
		return fail(err)
	}

	c.dlDone.Store(0)
	c.dlTotal.Store(setup.Size)
	c.setStatus(func(s *Status) { s.State = StateDownloading; s.Err = "" })

	dst := filepath.Join(dir, name)
	if err := cl.download(ctx, setup.URL, dst, want, &c.dlDone); err != nil {
		if ctx.Err() != nil {
			// Shutting down, not failing.
			c.setStatus(func(s *Status) { s.State = StateAvailable })
			return err
		}
		return fail(err)
	}

	c.save(ctx, map[string]string{
		store.KeyUpdatesReadyVer:    version,
		store.KeyUpdatesReadySHA256: want,
		store.KeyUpdatesError:       "",
	})
	c.setStatus(func(s *Status) { s.State = StateReady; s.Latest = version; s.Err = "" })
	c.sweep(dir, name)
	c.logger().Info("update staged and checked", "version", version, "file", dst)
	return nil
}

// ErrNotReady is returned when there is nothing staged to install.
var ErrNotReady = errors.New("no checked installer is ready")

// installLogName is what the silent installer writes. Named here because the
// message pointing at it is more useful than the file.
const installLogName = "update-install.log"

// Install runs the staged installer and returns immediately.
//
// It returns before the installer starts, on purpose: the installer's first act
// is to stop this service, so anything waiting for a reply after that point
// would never get one. The caller answers the request, and a moment later the
// service goes away and comes back as the new version.
func (c *Checker) Install(ctx context.Context) error {
	if !c.supported() {
		return fmt.Errorf("this build cannot apply updates")
	}

	c.passMu.Lock()
	defer c.passMu.Unlock()

	all, err := c.Store.AllSettings(ctx)
	if err != nil {
		return err
	}
	version := all[store.KeyUpdatesReadyVer]
	want := all[store.KeyUpdatesReadySHA256]
	if version == "" || want == "" {
		return ErrNotReady
	}
	path := c.stagedPath(version)

	// Re-checked here rather than trusted from the download. Between staging
	// and this call the file has been sitting on disk, and this is the moment
	// it stops being data and becomes something LocalSystem executes.
	got, err := sha256File(path)
	if err != nil {
		c.discardStaged(ctx)
		return fmt.Errorf("staged installer is unreadable: %w", err)
	}
	if got != want {
		c.discardStaged(ctx)
		err := fmt.Errorf("staged installer no longer matches its checksum")
		c.setStatus(func(s *Status) { s.State = StateError; s.Err = err.Error() })
		return err
	}

	now := c.now()
	c.save(ctx, map[string]string{store.KeyUpdatesInstallAt: now.Format(time.RFC3339)})
	c.setStatus(func(s *Status) { s.State = StateInstalling; s.Err = "" })

	logFile := filepath.Join(c.Dirs.LogDir(), installLogName)
	c.logger().Info("applying update", "version", version, "installer", path, "log", logFile)

	go func() {
		// Long enough for the API to have answered the request that asked for
		// this. Everything after the installer starts happens to a process that
		// is being shut down.
		time.Sleep(2 * time.Second)
		if err := launchInstaller(path, logFile); err != nil {
			c.logger().Error("launch installer", "installer", path, "err", err)
			c.setStatus(func(s *Status) { s.State = StateError; s.Err = err.Error() })
			c.save(context.WithoutCancel(ctx), map[string]string{
				store.KeyUpdatesInstallAt: "",
				store.KeyUpdatesError:     err.Error(),
			})
		}
	}()
	return nil
}

// stagedPath is where an installer for a version lives.
func (c *Checker) stagedPath(version string) string {
	return filepath.Join(c.Dirs.UpdateDir(), SetupAsset(version))
}

// discardStaged removes the staged installer and forgets it.
func (c *Checker) discardStaged(ctx context.Context) {
	dir := c.Dirs.UpdateDir()
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				c.logger().Warn("remove staged file", "file", e.Name(), "err", err)
			}
		}
	}
	c.forget(ctx)
	c.setStatus(func(s *Status) {
		if s.State == StateReady || s.State == StateInstalling {
			s.State = StateIdle
		}
	})
}

// forget clears the staged-installer bookkeeping.
func (c *Checker) forget(ctx context.Context) {
	c.save(ctx, map[string]string{
		store.KeyUpdatesReadyVer:    "",
		store.KeyUpdatesReadySHA256: "",
		store.KeyUpdatesInstallAt:   "",
	})
}

// sweep removes anything in the staging directory that is not the installer
// just staged: an installer for a version that has been superseded, or a
// .partial left by a download that was cut off.
func (c *Checker) sweep(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() == keep {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			c.logger().Warn("remove superseded staged file", "file", e.Name(), "err", err)
		}
	}
}

func (c *Checker) client() *client {
	return newClient(c.Current, c.BaseURL)
}

// save writes state, dropping empty values as deletions rather than storing
// blanks. Uses a context that survives shutdown for the same reason the
// notifier does: a record of work already done must not be lost because the
// service is stopping.
func (c *Checker) save(ctx context.Context, kv map[string]string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := c.Store.PutSettings(ctx, kv); err != nil {
		c.logger().Warn("save update state", "err", err)
	}
}

func (c *Checker) setStatus(fn func(*Status)) {
	c.mu.Lock()
	fn(&c.status)
	c.mu.Unlock()
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) logger() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// parseTime reads a stored RFC3339 stamp, or the zero time.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
