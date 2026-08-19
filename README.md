# hatchet-embedded

Run a full [Hatchet](https://github.com/hatchet-dev/hatchet) engine (migrations,
API, and gRPC) in-process from your Go program — or as a sidecar process from
the TypeScript and Python SDKs. It starts the engine, seeds an admin tenant,
and hands the SDK a client wired to the embedded instance.

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

client, err := hatchet.NewClient(hatchet.WithEmbedded())
```

## TypeScript and Python

The TypeScript and Python SDKs run the engine as a sidecar process. On first
use they download the `hatchet-embedded-sidecar` binary for your platform from
this repo's [releases](https://github.com/hatchet-dev/hatchet-embedded/releases)
(cached under `~/.hatchet/embedded/<version>`, signed and notarized on macOS),
spawn it, and wire the client to it. The sidecar shuts down with your process.

```ts
// requires the optional execa dependency: npm install execa@^5
import { HatchetClient } from '@hatchet-dev/typescript-sdk/v1';

const hatchet = await HatchetClient.embedded();
```

```python
from hatchet_sdk import Hatchet

hatchet = Hatchet.embedded()
```

Both accept the same options as the Go SDK (database URL, ports, log level,
...) plus `version` to pin a hatchet-embedded release; the
`HATCHET_EMBEDDED_VERSION` env var works too. See [examples](examples/).

Multiple instances on one machine work out of the box: ports are
auto-allocated and the bundled Postgres keeps its data in a per-project
directory (`~/.hatchet-embedded/<hash of working dir>`), which also persists
across restarts. Use `WithPostgresDataDir` / `postgresDataDir` /
`postgres_data_dir` to run two instances from the same directory.

## Options

`Start` also accepts functional options directly when you drive the engine
yourself:

| Option | Effect |
| --- | --- |
| `WithPostgres(url)` | Use your own Postgres instead of the embedded one |
| `WithPostgresDataDir(dir)` | Store the bundled Postgres runtime and data under `dir` |
| `WithRabbitMQ(url)` | Use RabbitMQ instead of the Postgres message queue |
| `WithAdminUser(email, password)` | Seed a specific admin user |
| `WithKeysets(master, privateJWT, publicJWT)` | Supply encryption keysets instead of generating them |
| `WithAPIPort(port)` / `WithGRPCPort(port)` | Bind the API / gRPC servers to specific ports |
| `WithoutAPI()` | Start only the engine + gRPC, no REST API |
| `WithoutMigrations()` | Skip running migrations on startup |
| `WithLogLevel(level)` | Engine log level (default `warn`) |

## License

MIT — see [LICENSE](LICENSE).
