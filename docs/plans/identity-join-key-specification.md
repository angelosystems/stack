# Specification: Dreiräumiger Identity Join Key (Three-space Identity Join Key)

This specification maps the dynamic execution flow and links identities across five different spaces in our ecosystem:

1. **Space 1: OS Runtime (PID / cgroups / systemd Slice)**
2. **Space 2: Session Log (/var/log/vk-sessions.jsonl)**
3. **Space 3: Vibe-Kanban SQLite Database (db.v2.sqlite)**
4. **Space 4: Beads Dolt-Postgres (Port 5433)**
5. **Space 5: Master Kanban Board / Portfolio (Port 5434)**

---

## 1. The Core Mapping Chain

The complete identity chain connects a running Linux process back to its top-level initiative card on the portfolio kanban board:

$$	ext{PID} \xrightarrow{	ext{procfs CWD}} 	ext{Workspace Directory} \xrightarrow{	ext{vk-sessions.jsonl}} 	ext{Workspace UUID} \xrightarrow{	ext{SQLite workspaces}} 	ext{Bead ID} \xrightarrow{	ext{Postgres}} 	ext{Initiative Card}$$

### Space 1: OS Runtime
A running agent process is identified on the host by its Process ID (**PID**).
- **Cgroup/Slice resolution**: By reading `/proc/<PID>/cgroup`, we resolve the systemd slice (e.g. `user.slice` or `solartown.slice`) and determine the associated provider (e.g. `angeloos`, `solartown`, `stayawesome`, `quantbot`, `mariobrain`).
- **Working Directory resolution**: By reading the symlink `/proc/<PID>/cwd`, we retrieve the active worktree path (e.g. `/var/tmp/vibe-kanban/worktrees/1134-sol-st-4aibw/stack`).

### Space 2: Session Bridge (R2/R3/R4 Bridge)
Each workspace checkout and execution logs setup/stop events to `/var/log/vk-sessions.jsonl`. 
Because `/proc/<PID>/cwd` gives us the directory name `1134-sol-st-4aibw`, we bridge the gap by parsing `/var/log/vk-sessions.jsonl` for a JSON entry where the `"cwd"` field contains this directory name prefix. From this JSON log entry, we extract:
- **`workspace_id`**: The UUID of the workspace (e.g., `11343ecb-e5b2-4e2b-af17-2172b6e1a6b9`).
- **`branch`**: The git branch name (e.g., `vk/1134-sol-st-4aibw`).
- **`repo`**: The repository/rig directory name (e.g., `stack`).

### Space 3: Workspace Metadata (SQLite)
Using the resolved **Workspace UUID** (converted to its uppercase hexadecimal representation, e.g., `11343ECBE5B24E2BAF172172B6E1A6B9`), we query the Vibe-Kanban SQLite workspaces database under `/root/.local/share/vibe-kanban/db.v2.sqlite`:
```sql
SELECT hex(id), name, branch, archived FROM workspaces WHERE id = x'<HEX_UUID>';
```
This query returns the **Workspace Name** (e.g., `sol-st-4aibw`).
From the name, we extract the **Bead ID** (e.g., `st-4aibw`) using deterministic regex/prefix trimming:
- Prefix `sol-` is trimmed, or
- Anything inside square brackets `[...]` is extracted, or
- Fallback to the workspace name itself.

### Space 4: Beads Tracking (Postgres :5433)
The extracted **Bead ID** is used to query the Beads tracking database (backing Dolt repository on Port 5433) to determine the status of the bead:
```sql
SELECT title, status FROM beads.issues WHERE id = '<BEAD_ID>';
```

### Space 5: Master Kanban Board / Portfolio (Postgres :5434)
The **Bead ID** is mapped to its parent Initiative Card on the Master Kanban Board by querying the portfolio schema in the Postgres database on Port 5434:
```sql
-- Check if bead is linked to an initiative
SELECT initiative_id FROM portfolio.initiative_link WHERE ref = '<BEAD_ID>' AND kind = 'bead';

-- Retrieve initiative details
SELECT title, stage, firma FROM portfolio.initiative WHERE id = '<INITIATIVE_ID>';
```
If the bead is unlinked, it is tracked in `portfolio.unlinked_item` as part of the leak detector reporting.

---

## 2. Verification and Reproducibility

An end-to-end integration test is maintained in `tools/portfolio/master-kanban/identity_join_key_spike_test.go` to guarantee that this multi-space join-key remains stable and functional. It dynamically finds running workspaces on the host or spins up a temporary real background process to verify the complete pipeline under real runtime conditions.
