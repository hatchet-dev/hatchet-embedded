package embed

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog"

	migrate "github.com/hatchet-dev/hatchet/cmd/hatchet-migrate/migrate"
	"github.com/hatchet-dev/hatchet/pkg/config/shared"
	"github.com/hatchet-dev/hatchet/pkg/logger"
)

// resolveLogLevel returns the configured log level, falling back to warn when
// it is unset or invalid.
func resolveLogLevel(cfg *Config) string {
	if cfg.logLevel != nil && *cfg.logLevel != "" {
		if _, err := zerolog.ParseLevel(*cfg.logLevel); err == nil {
			return *cfg.logLevel
		}
	}
	return "warn"
}

// resolveLogger returns the caller-supplied logger, or one built with the core
// pkg/logger console format so embed's own output matches the engine's exactly.
// Lifecycle notices (first-run, engine ready, heartbeat) log at info, so the
// default logger is floored at info to keep them visible at the default warn
// level; the engine, API, and database loggers run at the configured level.
func resolveLogger(cfg *Config) *zerolog.Logger {
	if cfg.logger != nil {
		return cfg.logger
	}

	level := resolveLogLevel(cfg)
	if lvl, err := zerolog.ParseLevel(level); err == nil && lvl > zerolog.InfoLevel {
		level = "info"
	}

	l := logger.NewStdErr(&shared.LoggerConfigFile{Level: level, Format: "console"}, "embed")
	return &l
}

// clientLogger returns the logger handed to the SDK client that embed.Start
// builds, so client and worker output shares the embedded format and level.
func clientLogger(cfg *Config) *zerolog.Logger {
	if cfg.logger != nil {
		return cfg.logger
	}

	l := logger.NewStdErr(&shared.LoggerConfigFile{Level: resolveLogLevel(cfg), Format: "console"}, "worker")
	return &l
}

// pgLogWriter routes embedded-postgres output through the logger at debug
// level, one record per line so multi-line chunks stay readable.
type pgLogWriter struct{ l *zerolog.Logger }

func (w pgLogWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if msg := strings.TrimSpace(line); msg != "" {
			w.l.Debug().Msg(msg)
		}
	}
	return len(p), nil
}

// runMigrations routes goose's per-migration lines through lg at debug level
// and reports one summary line when migrations were applied. Goose logs plain
// text through the std log package otherwise; this is the one place embed has
// to adapt output instead of configuring it.
func runMigrations(ctx context.Context, lg *zerolog.Logger) error {
	applied := 0
	goose.SetLogger(gooseLogger{lg: lg, applied: &applied})
	defer goose.SetLogger(gooseStdLogger{})

	if err := migrate.RunMigrations(ctx); err != nil {
		return err
	}

	if applied > 0 {
		lg.Info().Msgf("applied %d database migrations", applied)
	}
	return nil
}

// gooseLogger satisfies goose.Logger on top of the embed logger.
type gooseLogger struct {
	lg      *zerolog.Logger
	applied *int
}

func (g gooseLogger) Printf(format string, v ...interface{}) {
	msg := strings.TrimRight(fmt.Sprintf(format, v...), "\n")
	if strings.HasPrefix(msg, "OK ") {
		*g.applied++
	}
	g.lg.Debug().Msg(msg)
}

// Fatalf preserves goose's exit-on-fatal semantics; zerolog's Fatal exits the
// process after logging.
func (g gooseLogger) Fatalf(format string, v ...interface{}) {
	g.lg.Fatal().Msgf(format, v...)
}

// gooseStdLogger restores goose's stock std-log behavior after embed's
// migrations finish, in case the application uses goose itself.
type gooseStdLogger struct{}

func (gooseStdLogger) Fatalf(format string, v ...interface{}) { log.Fatalf(format, v...) }
func (gooseStdLogger) Printf(format string, v ...interface{}) { log.Printf(format, v...) }
