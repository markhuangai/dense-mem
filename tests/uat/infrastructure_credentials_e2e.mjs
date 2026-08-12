#!/usr/bin/env node

import { spawnSync } from "node:child_process";

const project = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const result = spawnSync("docker", ["compose", "-p", project, "-f", composeFile, "config", "--format", "json"], { encoding: "utf8" });
if (result.status !== 0) throw new Error("docker compose config failed");
const config = JSON.parse(result.stdout);
const services = config.services ?? {};
const serialized = JSON.stringify(config);
if (serialized.includes("/var/run/docker.sock") || serialized.includes("docker.sock")) {
  throw new Error("compose configuration mounts the Docker socket");
}
const redisHealthcheck = JSON.stringify(services.redis?.healthcheck?.test ?? []);
if (!redisHealthcheck.includes("REDISCLI_AUTH") || !redisHealthcheck.includes("redis-cli")) {
  throw new Error("Redis healthcheck does not authenticate through REDISCLI_AUTH");
}
const container = spawnSync("docker", ["compose", "-p", project, "-f", composeFile, "ps", "-q", "server"], { encoding: "utf8" });
if (container.status !== 0 || !container.stdout.trim()) throw new Error("server container is not running");
const inspect = spawnSync("docker", ["inspect", "--format", "{{json .Config.Entrypoint}}", container.stdout.trim()], { encoding: "utf8" });
if (inspect.status !== 0 || !inspect.stdout.includes("docker-entrypoint.sh")) {
  throw new Error("server does not use the exec-only Docker entrypoint");
}

console.log(JSON.stringify({
  status: "ok",
  docker_socket_mount_absent: true,
  redis_healthcheck_authenticated: true,
  exec_entrypoint_configured: true,
}, null, 2));

function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
