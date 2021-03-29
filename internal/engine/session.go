package engine

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/ooni/probe-cli/v3/internal/engine/atomicx"
	"github.com/ooni/probe-cli/v3/internal/engine/geolocate"
	"github.com/ooni/probe-cli/v3/internal/engine/internal/platform"
	"github.com/ooni/probe-cli/v3/internal/engine/internal/sessionresolver"
	"github.com/ooni/probe-cli/v3/internal/engine/internal/tunnel"
	"github.com/ooni/probe-cli/v3/internal/engine/kvstore"
	"github.com/ooni/probe-cli/v3/internal/engine/model"
	"github.com/ooni/probe-cli/v3/internal/engine/netx"
	"github.com/ooni/probe-cli/v3/internal/engine/netx/bytecounter"
	"github.com/ooni/probe-cli/v3/internal/engine/probeservices"
	"github.com/ooni/probe-cli/v3/internal/engine/resources"
	"github.com/ooni/probe-cli/v3/internal/engine/resourcesmanager"
	"github.com/ooni/probe-cli/v3/internal/version"
)

// SessionConfig contains the Session config
type SessionConfig struct {
	AssetsDir              string
	AvailableProbeServices []model.Service
	KVStore                KVStore
	Logger                 model.Logger
	ProxyURL               *url.URL
	SoftwareName           string
	SoftwareVersion        string
	TempDir                string
	TorArgs                []string
	TorBinary              string
}

// Session is a measurement session.
type Session struct {
	assetsDir                string
	availableProbeServices   []model.Service
	availableTestHelpers     map[string][]model.Service
	byteCounter              *bytecounter.Counter
	httpDefaultTransport     netx.HTTPRoundTripper
	kvStore                  model.KeyValueStore
	location                 *geolocate.Results
	logger                   model.Logger
	proxyURL                 *url.URL
	queryProbeServicesCount  *atomicx.Int64
	resolver                 *sessionresolver.Resolver
	selectedProbeServiceHook func(*model.Service)
	selectedProbeService     *model.Service
	softwareName             string
	softwareVersion          string
	tempDir                  string
	torArgs                  []string
	torBinary                string
	tunnelMu                 sync.Mutex
	tunnelName               string
	tunnel                   tunnel.Tunnel

	// mu provides mutual exclusion.
	mu sync.Mutex

	// testLookupLocationContext is a an optional hook for testing
	// allowing us to mock LookupLocationContext.
	testLookupLocationContext func(ctx context.Context) (*geolocate.Results, error)

	// testMaybeLookupBackendsContext is an optional hook for testing
	// allowing us to mock MaybeLookupBackendsContext.
	testMaybeLookupBackendsContext func(ctx context.Context) error

	// testMaybeLookupLocationContext is an optional hook for testing
	// allowing us to mock MaybeLookupLocationContext.
	testMaybeLookupLocationContext func(ctx context.Context) error

	// testNewProbeServicesClientForCheckIn is an optional hook for testing
	// allowing us to mock NewProbeServicesClient when calling CheckIn.
	testNewProbeServicesClientForCheckIn func(ctx context.Context) (
		sessionProbeServicesClientForCheckIn, error)
}

// sessionProbeServicesClientForCheckIn returns the probe services
// client that we should be using for performing the check-in.
type sessionProbeServicesClientForCheckIn interface {
	CheckIn(ctx context.Context, config model.CheckInConfig) (*model.CheckInInfo, error)
}

// NewSession creates a new session or returns an error
func NewSession(config SessionConfig) (*Session, error) {
	if config.AssetsDir == "" {
		return nil, errors.New("AssetsDir is empty")
	}
	if config.Logger == nil {
		return nil, errors.New("Logger is empty")
	}
	if config.SoftwareName == "" {
		return nil, errors.New("SoftwareName is empty")
	}
	if config.SoftwareVersion == "" {
		return nil, errors.New("SoftwareVersion is empty")
	}
	if config.KVStore == nil {
		config.KVStore = kvstore.NewMemoryKeyValueStore()
	}
	// Implementation note: if config.TempDir is empty, then Go will
	// use the temporary directory on the current system. This should
	// work on Desktop. We tested that it did also work on iOS, but
	// we have also seen on 2020-06-10 that it does not work on Android.
	tempDir, err := ioutil.TempDir(config.TempDir, "ooniengine")
	if err != nil {
		return nil, err
	}
	sess := &Session{
		assetsDir:               config.AssetsDir,
		availableProbeServices:  config.AvailableProbeServices,
		byteCounter:             bytecounter.New(),
		kvStore:                 config.KVStore,
		logger:                  config.Logger,
		proxyURL:                config.ProxyURL,
		queryProbeServicesCount: atomicx.NewInt64(),
		softwareName:            config.SoftwareName,
		softwareVersion:         config.SoftwareVersion,
		tempDir:                 tempDir,
		torArgs:                 config.TorArgs,
		torBinary:               config.TorBinary,
	}
	httpConfig := netx.Config{
		ByteCounter:  sess.byteCounter,
		BogonIsError: true,
		Logger:       sess.logger,
		ProxyURL:     config.ProxyURL,
	}
	sess.resolver = &sessionresolver.Resolver{
		ByteCounter: sess.byteCounter,
		KVStore:     config.KVStore,
		Logger:      sess.logger,
		ProxyURL:    config.ProxyURL,
	}
	httpConfig.FullResolver = sess.resolver
	sess.httpDefaultTransport = netx.NewHTTPTransport(httpConfig)
	return sess, nil
}

// ASNDatabasePath returns the path where the ASN database path should
// be if you have called s.FetchResourcesIdempotent.
func (s *Session) ASNDatabasePath() string {
	return filepath.Join(s.assetsDir, resources.ASNDatabaseName)
}

// KibiBytesReceived accounts for the KibiBytes received by the HTTP clients
// managed by this session so far, including experiments.
func (s *Session) KibiBytesReceived() float64 {
	return s.byteCounter.KibiBytesReceived()
}

// KibiBytesSent is like KibiBytesReceived but for the bytes sent.
func (s *Session) KibiBytesSent() float64 {
	return s.byteCounter.KibiBytesSent()
}

// CheckIn calls the check-in API. The input arguments MUST NOT
// be nil. Before querying the API, this function will ensure
// that the config structure does not contain any field that
// SHOULD be initialized and is not initialized. Whenever there
// is a field that is not initialized, we will attempt to set
// a reasonable default value for such a field. This list describes
// the current defaults we'll choose:
//
// - Platform: if empty, set to Session.Platform();
//
// - ProbeASN: if empty, set to Session.ProbeASNString();
//
// - ProbeCC: if empty, set to Session.ProbeCC();
//
// - RunType: if empty, set to "timed";
//
// - SoftwareName: if empty, set to Session.SoftwareName();
//
// - SoftwareVersion: if empty, set to Session.SoftwareVersion();
//
// - WebConnectivity.CategoryCodes: if nil, we will allocate
// an empty array (the API does not like nil).
//
// Because we MAY need to know the current ASN and CC, this
// function MAY call MaybeLookupLocationContext.
//
// The return value is either the check-in response or an error.
func (s *Session) CheckIn(
	ctx context.Context, config *model.CheckInConfig) (*model.CheckInInfo, error) {
	if err := s.maybeLookupLocationContext(ctx); err != nil {
		return nil, err
	}
	client, err := s.newProbeServicesClientForCheckIn(ctx)
	if err != nil {
		return nil, err
	}
	if config.Platform == "" {
		config.Platform = s.Platform()
	}
	if config.ProbeASN == "" {
		config.ProbeASN = s.ProbeASNString()
	}
	if config.ProbeCC == "" {
		config.ProbeCC = s.ProbeCC()
	}
	if config.RunType == "" {
		config.RunType = "timed" // most conservative choice
	}
	if config.SoftwareName == "" {
		config.SoftwareName = s.SoftwareName()
	}
	if config.SoftwareVersion == "" {
		config.SoftwareVersion = s.SoftwareVersion()
	}
	if config.WebConnectivity.CategoryCodes == nil {
		config.WebConnectivity.CategoryCodes = []string{}
	}
	return client.CheckIn(ctx, *config)
}

// maybeLookupLocationContext is a wrapper for MaybeLookupLocationContext that calls
// the configurable testMaybeLookupLocationContext mock, if configured, and the
// real MaybeLookupLocationContext API otherwise.
func (s *Session) maybeLookupLocationContext(ctx context.Context) error {
	if s.testMaybeLookupLocationContext != nil {
		return s.testMaybeLookupLocationContext(ctx)
	}
	return s.MaybeLookupLocationContext(ctx)
}

// newProbeServicesClientForCheckIn is a wrapper for NewProbeServicesClientForCheckIn
// that calls the configurable testNewProbeServicesClientForCheckIn mock, if
// configured, and the real NewProbeServicesClient API otherwise.
func (s *Session) newProbeServicesClientForCheckIn(
	ctx context.Context) (sessionProbeServicesClientForCheckIn, error) {
	if s.testNewProbeServicesClientForCheckIn != nil {
		return s.testNewProbeServicesClientForCheckIn(ctx)
	}
	client, err := s.NewProbeServicesClient(ctx)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// Close ensures that we close all the idle connections that the HTTP clients
// we are currently using may have created. It will also remove the temp dir
// that contains data from this session. Not calling this function may likely
// cause memory leaks in your application because of open idle connections,
// as well as excessive usage of disk space.
func (s *Session) Close() error {
	// TODO(bassosimone): introduce a sync.Once to make this method idempotent.
	s.httpDefaultTransport.CloseIdleConnections()
	s.resolver.CloseIdleConnections()
	s.logger.Infof("%s", s.resolver.Stats())
	if s.tunnel != nil {
		s.tunnel.Stop()
	}
	return os.RemoveAll(s.tempDir)
}

// CountryDatabasePath is like ASNDatabasePath but for the country DB path.
func (s *Session) CountryDatabasePath() string {
	return filepath.Join(s.assetsDir, resources.CountryDatabaseName)
}

// GetTestHelpersByName returns the available test helpers that
// use the specified name, or false if there's none.
func (s *Session) GetTestHelpersByName(name string) ([]model.Service, bool) {
	defer s.mu.Unlock()
	s.mu.Lock()
	services, ok := s.availableTestHelpers[name]
	return services, ok
}

// DefaultHTTPClient returns the session's default HTTP client.
func (s *Session) DefaultHTTPClient() *http.Client {
	return &http.Client{Transport: s.httpDefaultTransport}
}

// KeyValueStore returns the configured key-value store.
func (s *Session) KeyValueStore() model.KeyValueStore {
	return s.kvStore
}

// Logger returns the logger used by the session.
func (s *Session) Logger() model.Logger {
	return s.logger
}

// MaybeLookupLocation is a caching location lookup call.
func (s *Session) MaybeLookupLocation() error {
	return s.MaybeLookupLocationContext(context.Background())
}

// MaybeLookupBackends is a caching OONI backends lookup call.
func (s *Session) MaybeLookupBackends() error {
	return s.MaybeLookupBackendsContext(context.Background())
}

// ErrAlreadyUsingProxy indicates that we cannot create a tunnel with
// a specific name because we already configured a proxy.
var ErrAlreadyUsingProxy = errors.New(
	"session: cannot create a new tunnel of this kind: we are already using a proxy",
)

// MaybeStartTunnel starts the requested tunnel.
//
// This function silently succeeds if we're already using a tunnel with
// the same name or if the requested tunnel name is the empty string. This
// function fails, tho, when we already have a proxy or a tunnel with
// another name and we try to open a tunnel. This function of course also
// fails if we cannot start the requested tunnel. All in all, if you request
// for a tunnel name that is not the empty string and you get a nil error,
// you can be confident that session.ProxyURL() gives you the tunnel URL.
//
// The tunnel will be closed by session.Close().
func (s *Session) MaybeStartTunnel(ctx context.Context, name string) error {
	// TODO(bassosimone): see if we can unify tunnelMu and mu.
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	if s.tunnel != nil && s.tunnelName == name {
		// We've been asked more than once to start the same tunnel.
		return nil
	}
	if s.proxyURL != nil && name == "" {
		// The user configured a proxy and here we're not actually trying
		// to start any tunnel since `name` is empty.
		return nil
	}
	if s.proxyURL != nil || s.tunnel != nil {
		// We already have a proxy or we have a different tunnel. Because a tunnel
		// sets a proxy, the second check for s.tunnel is for robustness.
		return ErrAlreadyUsingProxy
	}
	tunnel, err := tunnel.Start(ctx, tunnel.Config{
		Name:    name,
		Session: s,
	})
	if err != nil {
		s.logger.Warnf("cannot start tunnel: %+v", err)
		return err
	}
	// Implementation note: tunnel _may_ be NIL here if name is ""
	if tunnel == nil {
		return nil
	}
	s.tunnelName = name
	s.tunnel = tunnel
	s.proxyURL = tunnel.SOCKS5ProxyURL()
	return nil
}

// NewExperimentBuilder returns a new experiment builder
// for the experiment with the given name, or an error if
// there's no such experiment with the given name
func (s *Session) NewExperimentBuilder(name string) (*ExperimentBuilder, error) {
	return newExperimentBuilder(s, name)
}

// NewProbeServicesClient creates a new client for talking with the
// OONI probe services. This function will benchmark the available
// probe services, and select the fastest. In case all probe services
// seem to be down, we try again applying circumvention tactics.
// This function will fail IMMEDIATELY if given a cancelled context.
func (s *Session) NewProbeServicesClient(ctx context.Context) (*probeservices.Client, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err() // helps with testing
	}
	if err := s.maybeLookupBackendsContext(ctx); err != nil {
		return nil, err
	}
	if err := s.maybeLookupLocationContext(ctx); err != nil {
		return nil, err
	}
	if s.selectedProbeServiceHook != nil {
		s.selectedProbeServiceHook(s.selectedProbeService)
	}
	return probeservices.NewClient(s, *s.selectedProbeService)
}

// NewSubmitter creates a new submitter instance.
func (s *Session) NewSubmitter(ctx context.Context) (Submitter, error) {
	psc, err := s.NewProbeServicesClient(ctx)
	if err != nil {
		return nil, err
	}
	return probeservices.NewSubmitter(psc, s.Logger()), nil
}

// NewOrchestraClient creates a new orchestra client. This client is registered
// and logged in with the OONI orchestra. An error is returned on failure.
func (s *Session) NewOrchestraClient(ctx context.Context) (model.ExperimentOrchestraClient, error) {
	clnt, err := s.NewProbeServicesClient(ctx)
	if err != nil {
		return nil, err
	}
	return s.initOrchestraClient(ctx, clnt, clnt.MaybeLogin)
}

// Platform returns the current platform. The platform is one of:
//
// - android
// - ios
// - linux
// - macos
// - windows
// - unknown
//
// When running on the iOS simulator, the returned platform is
// macos rather than ios if CGO is disabled. This is a known issue,
// that however should have a very limited impact.
func (s *Session) Platform() string {
	return platform.Name()
}

// ProbeASNString returns the probe ASN as a string.
func (s *Session) ProbeASNString() string {
	return fmt.Sprintf("AS%d", s.ProbeASN())
}

// ProbeASN returns the probe ASN as an integer.
func (s *Session) ProbeASN() uint {
	defer s.mu.Unlock()
	s.mu.Lock()
	asn := geolocate.DefaultProbeASN
	if s.location != nil {
		asn = s.location.ASN
	}
	return asn
}

// ProbeCC returns the probe CC.
func (s *Session) ProbeCC() string {
	defer s.mu.Unlock()
	s.mu.Lock()
	cc := geolocate.DefaultProbeCC
	if s.location != nil {
		cc = s.location.CountryCode
	}
	return cc
}

// ProbeNetworkName returns the probe network name.
func (s *Session) ProbeNetworkName() string {
	defer s.mu.Unlock()
	s.mu.Lock()
	nn := geolocate.DefaultProbeNetworkName
	if s.location != nil {
		nn = s.location.NetworkName
	}
	return nn
}

// ProbeIP returns the probe IP.
func (s *Session) ProbeIP() string {
	defer s.mu.Unlock()
	s.mu.Lock()
	ip := geolocate.DefaultProbeIP
	if s.location != nil {
		ip = s.location.ProbeIP
	}
	return ip
}

// ProxyURL returns the Proxy URL, or nil if not set
func (s *Session) ProxyURL() *url.URL {
	return s.proxyURL
}

// ResolverASNString returns the resolver ASN as a string
func (s *Session) ResolverASNString() string {
	return fmt.Sprintf("AS%d", s.ResolverASN())
}

// ResolverASN returns the resolver ASN
func (s *Session) ResolverASN() uint {
	defer s.mu.Unlock()
	s.mu.Lock()
	asn := geolocate.DefaultResolverASN
	if s.location != nil {
		asn = s.location.ResolverASN
	}
	return asn
}

// ResolverIP returns the resolver IP
func (s *Session) ResolverIP() string {
	defer s.mu.Unlock()
	s.mu.Lock()
	ip := geolocate.DefaultResolverIP
	if s.location != nil {
		ip = s.location.ResolverIP
	}
	return ip
}

// ResolverNetworkName returns the resolver network name.
func (s *Session) ResolverNetworkName() string {
	defer s.mu.Unlock()
	s.mu.Lock()
	nn := geolocate.DefaultResolverNetworkName
	if s.location != nil {
		nn = s.location.ResolverNetworkName
	}
	return nn
}

// SoftwareName returns the application name.
func (s *Session) SoftwareName() string {
	return s.softwareName
}

// SoftwareVersion returns the application version.
func (s *Session) SoftwareVersion() string {
	return s.softwareVersion
}

// TempDir returns the temporary directory.
func (s *Session) TempDir() string {
	return s.tempDir
}

// TorArgs returns the configured extra args for the tor binary. If not set
// we will not pass in any extra arg. Applies to `-OTunnel=tor` mainly.
func (s *Session) TorArgs() []string {
	return s.torArgs
}

// TorBinary returns the configured path to the tor binary. If not set
// we will attempt to use "tor". Applies to `-OTunnel=tor` mainly.
func (s *Session) TorBinary() string {
	return s.torBinary
}

// UserAgent constructs the user agent to be used in this session.
func (s *Session) UserAgent() (useragent string) {
	useragent += s.softwareName + "/" + s.softwareVersion
	useragent += " ooniprobe-engine/" + version.Version
	return
}

// MaybeUpdateResources updates the resources if needed.
func (s *Session) MaybeUpdateResources(ctx context.Context) error {
	return (&resourcesmanager.CopyWorker{DestDir: s.assetsDir}).Ensure()
}

// getAvailableProbeServicesUnlocked returns the available probe
// services. This function WILL NOT acquire the mu mutex, therefore,
// you MUST ensure you are using it from a locked context.
func (s *Session) getAvailableProbeServicesUnlocked() []model.Service {
	if len(s.availableProbeServices) > 0 {
		return s.availableProbeServices
	}
	return probeservices.Default()
}

func (s *Session) initOrchestraClient(
	ctx context.Context, clnt *probeservices.Client,
	maybeLogin func(ctx context.Context) error,
) (*probeservices.Client, error) {
	// The original implementation has as its only use case that we
	// were registering and logging in for sending an update regarding
	// the probe whereabouts. Yet here in probe-engine, the orchestra
	// is currently only used to fetch inputs. For this purpose, we don't
	// need to communicate any specific information. The code that will
	// perform an update used to be responsible of doing that. Now, we
	// are not using orchestra for this purpose anymore.
	meta := probeservices.Metadata{
		Platform:        "miniooni",
		ProbeASN:        "AS0",
		ProbeCC:         "ZZ",
		SoftwareName:    "miniooni",
		SoftwareVersion: "0.1.0-dev",
		SupportedTests:  []string{"web_connectivity"},
	}
	if err := clnt.MaybeRegister(ctx, meta); err != nil {
		return nil, err
	}
	if err := maybeLogin(ctx); err != nil {
		return nil, err
	}
	return clnt, nil
}

// ErrAllProbeServicesFailed indicates all probe services failed.
var ErrAllProbeServicesFailed = errors.New("all available probe services failed")

// maybeLookupBackendsContext uses testMaybeLookupBackendsContext if
// not nil, otherwise it calls MaybeLookupBackendsContext.
func (s *Session) maybeLookupBackendsContext(ctx context.Context) error {
	if s.testMaybeLookupBackendsContext != nil {
		return s.testMaybeLookupBackendsContext(ctx)
	}
	return s.MaybeLookupBackendsContext(ctx)
}

// MaybeLookupBackendsContext is like MaybeLookupBackends but with context.
func (s *Session) MaybeLookupBackendsContext(ctx context.Context) error {
	defer s.mu.Unlock()
	s.mu.Lock()
	if s.selectedProbeService != nil {
		return nil
	}
	s.queryProbeServicesCount.Add(1)
	candidates := probeservices.TryAll(ctx, s, s.getAvailableProbeServicesUnlocked())
	selected := probeservices.SelectBest(candidates)
	if selected == nil {
		return ErrAllProbeServicesFailed
	}
	s.logger.Infof("session: using probe services: %+v", selected.Endpoint)
	s.selectedProbeService = &selected.Endpoint
	s.availableTestHelpers = selected.TestHelpers
	return nil
}

// LookupLocationContext performs a location lookup. If you want memoisation
// of the results, you should use MaybeLookupLocationContext.
func (s *Session) LookupLocationContext(ctx context.Context) (*geolocate.Results, error) {
	// Implementation note: we don't perform the lookup of the resolver IP
	// when we are using a proxy because that might leak information.
	task := geolocate.Must(geolocate.NewTask(geolocate.Config{
		EnableResolverLookup: s.proxyURL == nil,
		Logger:               s.Logger(),
		Resolver:             s.resolver,
		ResourcesManager:     s,
		UserAgent:            s.UserAgent(),
	}))
	return task.Run(ctx)
}

// lookupLocationContext calls testLookupLocationContext if set and
// otherwise calls LookupLocationContext.
func (s *Session) lookupLocationContext(ctx context.Context) (*geolocate.Results, error) {
	if s.testLookupLocationContext != nil {
		return s.testLookupLocationContext(ctx)
	}
	return s.LookupLocationContext(ctx)
}

// MaybeLookupLocationContext is like MaybeLookupLocation but with a context
// that can be used to interrupt this long running operation. This function
// will fail IMMEDIATELY if given a cancelled context.
func (s *Session) MaybeLookupLocationContext(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err() // helps with testing
	}
	defer s.mu.Unlock()
	s.mu.Lock()
	if s.location == nil {
		location, err := s.lookupLocationContext(ctx)
		if err != nil {
			return err
		}
		s.location = location
	}
	return nil
}

var _ model.ExperimentSession = &Session{}