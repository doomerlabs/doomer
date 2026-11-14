# Shared agent E2E

The suite implementation and detailed design documentation moved to [doomerlabs/agent-e2e](https://github.com/doomerlabs/agent-e2e).

See the [runner guide](https://github.com/doomerlabs/agent-e2e/blob/8b1ea155c53d1b77073c818390141894c736c147/README.md) and [preserved design documentation](https://github.com/doomerlabs/agent-e2e/blob/8b1ea155c53d1b77073c818390141894c736c147/docs/agent-e2e-cache.md). Jev selection, conservative dependency fingerprints, signed R2 reuse and recordings remain part of the shared harness.

This repository retains the local delegator `scripts/agent-e2e-local-linux.sh`, the `.agents/skills/agent-e2e/SKILL.md` instructions, and the consumer CI workflow. The delegator runs the shared checkout; do not remove these integration entry points.
