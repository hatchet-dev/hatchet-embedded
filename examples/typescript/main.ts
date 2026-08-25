import { HatchetEmbeddedClient } from '@hatchet-dev/typescript-sdk/v1/embedded';

async function main() {
  const hatchet = await HatchetEmbeddedClient.init();

  const greet = hatchet.task({
    name: 'greet',
    fn: (input: { name: string }) => ({ greeting: `Hello, ${input.name}!` }),
  });

  const worker = await hatchet.worker('basic-worker', { workflows: [greet] });
  void worker.start();

  const result = await greet.run({ name: 'embed' });
  console.log(result.greeting);

  await worker.stop();
  await hatchet.stopEmbedded();
  process.exit(0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
