import threading
import time

from hatchet_sdk import Context, EmptyModel, Hatchet

hatchet = Hatchet.embedded()


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
    print(result["greeting"])


if __name__ == "__main__":
    main()
