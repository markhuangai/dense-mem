#!/usr/bin/env node
process.env.DENSE_MEM_E2E_FOUNDATION_SCENARIO = "migration_convergence";
await import("./v2_5_foundations_e2e.mjs");
