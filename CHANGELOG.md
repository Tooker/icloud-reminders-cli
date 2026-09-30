# Changelog

## 1.1.0 — 2026-10-01

- Create/discover native list sections and preserve their JSON metadata assets.
- Read reminders in manual section/hierarchy order, optionally as a tree.
- Move existing reminders under parents or into sections; reorder siblings
  without separating descendants.
- Add `sections`, `move`, `reorder` CLI commands and `list --legend`; document
  priority marks, status marks, indentation and manual drag handles for LLMs.
- Preserve original shared-owner zones and unknown metadata, check current
  permissions, reject cycles and foreign-list references, and rebuild old caches.
- Embed 1.1.0 in local/Docker builds and advertise all fourteen typed MCP tools.
