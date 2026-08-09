package embed

import (
	"strings"

	"github.com/rs/zerolog"

	"github.com/hatchet-dev/hatchet/pkg/logger"
)

// resolveLogger returns the caller-supplied logger, or one built the same way
// the Go SDK builds its own (pkg/logger) at the configured level.
func resolveLogger(cfg *Config) *zerolog.Logger {
	if cfg.logger != nil {
		return cfg.logger
	}
	l := logger.NewDefaultLogger("embed")
	if cfg.logLevel != nil && *cfg.logLevel != "" {
		if lvl, err := zerolog.ParseLevel(*cfg.logLevel); err == nil {
			l = l.Level(lvl)
		}
	}
	return &l
}

// pgLogWriter routes embedded-postgres output through the logger at debug level.
type pgLogWriter struct{ l *zerolog.Logger }

func (w pgLogWriter) Write(p []byte) (int, error) {
	if msg := strings.TrimRight(string(p), "\n"); msg != "" {
		w.l.Debug().Msg(msg)
	}
	return len(p), nil
}
