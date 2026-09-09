package embed

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	adminseed "github.com/hatchet-dev/hatchet/cmd/hatchet-admin/cli/seed"
	api "github.com/hatchet-dev/hatchet/cmd/hatchet-api/api"
	engine "github.com/hatchet-dev/hatchet/cmd/hatchet-engine/engine"
	"github.com/hatchet-dev/hatchet/pkg/config/loader"
	"github.com/hatchet-dev/hatchet/pkg/config/server"
	"github.com/hatchet-dev/hatchet/pkg/config/shared"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"github.com/hatchet-dev/hatchet-embedded/keyset"
)

type Instance struct {
	client      *hatchet.Client
	token       string
	tenantID    string
	apiURL      string
	grpcAddress string

	interruptCh  chan interface{}
	cancel       context.CancelFunc
	wg           *sync.WaitGroup
	shutdownOnce sync.Once

	pg         *embeddedpostgres.EmbeddedPostgres
	stopPGOnce sync.Once

	// clientLogger is handed to the SDK client that Start builds, so client
	// and worker output shares the embedded format and level
	clientLogger *zerolog.Logger
}

func (i *Instance) Client() *hatchet.Client { return i.client }

func (i *Instance) Token() string { return i.token }

func (i *Instance) TenantID() string { return i.tenantID }

func (i *Instance) APIURL() string { return i.apiURL }

func (i *Instance) GRPCAddress() string { return i.grpcAddress }

func (i *Instance) Shutdown(ctx context.Context) error {
	i.shutdownOnce.Do(func() {
		i.cancel()
		close(i.interruptCh)
	})

	done := make(chan struct{})
	go func() {
		i.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return i.stopPostgres()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (i *Instance) stopPostgres() error {
	var err error
	i.stopPGOnce.Do(func() {
		if i.pg != nil {
			err = i.pg.Stop()
		}
	})
	return err
}

func StartServer(ctx context.Context, opts ...Option) (inst *Instance, err error) {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	lg := resolveLogger(cfg)

	stopHeartbeat := startupHeartbeat(lg)
	defer stopHeartbeat()

	var pg *embeddedpostgres.EmbeddedPostgres
	if strings.TrimSpace(cfg.postgresURL) == "" {
		pg, cfg.postgresURL, err = startEmbeddedPostgres(lg, cfg.postgresDataDir)
		if err != nil {
			return nil, err
		}
		defer func() {
			if err != nil {
				_ = pg.Stop()
			}
		}()
	}

	version, err := resolveVersion()
	if err != nil {
		return nil, err
	}
	cfg.version = &version

	if err := os.Setenv("DATABASE_URL", cfg.postgresURL); err != nil {
		return nil, fmt.Errorf("could not set DATABASE_URL: %w", err)
	}

	// the database logger is configured from the environment when the data
	// layer loads, so it follows the embedded level and console format
	logLevel := resolveLogLevel(cfg)
	_ = os.Setenv("DATABASE_LOGGER_LEVEL", logLevel)
	_ = os.Setenv("DATABASE_LOGGER_FORMAT", "console")

	if cfg.adminEmail != nil && *cfg.adminEmail != "" {
		_ = os.Setenv("ADMIN_EMAIL", *cfg.adminEmail)
	}
	if cfg.adminPassword != nil && *cfg.adminPassword != "" {
		_ = os.Setenv("ADMIN_PASSWORD", *cfg.adminPassword)
	}

	if cfg.runMigrations != nil && *cfg.runMigrations {
		if migrateErr := runMigrations(ctx, lg); migrateErr != nil {
			return nil, fmt.Errorf("could not run migrations: %w", migrateErr)
		}
	}

	if cfg.masterKeyset == nil || len(*cfg.masterKeyset) == 0 {
		lg.Warn().Msgf("using auto-managed keysets stored in the %q schema; "+
			"at-rest encryption offers no additional protection here because the keys live in the same database as the data they encrypt. "+
			"Pass WithKeysets to manage keys externally.", keyset.DefaultSchema)
		ks, keysetErr := keyset.Resolve(ctx, cfg.postgresURL)
		if keysetErr != nil {
			return nil, keysetErr
		}
		cfg.masterKeyset = &ks.Master
		cfg.privateJWTKeyset = &ks.PrivateJWT
		cfg.publicJWTKeyset = &ks.PublicJWT
	}

	startServerAPI := cfg.startAPI == nil || *cfg.startAPI

	grpcPort, err := resolvePort(cfg.grpcPort)
	if err != nil {
		return nil, fmt.Errorf("could not allocate a gRPC port: %w", err)
	}

	apiPort := 0
	var apiPortHold net.Listener
	if startServerAPI {
		apiPort, apiPortHold, err = resolveAPIPort(cfg.apiPort)
		if err != nil {
			return nil, fmt.Errorf("could not allocate an API port: %w", err)
		}
		defer func() {
			if err != nil && apiPortHold != nil {
				_ = apiPortHold.Close()
			}
		}()
	}

	grpcBroadcast := fmt.Sprintf("127.0.0.1:%d", grpcPort)
	apiURL := fmt.Sprintf("http://localhost:%d", apiPort)

	hashKey, err := randomHex(32)
	if err != nil {
		return nil, fmt.Errorf("could not generate cookie secret: %w", err)
	}
	blockKey, err := randomHex(16)
	if err != nil {
		return nil, fmt.Errorf("could not generate cookie secret: %w", err)
	}

	override := func(scf *server.ServerConfigFile) {
		// the engine, API, queue, and pgx-stats loggers share the embedded
		// level and console format, so all output lands on stderr in one style
		loggerCfg := shared.LoggerConfigFile{Level: logLevel, Format: "console"}
		scf.Logger = loggerCfg
		scf.AdditionalLoggers.Queue = loggerCfg
		scf.AdditionalLoggers.PgxStats = loggerCfg

		scf.Auth.Cookie.Domain = "localhost"
		scf.Auth.Cookie.Insecure = true
		scf.Auth.Cookie.Secrets = hashKey + " " + blockKey

		scf.Runtime.Port = apiPort
		scf.Runtime.ServerURL = apiURL
		scf.Runtime.GRPCPort = grpcPort
		scf.Runtime.GRPCBindAddress = "127.0.0.1"
		scf.Runtime.GRPCBroadcastAddress = grpcBroadcast
		scf.Runtime.GRPCInsecure = true
		scf.Runtime.Healthcheck = false
		scf.Runtime.Embedded = true

		if cfg.usePostgresMQ() {
			scf.MessageQueue.Kind = "postgres"
		} else {
			scf.MessageQueue.Kind = "rabbitmq"
			scf.MessageQueue.RabbitMQ.URL = *cfg.rabbitMQURL
		}

		scf.Encryption.MasterKeyset = string(*cfg.masterKeyset)
		scf.Encryption.JWT.PrivateJWTKeyset = string(*cfg.privateJWTKeyset)
		scf.Encryption.JWT.PublicJWTKeyset = string(*cfg.publicJWTKeyset)
	}

	cf := loader.NewConfigLoader("")

	dc, err := cf.InitDataLayer()
	if err != nil {
		return nil, fmt.Errorf("could not init data layer: %w", err)
	}
	if seedErr := adminseed.SeedDatabase(dc); seedErr != nil {
		_ = dc.Disconnect()
		return nil, fmt.Errorf("could not seed database: %w", seedErr)
	}
	tenantID := dc.Seed.DefaultTenantID

	fleetSize, err := activeFleetSize(ctx, dc.Pool)
	if err != nil {
		lg.Warn().Err(err).Msg("could not read fleet size")
	}

	tokenCleanup, sc, err := cf.CreateServerFromConfig(*cfg.version, override)
	if err != nil {
		_ = dc.Disconnect()
		return nil, fmt.Errorf("could not build server config: %w", err)
	}

	parsedTenantID, err := uuid.Parse(tenantID)
	if err != nil {
		_ = tokenCleanup()
		_ = sc.Disconnect()
		_ = dc.Disconnect()
		return nil, fmt.Errorf("could not parse default tenant id: %w", err)
	}

	expiresAt := time.Now().UTC().Add(90 * 24 * time.Hour)
	tok, err := sc.Auth.JWTManager.GenerateTenantToken(ctx, parsedTenantID, "embedded", false, &expiresAt)

	_ = tokenCleanup()
	_ = sc.Disconnect()
	_ = dc.Disconnect()

	if err != nil {
		return nil, fmt.Errorf("could not mint token: %w", err)
	}

	interruptCh := make(chan interface{})
	engineCtx, cancel := context.WithCancel(ctx)
	wg := &sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if runErr := engine.Run(engineCtx, cf, *cfg.version, override); runErr != nil {
			lg.Error().Err(runErr).Msg("engine exited")
		}
	}()

	if startServerAPI {
		if apiPortHold != nil {
			_ = apiPortHold.Close()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if startErr := api.Start(cf, interruptCh, *cfg.version, override); startErr != nil {
				lg.Error().Err(startErr).Msg("api exited")
			}
		}()
	}

	waitTargets := []string{grpcBroadcast}
	if startServerAPI {
		waitTargets = append(waitTargets, fmt.Sprintf("127.0.0.1:%d", apiPort))
	}
	if waitErr := waitForListeners(ctx, waitTargets, 30*time.Second); waitErr != nil {
		cancel()
		close(interruptCh)
		return nil, waitErr
	}

	_ = os.Setenv("HATCHET_CLIENT_TOKEN", tok.Token)
	_ = os.Setenv("HATCHET_CLIENT_HOST_PORT", grpcBroadcast)
	_ = os.Setenv("HATCHET_CLIENT_TENANT_ID", tenantID)
	_ = os.Setenv("HATCHET_CLIENT_TLS_STRATEGY", "none")
	if startServerAPI {
		_ = os.Setenv("HATCHET_CLIENT_SERVER_URL", apiURL)
	}
	_ = os.Setenv("HATCHET_CLIENT_LOG_LEVEL", logLevel)
	_ = os.Setenv("HATCHET_CLIENT_LOG_FORMAT", "console")

	fleetStatus := "starting a new fleet"
	if fleetSize > 0 {
		fleetStatus = fmt.Sprintf("joining a fleet of %d engine(s)", fleetSize)
	}
	apiStatus := "off"
	if startServerAPI {
		apiStatus = apiURL
	}
	lg.Info().Msgf("engine ready: grpc=%s api=%s | %s", grpcBroadcast, apiStatus, fleetStatus)

	instanceAPIURL := ""
	if startServerAPI {
		instanceAPIURL = apiURL
	}

	return &Instance{
		token:        tok.Token,
		tenantID:     tenantID,
		apiURL:       instanceAPIURL,
		grpcAddress:  grpcBroadcast,
		interruptCh:  interruptCh,
		cancel:       cancel,
		wg:           wg,
		pg:           pg,
		clientLogger: clientLogger(cfg),
	}, nil
}

func startEmbeddedPostgres(lg *zerolog.Logger, baseDir string) (*embeddedpostgres.EmbeddedPostgres, string, error) {
	port, err := freePort()
	if err != nil {
		return nil, "", fmt.Errorf("could not allocate a Postgres port: %w", err)
	}
	if port < 0 || port > math.MaxUint16 {
		return nil, "", fmt.Errorf("allocated Postgres port %d out of range", port)
	}

	if baseDir == "" {
		baseDir, err = defaultPostgresBaseDir()
		if err != nil {
			return nil, "", err
		}
	}

	needsDownload, needsInit := postgresFirstRunPhases(baseDir)
	switch {
	case needsDownload:
		lg.Info().Msgf("first run: downloading a bundled Postgres to ~/.embedded-postgres-go and initializing it in %s (this can take a minute)", baseDir)
	case needsInit:
		lg.Info().Msgf("first run: initializing a bundled Postgres in %s (this can take a minute)", baseDir)
	}

	pg := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Port(uint32(port)).
			Database("hatchet").
			RuntimePath(filepath.Join(baseDir, "runtime")).
			DataPath(filepath.Join(baseDir, "data")).
			StartParameters(map[string]string{"timezone": "UTC"}).
			Logger(pgLogWriter{lg}),
	)
	if startErr := pg.Start(); startErr != nil {
		return nil, "", fmt.Errorf("could not start embedded Postgres: %w", startErr)
	}

	url := fmt.Sprintf("postgres://postgres:postgres@localhost:%d/hatchet?sslmode=disable", port)
	lg.Info().Msgf("started embedded Postgres on port %d with data in %s (pass WithPostgres to use your own)", port, baseDir)
	return pg, url, nil
}

// postgresFirstRunPhases reports which slow first-run phases the bundled
// Postgres will go through before it can accept connections: downloading its
// binaries (no archive cached under ~/.embedded-postgres-go, the
// embedded-postgres library's cache; the library exposes no download-progress
// hook, so presence of any cached archive is the best available signal) and
// initializing a fresh data directory under baseDir.
func postgresFirstRunPhases(baseDir string) (needsDownload, needsInit bool) {
	needsDownload = true
	if home, err := os.UserHomeDir(); err == nil {
		matches, _ := filepath.Glob(filepath.Join(home, ".embedded-postgres-go", "embedded-postgres-binaries-*.txz"))
		needsDownload = len(matches) == 0
	}

	_, statErr := os.Stat(filepath.Join(baseDir, "data", "PG_VERSION"))
	needsInit = statErr != nil

	return needsDownload, needsInit
}

// startupHeartbeat logs a line every 30 seconds until the returned stop
// function is called, so slow first runs (Postgres download, initdb,
// migrations) do not look hung. Warm starts finish well within the first
// interval and print nothing.
func startupHeartbeat(lg *zerolog.Logger) func() {
	start := time.Now()
	done := make(chan struct{})

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				lg.Info().Msgf("still waiting for the embedded engine (%ds elapsed)", int(time.Since(start).Seconds()))
			}
		}
	}()

	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// defaultPostgresBaseDir keys the bundled Postgres directory by the working
// directory so instances started from different projects never share a data
// dir, while restarts from the same project keep their data.
func defaultPostgresBaseDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not determine the working directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine the home directory: %w", err)
	}
	sum := sha256.Sum256([]byte(cwd))
	return filepath.Join(home, ".hatchet-embedded", hex.EncodeToString(sum[:6])), nil
}

func Start(ctx context.Context, opts ...Option) (*Instance, error) {
	inst, err := StartServer(ctx, opts...)
	if err != nil {
		return nil, err
	}

	client, err := hatchet.NewClient(hatchet.WithClientLogger(inst.clientLogger))
	if err != nil {
		_ = inst.Shutdown(context.Background())
		return nil, fmt.Errorf("could not build embedded client: %w", err)
	}
	inst.client = client

	return inst, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// DefaultAPIPort is the port the embedded API binds to when it is free, so
// tooling like 'hatchet embedded-ui' can find an instance without
// configuration. Additional instances fall back to a random free port. The
// value avoids IANA-assigned services and the OS ephemeral range.
const DefaultAPIPort = 28243

func resolveAPIPort(explicit *int) (int, net.Listener, error) {
	if explicit != nil {
		return *explicit, nil, nil
	}
	if l, err := net.Listen("tcp", fmt.Sprintf(":%d", DefaultAPIPort)); err == nil {
		return DefaultAPIPort, l, nil
	}
	port, err := freePort()
	return port, nil, err
}

func resolvePort(explicit *int) (int, error) {
	if explicit != nil {
		return *explicit, nil
	}
	return freePort()
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitForListeners(ctx context.Context, addresses []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for _, addr := range addresses {
		for {
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err == nil {
				_ = conn.Close()
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("embed: %s did not start listening within %s: %w", addr, timeout, err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return nil
}
