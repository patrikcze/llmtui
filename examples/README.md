# Starter skills and plugins

These examples are bundled for optional local use. They are inert by default:
no skill is activated and no plugin is enabled merely because these files are
present.

Launch `llmtui` from the extracted archive directory and add these paths to
your config to make the examples discoverable:

```yaml
skills:
  paths: ["./examples/skills"]
plugins:
  paths: ["./examples/plugins"]
```

Then use `/skills list` and `/plugins list` to inspect them. Activate a skill
with `/skills use <id>` or enable a plugin with `/plugins enable <id>`.

Skills are instructions, not permissions. Enabling or activating one does not
turn on workspace tools, web access, or MCP, and it does not bypass approvals.
