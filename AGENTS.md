# CLI releases

For requests to prepare, publish, verify, or recover a CLI release, read and
follow `.agents/skills/doomer-release/SKILL.md` and `docs/release.md`.

Release completion includes GitHub, public downloads, Homebrew, and updating
the public installer skill in `doomerlabs/skills`. New stable versions always
use `YYYY.M.D.N`, beginning with `.1` each day. Never modify the user's installed
CLI as part of publishing. Do not commit `scripts/stage-skill-dev.py`.
