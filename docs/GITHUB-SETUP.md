# GitHub repository setup

**Target:** public repository `nakshjhaai-lgtm/cosmos-tlm-bridge`  
**Topics:** `openc3`, `cosmos`, `satellite-telemetry`, `ground-software`, `satellite-operations`

## Status in this sandbox

`gh auth status` succeeds and writing to the session branch `arena/ba799aff-for-turion` works. However, the supplied GitHub App installation token has only contents access to `For-turion`; repository creation, renaming, topic replacement and template generation all return:

```
HTTP 403: Resource not accessible by integration
```

Concretely, these were attempted and blocked: `POST /user/repos`, `POST /repos/{owner}/{repo}/generate`, `PATCH /repos/{owner}/{repo}`, `POST /repos/{owner}/{repo}/forks`, `PUT /repos/{owner}/{repo}/topics`, and the GraphQL `createRepository` mutation. No repository named `cosmos-tlm-bridge` exists yet.

**Final attempt (step 3, 2026-11-03):** `bash scripts/create-github-repo.sh` returned

```
GraphQL: Resource not accessible by integration (createRepository)
```

Because repository creation is refused, the five-topic assignment was not reached. The GitHub Actions workflow registrations for the temporary fetch/evidence jobs also cannot be deleted with this token (DELETE returns 404), but they are absent from the clean working tree and branch history.

## What to do

1. In Arena, reconnect/enable GitHub with repository-creation and administration permission for `nakshjhaai-lgtm`.
2. From this repository root, run:

   ```bash
   bash scripts/create-github-repo.sh
   ```

   The script creates the public repo, pushes the current clean tree, and applies all five topics. It is idempotent: if the repo already exists it only adds missing topics.

This is the only requested step blocked by the current GitHub connection. All source, CI, evidence and README work is present on the session branch and ready to push.
