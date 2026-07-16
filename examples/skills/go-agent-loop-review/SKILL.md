---
schema_version: 1
id: go-agent-loop-review
name: Go Agent Loop Review
version: 1.0.0
description: Review a Go-based LLM agent loop, tool execution, and MCP integration.
tags:
  - go
  - agents
  - tools
  - mcp
triggers:
  - review the agent loop
  - inspect tool calling
  - debug MCP tools
recommended_tools:
  - read_file
  - list_dir
  - run_command
capabilities:
  tool_calling: optional
---

# Go Agent Loop Review

Use this skill when reviewing an existing Go-based LLM agent.

## Workflow

1. Trace the complete model-and-tool loop end to end: request build,
   streaming events, tool-call extraction, execution, result delivery,
   continuation.
2. Verify message ordering: every assistant message that carries tool calls
   must be followed by one `role:"tool"` result per call before the next
   assistant turn.
3. Check tool-call identifiers: results must answer the exact IDs the
   assistant message carries; generated IDs must never collide within a
   session.
4. Check cancellation and timeouts: a cancelled stream must not leak
   goroutines, and a stalled tool call must be bounded by a timeout.
5. Review MCP routing: namespaced tool names, per-server approval modes,
   and result size caps.
6. Confirm the loop is bounded (a per-turn iteration budget) and that a
   denied tool call is reported back to the model instead of dead-ending.
7. Recommend deterministic tests for each finding: fake model scripts,
   fake tool executors, no live LLM required.

## Checklist

- [ ] One terminal event per stream, channel closed afterwards
- [ ] Tool results correlate by call ID
- [ ] Trimming/compaction never severs a call/result pair
- [ ] Approval prompts cannot be answered by an unrelated keypress
- [ ] Cache keys reflect everything that varies the request
