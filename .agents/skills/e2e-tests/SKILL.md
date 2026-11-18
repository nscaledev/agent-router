---
name: e2e-tests
description: Run this PR's end-to-end suite on the local atlas stack. This checkout's Envoy AI Gateway controller and ext_proc images are swapped into the stack's Envoy AI Gateway, ai-gateway's live suite runs against them, and the result is written as the e2e-result.json the ship workflow publishes. Never writes to GitHub. Use when the ship workflow reaches e2e, when a retest is triggered after review feedback, or when the user asks to run the e2e checks.
---

# E2E tests

Two neighbours do most of the work, and this file adds only what is agent-router's own:

- **The atlas** (`nscaledev/ai-services-atlas`, its `e2e-tests` skill) owns the stack, the lock,
  the runner, classification and the result file.
- **ai-gateway** (`nscaledev/ai-gateway`, `scripts/test-k8s-integ.sh`) is how Nscale uses this
  fork in production. Its live suite deploys ai-gateway's chart on the stack's Envoy Gateway and
  Envoy AI Gateway, then drives the OpenAI-compatible and Anthropic routes through them. Here, that
  Envoy AI Gateway runs **this checkout**: controller and ext_proc images built from the working
  tree, swapped into the stack's controller for the run and restored afterwards.

Run these lines first, in the shell that runs the suite. They leave the atlas at `$ATLAS_DIR` and
ai-gateway at `$AI_GATEWAY_DIR`. Each is your own checkout when you set it, which is never pulled.
Otherwise it is a copy under `~/.cache/atlas`, cloned on first use and moved to `main` before every
run that starts while no other e2e run holds a lock.

```sh
e2e_checkout() { # repo; prints the copy's path, cloning or moving it to main first
  dir="$HOME/.cache/atlas/$1"
  if [ ! -d "$dir/.git" ]; then
    gh repo clone "nscaledev/$1" "$dir" -- --quiet ||
      echo "e2e: could not clone nscaledev/$1; run gh auth login" >&2
  # Moved to main only while no e2e run holds a lock, so a running suite never sees it change.
  elif ! find "${XDG_CACHE_HOME:-$HOME/.cache}/atlas" -maxdepth 1 -name 'e2e-*.lock' 2>/dev/null | grep -q .; then
    { git -C "$dir" -c credential.helper= -c credential.helper='!gh auth git-credential' fetch --quiet origin main &&
      git -C "$dir" checkout --quiet --detach FETCH_HEAD; } ||
      echo "e2e: $dir not updated; using it as it is" >&2
  fi
  echo "$dir"
}
: "${ATLAS_DIR:=$(e2e_checkout ai-services-atlas)}"
: "${AI_GATEWAY_DIR:=$(e2e_checkout ai-gateway)}"
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
ai-gateway's own e2e skill. agent-router is not an atlas component, so `up` brings the stack up from
the pins only.

```sh
E2E_NEEDS="envir-base-layer"
```

The base layer installs Envoy AI Gateway (pinned `v1.2.0`) as `aigateway-system/ai-gateway-controller`.
Its webhook injects the ext_proc sidecar into every Envoy pod carrying
`ai.nscale.com/product: ai-gateway`. So ai-gateway's script cannot install a second controller
beside it: it refuses, with a bootstrap failure. Instead, `swap_in_this_build` changes that
controller in one patch:

- the controller image becomes this checkout's;
- its `--extProcImage` argument names this checkout's ext_proc image, so every Envoy pod created
  afterwards gets this build's sidecar.

The run then calls the script with `AI_GATEWAY_INSTALL_MODE=existing` and `AI_GATEWAY_VERSION` set
to the images' tag. The script reuses only a controller whose image ends in that tag, so a run that
did not swap fails at bootstrap instead of passing against the pins. The script's
`using existing Envoy AI Gateway installation (aigateway-system:ai-gateway-controller)` line, and
the sidecar images `integ` records, show which build ran.

`aigw_restore` puts the controller back, and runs from `run.sh`'s EXIT trap. When the diff changes
the CRDs (`manifests/charts/ai-gateway-crds-helm`, `api/`), the swap also applies this checkout's
CRDs, which cannot be put back. The trap then deletes the cluster (`atlas down`), so the next run
starts from the pins.

In each case's `detail`, record the ai-gateway commit the suite ran from and the image tag.

## Target concurrency

**Mode:** machine-local and serialized

The atlas e2e lock and lease are taken by `e2e_preflight` within a bounded wait and released on
exit (atlas `e2e-tests`, "Target concurrency"). They also keep two runs of one PR off
`$E2E_STATE_DIR`. The swap changes a shared component of the cluster, so it happens only while
this run holds the cluster's e2e lock:

- **The controller's pinned spec** is saved per cluster before the swap, at
  `~/.cache/atlas/agent-router-e2e-<cluster>.json`.
- **`aigw_restore`** applies it from the EXIT trap, before the lease is released, and then deletes
  the file.
- **A run killed before its trap ran** leaves the swap and the file behind. The next
  `swap_in_this_build` on that cluster restores from the file before it swaps, so the pinned spec
  is never lost or overwritten.

The suite's namespace and release and the image tag derive from the sha, so a replay never meets
another run's resources.

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
`LIBRARY_CASES`, so the manifest records them. Arm the restore right after `e2e_preflight`:

```sh
e2e_preflight "ship e2e agent-router#<pr>"
trap 'aigw_restore; e2e_cleanup' EXIT # restore the pinned controller before the lease is released
```

```sh
LIBRARY_CASES="up build_this_checkout swap_in_this_build gateway_contract gateway_contract_nscale"
: "${AI_GATEWAY_DIR:=$HOME/.cache/atlas/ai-gateway}"
TAG="e2e-${E2E_SHA:0:7}"
IMG="localhost/agent-router-e2e/ai-gateway" # make names the images $IMG-controller:$TAG and $IMG-extproc:$TAG
AIGW_NS=aigateway-system                    # where the base layer installs Envoy AI Gateway
# The suite's namespace and release. ai-gateway's script treats the agw-e2e- prefix as its own when
# it cleans up after a failure; "ar" keeps it apart from ai-gateway's own runs of the same sha.
AGW="agw-e2e-ar-${E2E_SHA:0:7}"
aigw_saved() { echo "${XDG_CACHE_HOME:-$HOME/.cache}/atlas/agent-router-e2e-$CLUSTER.json"; }

# Builds this checkout's controller and ext_proc images for the node's architecture and loads them
# into the atlas's kind node. A pull or Docker fault is $INFRA; a compile or build error is the diff's.
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
  echo "built and loaded $IMG-controller:$TAG and $IMG-extproc:$TAG"
}

# A JSON patch giving the controller's first container the image, pull policy and arguments in $1's,
# a Deployment as JSON, with --extProcImage set to $2 when given.
aigw_patch() { # deployment.json image [extproc_image]
  jq -c --arg c "$2" --arg e "${3:-}" '.spec.template.spec.containers[0] as $k | [
    {op: "replace", path: "/spec/template/spec/containers/0/image", value: $c},
    {op: "replace", path: "/spec/template/spec/containers/0/imagePullPolicy",
     value: (if $e == "" then $k.imagePullPolicy else "IfNotPresent" end)},
    {op: "replace", path: "/spec/template/spec/containers/0/args", value: ($k.args | map(
      if $e != "" and startswith("--extProcImage=") then "--extProcImage=" + $e
      elif $e != "" and startswith("--extProcImagePullPolicy=") then "--extProcImagePullPolicy=IfNotPresent"
      else . end))}]' "$1"
}

# Puts the base layer's controller back from the saved spec. From the EXIT trap, and before a swap
# when a killed run left one behind. Deletes the cluster when this run applied its own CRDs.
aigw_restore() {
  local saved
  [[ -n $CLUSTER ]] || return 0
  saved="$(aigw_saved)"
  if [[ -s $saved ]]; then
    if $KUBECTL -n "$AIGW_NS" patch deploy ai-gateway-controller --type=json \
      -p "$(aigw_patch "$saved" "$(jq -r '.spec.template.spec.containers[0].image' "$saved")")" >/dev/null &&
      $KUBECTL -n "$AIGW_NS" rollout status deploy/ai-gateway-controller --timeout=300s >/dev/null; then
      rm -f "$saved"
      echo "restored $AIGW_NS/ai-gateway-controller to the pins"
    else
      echo "could not restore $AIGW_NS/ai-gateway-controller; the spec to restore is kept at $saved" >&2
    fi
  fi
  if [[ -f $RUN_DIR/aigw-crds-applied ]]; then
    echo "this run applied this checkout's CRDs, which cannot be put back: deleting cluster $CLUSTER"
    atlas down || true
  fi
}

# Swaps this checkout's images into the base layer's controller (see "Target resolution").
case_swap_in_this_build() {
  local saved
  saved="$(aigw_saved)"
  $KUBECTL -n "$AIGW_NS" get deploy ai-gateway-controller >/dev/null 2>&1 ||
    { echo "no Envoy AI Gateway controller in $AIGW_NS: the base layer is not up"; return "$INFRA"; }
  [[ -s $saved ]] && aigw_restore # a killed run's swap, or this case's first attempt: start from the pins
  $KUBECTL -n "$AIGW_NS" get deploy ai-gateway-controller -o json >"$saved.tmp" && mv "$saved.tmp" "$saved" || return "$INFRA"
  [[ $(jq -r '.spec.template.spec.containers[0].image' "$saved") != "$IMG"-* ]] ||
    { rm -f "$saved"; echo "the controller already runs an agent-router e2e image and no pinned spec is saved; run atlas down"; return "$INFRA"; }
  jq -e '.spec.template.spec.containers[0].args | any(startswith("--extProcImage="))' "$saved" >/dev/null ||
    { echo "the controller has no --extProcImage argument; it is not the chart this skill knows"; return "$INFRA"; }
  if ! git diff --quiet "${E2E_BASE_SHA:-origin/main}" "$E2E_SHA" -- manifests/charts/ai-gateway-crds-helm api; then
    touch "$RUN_DIR/aigw-crds-applied"
    helm template aigw-crds manifests/charts/ai-gateway-crds-helm | $KUBECTL apply --server-side --force-conflicts -f - || return 1
  fi
  $KUBECTL -n "$AIGW_NS" patch deploy ai-gateway-controller --type=json \
    -p "$(aigw_patch "$saved" "$IMG-controller:$TAG" "$IMG-extproc:$TAG")" || return 1
  # This build's controller failing to roll out is the diff's.
  $KUBECTL -n "$AIGW_NS" rollout status deploy/ai-gateway-controller --timeout=300s || return 1
  echo "swapped $IMG-controller:$TAG and $IMG-extproc:$TAG into $AIGW_NS/ai-gateway-controller"
}

# ai-gateway's test-k8s-integ.sh against the swapped controller. Output goes to stdout and to
# $RUN_DIR/integ-<label>.log, so a PR-specific case can assert on what the suite printed. While it
# runs, a watcher records the image of every ai-gateway-extproc sidecar injected into the suite's
# Envoy pods to $RUN_DIR/integ-<label>.extproc. $INFRA only for a fault named in the environment:
# a missing tool, Docker or buildx down, the private base unreadable, the atlas kubeconfig missing,
# or one of the script's markers (a bootstrap failure, or a registry rate limit). Anything else is
# the diff's.
integ() { # label recipe [args...]
  local label=$1 c status watcher log="$RUN_DIR/integ-$1.log" seen="$RUN_DIR/integ-$1.extproc"
  shift
  for c in just docker kind go helm kubectl curl jq yq; do
    command -v "$c" >/dev/null || { echo "missing required command: $c"; return "$INFRA"; }
  done
  docker buildx version >/dev/null 2>&1 || { echo "missing docker buildx plugin"; return "$INFRA"; }
  [[ -f $KUBECONFIG_PATH ]] || { echo "atlas kubeconfig missing: $KUBECONFIG_PATH"; return "$INFRA"; }
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
    AI_GATEWAY_INSTALL_MODE=existing AI_GATEWAY_VERSION="$TAG" \
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

Run `build_this_checkout` and then `swap_in_this_build` after `up`, before any case that calls `integ`.

`test-k8s-integ` runs the whole suite. To run one group of it, set `K8S_INTEG_GROUP`, which
`integ` passes through to the script like `GATEWAY_AUTH_MODE`. That group runs after the same setup
as the whole suite. The groups are listed in the script's usage text
(`$AI_GATEWAY_DIR/scripts/test-k8s-integ.sh --help`). The billing and Anthropic checks run in
`core` under `GATEWAY_AUTH_MODE=nscale`.

| case | what it proves | how |
|---|---|---|
| `build_this_checkout` | the controller and ext_proc images build from this checkout and load into the node | `run_case build_this_checkout "..."` |
| `swap_in_this_build` | this checkout's controller rolls out in place of the pinned one, with this build's ext_proc as the sidecar it injects | `run_case swap_in_this_build "..."` |
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
- **Controller or webhook changes.** `swap_in_this_build` fails if this build's controller does
  not roll out, and `gateway_contract` fails if the webhook does not inject the sidecar or Envoy
  pods are rejected. `injected_this_build` proves which sidecar ran.
- **Chart changes** (`manifests/charts/ai-gateway-helm`). The swap changes images and the
  ext_proc image argument only, and the base layer's chart stays pinned. A criterion about
  templates or values is a `gap` naming this repo's `make helm-test` and `make test-e2e`, which
  install this checkout's chart.
- **CRD changes.** The swap applies this checkout's CRDs, so `gateway_contract` proves the
  swapped controller serves ai-gateway's resources with them. A CRD that ai-gateway does not
  create is a `gap` naming this repo's `make test-crdcel` and `make test-controller`.
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
  - this build's controller failing to roll out in the swap;
  - the webhook not injecting the sidecar, or injecting another image;
  - a request through Envoy returning the wrong status or body.

  These are the diff's even when the script reports a bootstrap failure. Its existing-mode check
  also covers whether the controller and its webhook are ready, so when the log shows that check
  failing after a successful swap (`AI_GATEWAY_INSTALL_MODE=existing requires ...`), rule it
  `real`, not `infra`.
- **ai-gateway's own chart failing** to build or install, when the diff does not change what it
  consumes, is `infra`. Say so in `detail`.

## Diagnostic snapshot on failure

`run_case` appends `atlas status`. Inside the failing case, add the swapped controller's state
before the trap restores it:

```sh
$KUBECTL -n "$AIGW_NS" get pods -o wide
$KUBECTL -n "$AIGW_NS" logs deploy/ai-gateway-controller --tail=200
$KUBECTL get pods -A -l "gateway.envoyproxy.io/owning-gateway-namespace=$AGW" -o wide
```

## Running and the result

As the atlas skill says: write `run.sh` and `manifest.json` from its scaffold, run from this
checkout (the image build and the CRD chart read `$PWD`), and let `write_result` write the
e2e-result file to `$E2E_RESULT_PATH`. Do not post anything: the workflow validates and publishes
the file.

## Not proven here

- **Only the images are this checkout's.** The controller's Deployment, webhook and RBAC are the
  base layer's pinned chart (`v1.2.0`). Neither this checkout's chart nor the fork release Nscale
  deploys from Harbor is exercised.
- **The backend is ai-gateway's mock.** Its usage figures are canned, and real engine output
  (vLLM, SGLang) is not exercised.

## Evidence integrity

Never cite gitignored or local-only content in `detail` as if a reviewer could open it: that
includes anything under `$E2E_STATE_DIR`, `$RUN_DIR` and the script's temp files. Cite committed
sources or the shown output of a command.
