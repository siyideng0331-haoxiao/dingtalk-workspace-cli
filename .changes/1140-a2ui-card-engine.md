---
category: Added
---

- **Chat A2UI cards** (#1140) — adds `chat message send-a2ui-card` and
  `chat message update-a2ui-card` as dedicated A2UI commands while preserving
  the existing streaming card commands. A2UI content is delivered as a JSON
  string array, and update status accepts enum names plus compatible numbers
  1-9.
