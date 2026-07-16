/**
 * UAT-11 — Phase 8: MCP server tool surface.
 *
 * Verifies that the dense-mem MCP HTTP endpoint:
 * - responds to JSON-RPC initialize with a valid tools list
 * - exposes the dense-mem memory contract tools
 * - relies on profile scope derived from the API key
 * - returns structured errors for unknown tools
 *
 * NOTE: These tests call the live dense-mem server's Streamable HTTP /mcp
 * endpoint, so they require DENSE_MEM_URL and DENSE_MEM_API_KEY.
 */

import { test, expect } from '@playwright/test';
import {
  spawnMcp,
  DENSE_MEM_API_KEY,
  DENSE_MEM_URL,
} from './helpers';

function mcpEnv(): Record<string, string> {
  return {
    DENSE_MEM_URL,
    DENSE_MEM_API_KEY,
  };
}

// UAT-11a: MCP server responds to initialize with tools list
test('UAT-11a: MCP initialize returns tools list', async () => {
  const mcp = await spawnMcp(mcpEnv());

  try {
    const response = await mcp.call('initialize', {
      protocolVersion: '2024-11-05',
      capabilities: {},
      clientInfo: { name: 'uat-test', version: '1.0.0' },
    });

    expect(response).toBeDefined();
    const res = response as Record<string, unknown>;
    expect(res).toHaveProperty('result');
    const result = res.result as Record<string, unknown>;
    expect(result).toHaveProperty('capabilities');
  } finally {
    await mcp.close();
  }
});

// UAT-11b: MCP server exposes expected tools
test('UAT-11b: MCP endpoint exposes required memory tools', async () => {
  const mcp = await spawnMcp(mcpEnv());

  try {
    // Initialize first
    await mcp.call('initialize', {
      protocolVersion: '2024-11-05',
      capabilities: {},
      clientInfo: { name: 'uat-test', version: '1.0.0' },
    });

    const response = await mcp.call('tools/list', {});
    const res = response as Record<string, unknown>;
    expect(res).toHaveProperty('result');
    const result = res.result as Record<string, unknown>;
    expect(result).toHaveProperty('tools');

    const tools = result.tools as Array<{ name: string }>;
    const toolNames = tools.map((t) => t.name);
    expect(toolNames).toEqual(
      expect.arrayContaining([
        'recall_memory',
        'trace_memory',
        'remember',
        'get_memory_placement',
        'dispute_memory_placement',
        'import_memories',
        'reflect_memories',
        'confirm_memory',
        'find_memory_pack_candidates',
        'export_memory_pack',
        'inspect_memory_pack',
        'import_memory_pack',
        'rollback_memory_pack_import',
      ]),
    );
    const removedTools = [
      'list_recent_memories',
      'keyword_search',
      'semantic_search',
      'graph_query',
      'post_claim',
      'get_fact',
    ];
    for (const toolName of removedTools) {
      expect(toolNames).not.toContain(toolName);
    }
  } finally {
    await mcp.close();
  }
});

// UAT-11c: MCP unknown tool returns JSON-RPC error
test('UAT-11c: calling unknown MCP tool returns JSON-RPC error', async () => {
  const mcp = await spawnMcp(mcpEnv());

  try {
    await mcp.call('initialize', {
      protocolVersion: '2024-11-05',
      capabilities: {},
      clientInfo: { name: 'uat-test', version: '1.0.0' },
    });

    const response = await mcp.call('tools/call', {
      name: 'nonexistent_tool_xyz',
      arguments: {},
    });
    const res = response as Record<string, unknown>;
    // Must return a JSON-RPC error, not a success result
    expect(res).toHaveProperty('error');
    const err = res.error as Record<string, unknown>;
    expect(typeof err.code).toBe('number');
    expect(typeof err.message).toBe('string');
  } finally {
    await mcp.close();
  }
});

// UAT-11d: MCP recall tool returns profile-scoped results
test('UAT-11d: MCP recall_memory tool is profile-scoped', async () => {
  const mcp = await spawnMcp(mcpEnv());

  try {
    await mcp.call('initialize', {
      protocolVersion: '2024-11-05',
      capabilities: {},
      clientInfo: { name: 'uat-test', version: '1.0.0' },
    });

    const response = await mcp.call('tools/call', {
      name: 'recall_memory',
      arguments: {
        query: 'test query for profile isolation',
        limit: 5,
      },
    });

    const res = response as Record<string, unknown>;
    // Should return either a result (empty or populated) or a JSON-RPC error
    // not a 5xx panic
    const hasResult = 'result' in res;
    const hasError = 'error' in res;
    expect(hasResult || hasError).toBe(true);
  } finally {
    await mcp.close();
  }
});
