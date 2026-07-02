# Local Memory

Small, user-curated preference snippets stored in one YAML file
(`memory.path`). **Disabled by default** (`memory.enabled: false`) and
nothing is ever stored automatically.

- `/memory add Prefer concise Go examples.` — remember a preference
- `/memory list`, `/memory remove <id>`, `/memory clear`
- `/memory on` / `/memory off` — toggle for the session

When enabled, at most 3 snippets whose keywords overlap your message are
added to the prompt as a clearly labeled "Relevant Memory" section — never
the whole file. See exactly what is included with `/prompt preview`.

**Do not store secrets or sensitive personal data in memory.**
