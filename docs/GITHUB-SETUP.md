# GitHub repository setup

Target repository: `nakshjhaai-lgtm/cosmos-tlm-bridge`, public.

Topics: `openc3`, `cosmos`, `satellite-telemetry`, `ground-software`, `satellite-operations`.

1. Current status

`gh auth status` succeeds, and writing to the session branch `arena/ba799aff-for-turion` works. The supplied GitHub App installation token only has contents access to `For-turion`. Repository creation, renaming, topic replacement, and template generation all return:

```
HTTP 403: Resource not accessible by integration
```

The blocked calls were `POST /user/repos`, `POST /repos/{owner}/{repo}/generate`, `PATCH /repos/{owner}/{repo}`, `POST /repos/{owner}/{repo}/forks`, `PUT /repos/{owner}/{repo}/topics`, and the GraphQL `createRepository` mutation. No repository named `cosmos-tlm-bridge` exists yet.

The last attempt on 2026-11-03 ran `bash scripts/create-github-repo.sh` and returned:

```
GraphQL: Resource not accessible by integration (createRepository)
```

Because repository creation was refused, the five-topic assignment was never reached. The GitHub Actions workflow registrations for the temporary fetch and evidence jobs also cannot be deleted with this token; DELETE returns 404. They are absent from the clean working tree and branch history.

2. What to do

1. In Arena, reconnect or enable GitHub with repository-creation and administration permission for `nakshjhaai-lgtm`.
2. From this repository root, run:

   ```bash
   bash scripts/create-github-repo.sh
   ```

   The script creates the public repo, pushes the current clean tree, and applies all five topics. If the repo already exists, it only adds missing topics.

This is the only requested step blocked by the current GitHub connection. Source, CI, evidence, and README work is on the session branch and ready to push.
