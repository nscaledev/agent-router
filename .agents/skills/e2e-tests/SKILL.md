---
name: e2e-tests
description: Run this PR's end-to-end suite on the local atlas stack. This checkout's Envoy AI Gateway controller and ext_proc images, and its chart, are installed as the gateway ai-gateway's live suite runs against, and the result is written as the e2e-result.json the ship workflow publishes. Never writes to GitHub. Use when the ship workflow reaches e2e, when a retest is triggered after review feedback, or when the user asks to run the e2e checks.
---

# E2E tests

Two neighbours do most of the work, and this file adds only what is agent-router's own:

- **The atlas** (`nscaledev/ai-services-atlas`, its `e2e-tests` skill) owns the stack, the lock,
  the runner, classification and the result file.
- **ai-gateway** (`nscaledev/ai-gateway`, `scripts/test-k8s-integ.sh`) is how Nscale uses this
  fork in production. Its live suite installs a temporary Envoy Gateway and Envoy AI Gateway beside
  its own chart, then drives the OpenAI-compatible and Anthropic routes through them. Here, that
  temporary Envoy AI Gateway is **this checkout**: its chart, and controller and ext_proc images
  built from the working tree.

Run these lines first, in the shell that runs the suite. They leave the atlas at `$ATLAS_DIR` and
ai-gateway at `$AI_GATEWAY_DIR`. Each is your own checkout when you set it, which is never pulled.
Otherwise it is a copy under `~/.cache/atlas`, cloned on first use and moved to `main` before every
run that starts while no other e2e run holds a lock.

```sh
for repo in ai-services-atlas ai-gateway; do
  var=ATLAS_DIR; [[ $repo == ai-gateway ]] && var=AI_GATEWAY_DIR
  [[ -n ${!var:-} ]] && continue
  dir="$HOME/.cache/atlas/$repo"
  if [ ! -d "$dir/.git" ]; then
    gh repo clone "nscaledev/$repo" "$dir" -- --quiet ||
      echo "e2e: could not clone nscaledev/$repo; run gh auth login" >&2
  # Moved to main only while no e2e run holds a lock, so a running suite never sees it change.
  elif ! find "${XDG_CACHE_HOME:-$HOME/.cache}/atlas" -maxdepth 1 -name 'e2e-*.lock' 2>/dev/null | grep -q .; then
    { git -C "$dir" -c credential.helper= -c credential.helper='!gh auth git-credential' fetch --quiet origin main &&
      git -C "$dir" checkout --quiet --detach FETCH_HEAD; } ||
      echo "e2e: $dir not updated; using it as it is" >&2
  fi
  printf -v "$var" '%s' "$dir"
done
export ATLAS_DIR AI_GATEWAY_DIR
```

Then read `$ATLAS_DIR/.agents/skills/e2e-tests/SKILL.md` and follow it.

Everything here runs against a **live cluster**. Translator behaviour that a request and response
pair can show belongs in this repo's unit and data-plane tests (`make test`,
`make test-data-plane`). It is an e2e case only when it needs a real controller, a real Envoy
with the injected ext_proc sidecar, or a real consumer such as ai-gateway's processor and billing.

## Cheapest proof first

Before taking the stack, build what the run will deploy and run the fast tests the diff touches:

```sh
go build ./... && go test ./internal/translator/... # or whichever packages the diff touches
```

A compile error or a failing unit test there is `real`, and needs no cluster.

## Target resolution

The atlas stack (atlas `e2e-tests`, "Target resolution") with ai-gateway's neighbours, the same as
ai-gateway's own e2e skill. agent-router is not an atlas component, so `up` only brings the stack
up from the pins. The base layer does not install Envoy AI Gateway on the atlas, so the cluster has
no Envoy AI Gateway controller of its own. ai-gateway's script then installs a temporary one, plus
a temporary Envoy Gateway wired to it, and tears both down afterwards.

```sh
E2E_NEEDS="envir-base-layer"
```

This checkout reaches that temporary install through three of the script's own inputs:

- `AI_GATEWAY_CHART_REF`: a copy of `manifests/charts/ai-gateway-helm` whose `values.yaml` names
  the images built from this checkout.
- `AI_GATEWAY_CRDS_CHART_REF`: `manifests/charts/ai-gateway-crds-helm` from this checkout.
- `AI_GATEWAY_INSTALL_MODE=temp`, so an existing controller is never reused in its place.

`AI_GATEWAY_VERSION` is set to the images' tag, so the script's
`installing temporary Envoy AI Gateway (<tag>, ...)` line names this build.

In each case's `detail`, record the ai-gateway commit the suite ran from and the image tag.

## Target concurrency

**Mode:** machine-local and serialized

The atlas e2e lock and lease are taken by `e2e_preflight` within a bounded wait and released on
exit (atlas `e2e-tests`, "Target concurrency"). They also keep two runs of one PR off
`$E2E_STATE_DIR`. The suite's namespace and release, the temporary controller's namespace
(`<namespace>-aigateway-system`), the image tag and the chart copy all derive from the sha. The
chart copy lives in the run's own temporary directory. So a replay never meets another run's
resources.

## Pre-flight checks

These are on top of the atlas's own:

- **Tools:** `just docker kind go helm kubectl curl jq yq`, plus Docker buildx. A missing one is
  `infra`.
- **Image bases:** this repo's images pull `golang` and `gcr.io/distroless`. ai-gateway's images
  pull a private Chainguard base, so Docker must be logged in to `registry.nscale.com`.
- **Harbor:** the atlas needs its Harbor login (`atlas login`).

The helpers below check each of these and return `$INFRA` when one is missing.

## Reusable coverage

Paste the helpers into `run.sh` after sourcing the library. List every library case it calls in
`LIBRARY_CASES`, so the manifest records them.

```sh
LIBRARY_CASES="up build_this_checkout gateway_contract gateway_contract_nscale"
TAG="e2e-${E2E_SHA:0:7}"
IMG="localhost/agent-router-e2e/ai-gateway" # make names the images $IMG-controller:$TAG and $IMG-extproc:$TAG
# The suite's namespace and release. ai-gateway's script treats the agw-e2e- prefix as its own when
# it cleans up after a failure; "ar" keeps it apart from ai-gateway's own runs of the same sha.
AGW="agw-e2e-ar-${E2E_SHA:0:7}"
CHART="$RUN_DIR/ai-gateway-helm"

# Builds this checkout's controller and ext_proc images for the node's architecture, loads them into
# the atlas's kind node, and writes a chart copy that names them. A pull or Docker fault is $INFRA;
# a compile or build error is the diff's.
case_build_this_checkout() {
  local arch cmd out
  arch="$(docker info --format '{{.Architecture}}')" || { echo "Docker is not running"; return "$INFRA"; }
  case $arch in aarch64 | arm64) arch=arm64 ;; x86_64 | amd64) arch=amd64 ;; *) echo "unsupported node architecture $arch"; return "$INFRA" ;; esac
  for cmd in controller extproc; do
    out="$(make "docker-build.$cmd" GOARCH_LIST="$arch" OCI_REPOSITORY_PREFIX="$IMG" TAG="$TAG" \
      DOCKER_BUILD_ARGS="--load --platform linux/$arch" 2>&1)" || {
      echo "$out" | tail -40
      grep -qE 'failed to resolve source metadata|pull access denied|toomanyrequests|Cannot connect to the Docker daemon|no space left on device' <<<"$out" && return "$INFRA"
      return 1
    }
    kind load docker-image "$IMG-$cmd:$TAG" --name "$CLUSTER" || return "$INFRA"
  done
  rm -rf "$CHART" && cp -R manifests/charts/ai-gateway-helm "$CHART"
  yq -i ".controller.image.repository = \"$IMG-controller\" | .controller.image.tag = \"$TAG\" |
    .controller.imagePullPolicy = \"IfNotPresent\" | .extProc.image.repository = \"$IMG-extproc\" |
    .extProc.image.tag = \"$TAG\" | .extProc.imagePullPolicy = \"IfNotPresent\"" "$CHART/values.yaml"
  echo "built $IMG-controller:$TAG and $IMG-extproc:$TAG; chart $CHART"
}

# ai-gateway's test-k8s-integ.sh against this checkout's Envoy AI Gateway. Output goes to stdout and
# to $RUN_DIR/integ-<label>.log, so a PR-specific case can assert on what the suite printed. While it
# runs, a watcher records the image of every ai-gateway-extproc sidecar injected into the suite's
# Envoy pods to $RUN_DIR/integ-<label>.extproc. $INFRA only for a fault named in the environment:
# a missing tool, Docker or buildx down, the private base unreadable, the atlas kubeconfig missing,
# or one of the script's markers (a bootstrap failure, or a registry rate limit). Anything else is
# the diff's.
integ() { # label recipe [args...]
  local label=$1 c status watcher repo=$PWD log="$RUN_DIR/integ-$1.log" seen="$RUN_DIR/integ-$1.extproc"
  shift
  for c in just docker kind go helm kubectl curl jq yq; do
    command -v "$c" >/dev/null || { echo "missing required command: $c"; return "$INFRA"; }
  done
  docker buildx version >/dev/null 2>&1 || { echo "missing docker buildx plugin"; return "$INFRA"; }
  [[ -f $KUBECONFIG_PATH ]] || { echo "atlas kubeconfig missing: $KUBECONFIG_PATH"; return "$INFRA"; }
  [[ -f $CHART/values.yaml ]] || { echo "no chart copy at $CHART: run build_this_checkout first"; return 1; }
  docker buildx imagetools inspect "$(awk '/^FROM /{print $2; exit}' "$AI_GATEWAY_DIR/deploy/processor/Dockerfile")" >/dev/null ||
    { echo "cannot read the Chainguard base; log Docker in to registry.nscale.com"; return "$INFRA"; }
  echo "ai-gateway $(git -C "$AI_GATEWAY_DIR" rev-parse --short HEAD), Envoy AI Gateway $TAG"
  : >"$seen"
  while sleep 10; do
    $KUBECTL get pods -A -l "gateway.envoyproxy.io/owning-gateway-namespace=$AGW" -o json 2>/dev/null |
      jq -r '.items[].spec | ((.initContainers // []) + .containers)[] | select(.name == "ai-gateway-extproc") | .image' >>"$seen"
  done &
  watcher=$!
  (cd "$AI_GATEWAY_DIR" && KIND_CLUSTER_NAME="$CLUSTER" STALE_TEST_WEBHOOK_NAMESPACE_REGEX='^(ai-gateway-auth-matrix|agwm|agw-mx|agw-e2e-)' \
    AI_GATEWAY_INSTALL_MODE=temp AI_GATEWAY_VERSION="$TAG" AI_GATEWAY_CHART_REF="$CHART" \
    AI_GATEWAY_CRDS_CHART_REF="$repo/manifests/charts/ai-gateway-crds-helm" \
    just "$@" "$KUBECONFIG_PATH" "$AGW" "$AGW") >"$log" 2>&1
  status=$?
  kill "$watcher" 2>/dev/null
  sort -u -o "$seen" "$seen"
  cat "$log"
  echo "ext_proc images injected into the suite's Envoy pods: $(paste -sd, "$seen")"
  ((status == 0)) && return 0
  grep -qE 'test-k8s-integ: (environment bootstrap failed|registry rate limit hit)' "$log" && return "$INFRA"
  return 1
}

# The suite's sidecars must all be this build's: a run on any other ext_proc proves nothing here.
injected_this_build() { # label
  [[ -s $RUN_DIR/integ-$1.extproc ]] || { echo "no ai-gateway-extproc sidecar was observed"; return 1; }
  [[ $(cat "$RUN_DIR/integ-$1.extproc") == "$IMG-extproc:$TAG" ]] ||
    { echo "unexpected ext_proc images: $(paste -sd, "$RUN_DIR/integ-$1.extproc")"; return 1; }
}

case_gateway_contract() { integ default test-k8s-integ && injected_this_build default; }
case_gateway_contract_nscale() { GATEWAY_AUTH_MODE=nscale integ nscale test-k8s-integ && injected_this_build nscale; }
```

Run `build_this_checkout` after `up`, and before any case that calls `integ`.

`test-k8s-integ` runs the whole suite. To run one group of it, set `K8S_INTEG_GROUP`, which
`integ` passes through to the script like `GATEWAY_AUTH_MODE`. That group runs after the same setup
as the whole suite. The groups are listed in the script's usage text
(`$AI_GATEWAY_DIR/scripts/test-k8s-integ.sh --help`). The billing and Anthropic checks run in
`core` under `GATEWAY_AUTH_MODE=nscale`.

| case | what it proves | how |
|---|---|---|
| `build_this_checkout` | the controller and ext_proc images build from this checkout and load into the node | `run_case build_this_checkout "..."` |
| `gateway_contract` | ai-gateway's full OpenAI-compatible surface through this build's controller and injected ext_proc, with the sidecar image checked | `integ default test-k8s-integ` |
| `gateway_contract_nscale` | the above, plus ext_auth, billing publication over NATS, and `POST /anthropic/v1/messages` converted to chat completions and back, streaming and not, with the mock reporting 4 cached of 10 prompt tokens | `GATEWAY_AUTH_MODE=nscale integ nscale test-k8s-integ` |

The endpoint contract checker is `$AI_GATEWAY_DIR/tests/k8sinteg/cmd/check`. Read it before
claiming a surface is uncovered. It prints every response it checks as
`POST <path> (<label>) => <body> [status=<code>]`. A full run, including both repos' image builds,
takes roughly 15-25 minutes on a warm stack. Do not treat a slow run as a hang before 30 minutes.

## PR-specific suite

As the atlas skill says, write one case per acceptance criterion. A library case never counts as
the PR-specific case on its own. Pair it with a `case_<name>` of this PR's own, which usually
asserts on what `integ` printed for the criterion's route.

```sh
# An example criterion: an Anthropic Messages response reports the backend's cached prompt tokens
# (Anthropic input_tokens is the UNCACHED part). Asserts on the body the checker printed for the
# non-streaming request; the mock reports 4 cached of 10 prompt tokens. The checker asserts the
# converter's usage itself, so it must expect the same split before this case can pass.
case_anthropic_cached_usage() {
  GATEWAY_AUTH_MODE=nscale K8S_INTEG_GROUP=core integ anthropic test-k8s-integ && injected_this_build anthropic || return
  grep -F 'POST /anthropic/v1/messages (billing publication, stream=false) =>' "$RUN_DIR/integ-anthropic.log" |
    grep -qF '"cache_read_input_tokens":4,"input_tokens":6'
}
```

Guidance for the diff shapes this repository produces:

- **Translator, ext_proc or filter changes.** Find the checker's request for the changed route and
  assert on its printed body or status. When the checker itself asserts the behaviour the diff
  changes, it fails the run. That is a stale assertion in ai-gateway: rule it per ship's e2e node
  (name the superseding record, prove the property here), and land the checker change in
  ai-gateway. Never weaken this suite to pass.
- **Controller, webhook or chart changes.** `gateway_contract` already fails if the controller
  does not roll out, the webhook does not inject the sidecar, or Envoy pods are rejected.
  `injected_this_build` proves which sidecar ran. Add a case for any new values key, set to a
  non-default value with `yq` on `$CHART/values.yaml` in that case.
- **CRD changes.** The CRD chart comes from this checkout, so `gateway_contract` proves the
  temporary install accepts them. A CRD that ai-gateway does not create is a `gap` naming this
  repo's `make test-crdcel` and `make test-controller`.
- **Upstream-only surfaces** (providers Nscale does not route to: AWS, GCP, Azure, MCP). Not
  provable here: a `gap` naming the data-plane or e2e test in this repo that covers it.
- **Docs, comments, CI or formatting only.** An explicit skip per criterion, as the atlas skill
  says.

Use `$AGW` for any namespace a case creates, so a replay is safe.

## Classification

As the atlas skill says, with these lines for agent-router:

- **`infra`:**
  - a missing tool, Docker down, or a base image that cannot be pulled;
  - the Chainguard base unreadable, or the atlas kubeconfig missing;
  - the script printing `test-k8s-integ: environment bootstrap failed` or
    `test-k8s-integ: registry rate limit hit`.

  `case_build_this_checkout` and `integ` return `$INFRA` for each.
- **`real`:**
  - this checkout's images failing to compile or build;
  - the temporary Envoy AI Gateway failing to install or roll out;
  - the webhook not injecting the sidecar, or injecting another image;
  - a request through Envoy returning the wrong status or body.

  These are the diff's even though the script reports a failed temporary install as a bootstrap
  failure. When the log shows the temporary Envoy AI Gateway install or its rollout failed, rule it
  `real`, not `infra`.
- **ai-gateway's own chart failing** to build or install, when the diff does not change what it
  consumes, is `infra`. Say so in `detail`.

## Diagnostic snapshot on failure

`run_case` appends `atlas status`. Add the temporary controller's state inside the failing case:

```sh
$KUBECTL -n "$AGW-aigateway-system" get pods -o wide
$KUBECTL -n "$AGW-aigateway-system" logs deploy/ai-gateway-controller --tail=200
$KUBECTL get pods -A -l "gateway.envoyproxy.io/owning-gateway-namespace=$AGW" -o wide
```

Set `KEEP_TEMP_BOOTSTRAP_ON_FAILURE=true` when the temporary install has to survive for
inspection.

## Running and the result

As the atlas skill says: write `run.sh` and `manifest.json` from its scaffold, run from this
checkout (the image build and chart copy read `$PWD`), and let `write_result` write the
e2e-result file to `$E2E_RESULT_PATH`. Do not post anything: the workflow validates and publishes
the file.

## Not proven here

- **The gateway is a temporary install.** It is not the fork release Nscale deploys from Harbor,
  and production chart values (replicas, resources, pod security) are ai-gateway's, not exercised
  here.
- **The backend is ai-gateway's mock.** Its usage figures are canned, and real engine output
  (vLLM, SGLang) is not exercised.

## Evidence integrity

Never cite gitignored or local-only content in `detail` as if a reviewer could open it: that
includes anything under `$E2E_STATE_DIR`, `$RUN_DIR` and the script's temp files. Cite committed
sources or the shown output of a command.
