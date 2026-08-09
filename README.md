# hatchet-embedded

Run a full [Hatchet](https://github.com/hatchet-dev/hatchet) engine (migrations,
API, and gRPC) in-process from your Go program. It starts the engine, seeds an
admin tenant, and hands the Go SDK a client wired to the embedded instance.

By default it also spins up its own Postgres using
[fergusstrange/embedded-postgres](https://github.com/fergusstrange/embedded-postgres),
so you need zero external services to get running. Pass `WithPostgres(url)` to
point at your own database instead — that disables the embedded Postgres.

## Install

```bash
go get github.com/hatchet-dev/hatchet-embedded
```

## Usage

The package registers itself with the Go SDK via a blank import. Import it for
side effects alongside the SDK; the SDK detects the embedded config and boots the
engine on `NewClient`:

```go
import (
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	_ "github.com/hatchet-dev/hatchet-embedded"
)

client, err := hatchet.NewClient(...)
```

Embedded mode is enabled through the SDK's client options / environment; without
the blank import the SDK returns an error asking for it.

## Options

`Start` also accepts functional options directly when you drive the engine
yourself:

| Option | Effect |
| --- | --- |
| `WithPostgres(url)` | Use your own Postgres instead of the embedded one |
| `WithRabbitMQ(url)` | Use RabbitMQ instead of the Postgres message queue |
| `WithAdminUser(email, password)` | Seed a specific admin user |
| `WithKeysets(master, privateJWT, publicJWT)` | Supply encryption keysets instead of generating them |
| `WithAPIPort(port)` / `WithGRPCPort(port)` | Bind the API / gRPC servers to specific ports |
| `WithoutAPI()` | Start only the engine + gRPC, no REST API |
| `WithoutMigrations()` | Skip running migrations on startup |
| `WithLogLevel(level)` | Engine log level (default `warn`) |

## License

MIT — see [LICENSE](LICENSE).
