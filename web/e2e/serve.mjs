// Playwright webServer entry: runs the stack until Playwright stops it.
import { startStack } from "./harness.mjs";

const stack = await startStack();
console.log(`cronpad e2e stack ready at ${stack.baseURL} (echo ${stack.echoURL})`);

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    void stack.stop().finally(() => process.exit(0));
  });
}
setInterval(() => {}, 1 << 30);
