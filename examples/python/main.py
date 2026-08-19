import os
import threading
import time

from hatchet_sdk import Context, EmptyModel
from hatchet_sdk.embedded import HatchetEmbedded

hatchet = HatchetEmbedded()


class GreetInput(EmptyModel):
    name: str = "embed"


@hatchet.task(name="greet", input_validator=GreetInput)
def greet(input: GreetInput, ctx: Context) -> dict[str, str]:
    return {"greeting": f"Hello, {input.name}!"}


def main() -> None:
    worker = hatchet.worker("basic-worker", workflows=[greet])
    threading.Thread(target=worker.start, daemon=True).start()
    time.sleep(2)

    result = greet.run(GreetInput(name="embed"))
    print(result["greeting"], flush=True)

    # the worker's subprocesses would otherwise keep the interpreter alive at
    # exit; the sidecar still shuts down cleanly via its stdin pipe
    os._exit(0)


if __name__ == "__main__":
    main()
