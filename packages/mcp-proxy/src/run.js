import { streamableHttpToStdio } from "supergateway/dist/gateways/streamableHttpToStdio.js";

import { ConfigError, formatHelp, parseConfig } from "./config.js";
import { createLogger, redact } from "./logger.js";

export async function run(
  argv = process.argv.slice(2),
  env = process.env,
  stdio = { stdout: process.stdout, stderr: process.stderr },
  gateway = streamableHttpToStdio,
) {
  let config;
  try {
    config = parseConfig(argv, env);
  } catch (error) {
    writeError(stdio.stderr, error);
    return 1;
  }

  if (config.help) {
    stdio.stdout.write(config.helpText ?? formatHelp());
    return 0;
  }

  const logger = createLogger({ verbose: config.verbose, stderr: stdio.stderr });
  try {
    await gateway({
      streamableHttpUrl: config.url,
      headers: config.headers,
      logger,
    });
  } catch (error) {
    writeError(stdio.stderr, error);
    return 1;
  }

  return 0;
}

function writeError(stderr, error) {
  const prefix = error instanceof ConfigError ? "Configuration error" : "Dense-Mem MCP proxy error";
  const message = error instanceof Error ? error.message : String(error);
  stderr.write(`${prefix}: ${redact(message)}\n`);
}
