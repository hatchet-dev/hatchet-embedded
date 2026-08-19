# hatchet-embedded

Run a full [Hatchet](https://github.com/hatchet-dev/hatchet) engine (migrations,
API, and gRPC) from your application — in-process from Go, or as a sidecar
process from the TypeScript and Python SDKs. By default it also starts a
bundled Postgres, so you need zero external services to get running.

**Full documentation: [docs.hatchet.run/v1/embedded](https://docs.hatchet.run/v1/embedded)**

## Quickstart

Go (in-process, via a blank import):

```go
import (
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	_ "github.com/hatchet-dev/hatchet-embedded"
)

client, err := hatchet.NewClient(hatchet.WithEmbedded())
```

TypeScript (a separate entry point, so it never ends up in production bundles):

```ts
import { HatchetEmbeddedClient } from '@hatchet-dev/typescript-sdk/v1/embedded';

const hatchet = await HatchetEmbeddedClient.init();
```

Python (a separate import path, loaded only when you use it):

```sh
pip install hatchet-sdk[embedded]
```

```python
from hatchet_sdk.embedded import HatchetEmbedded

hatchet = HatchetEmbedded()
```

Runnable examples for all three live in [examples](examples/).

## Releases

Release tags correspond to the publicly released Hatchet engine version baked
into the sidecar: `vX.Y.Z`, or `vX.Y.Z-N` for sidecar-only fixes on the same
engine. Each release ships `hatchet-embedded-sidecar` binaries for
darwin/linux (signed and notarized on macOS), which the TypeScript and Python
SDKs download on first use.

## License

MIT — see [LICENSE](LICENSE).
