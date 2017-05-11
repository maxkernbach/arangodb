//
// DISCLAIMER
//
// Copyright 2017 ArangoDB GmbH, Cologne, Germany
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Copyright holder is ArangoDB GmbH, Cologne, Germany
//
// Author Ewout Prangsma
//

package test

import (
	"os"
	"testing"
	"time"
)

// TestProcessClusterLocal runs `arangodb --starter.local`
func TestProcessClusterLocal(t *testing.T) {
	needTestMode(t, testModeProcess)
	dataDir := SetUniqueDataDir(t)
	defer os.RemoveAll(dataDir)

	start := time.Now()

	child := Spawn(t, "${STARTER} --starter.local")
	defer child.Close()

	if ok := WaitUntilStarterReady(t, whatCluster, child); ok {
		t.Logf("Cluster start took %s", time.Since(start))
		testCluster(t, insecureStarterEndpoint(0), false)
		testCluster(t, insecureStarterEndpoint(5), false)
		testCluster(t, insecureStarterEndpoint(10), false)
	}

	if isVerbose {
		t.Log("Waiting for termination")
	}
	SendIntrAndWait(t, child)
}

// TestProcessClusterLocal runs `arangodb --starter.local`, stopping it through the `/shutdown` API.
func TestProcessClusterLocalShutdownViaAPI(t *testing.T) {
	needTestMode(t, testModeProcess)
	dataDir := SetUniqueDataDir(t)
	defer os.RemoveAll(dataDir)

	start := time.Now()

	child := Spawn(t, "${STARTER} --starter.local")
	defer child.Close()

	if ok := WaitUntilStarterReady(t, whatCluster, child); ok {
		t.Logf("Cluster start took %s", time.Since(start))
		testCluster(t, insecureStarterEndpoint(0), false)
		testCluster(t, insecureStarterEndpoint(5), false)
		testCluster(t, insecureStarterEndpoint(10), false)
	}

	if isVerbose {
		t.Log("Waiting for termination")
	}
	ShutdownStarter(t, insecureStarterEndpoint(0))
}

// TestOldProcessClusterLocal runs `arangodb --local`
func TestOldProcessClusterLocal(t *testing.T) {
	needTestMode(t, testModeProcess)
	dataDir := SetUniqueDataDir(t)
	defer os.RemoveAll(dataDir)

	start := time.Now()

	child := Spawn(t, "${STARTER} --local")
	defer child.Close()

	if ok := WaitUntilStarterReady(t, whatCluster, child); ok {
		t.Logf("Cluster start took %s", time.Since(start))
		testCluster(t, insecureStarterEndpoint(0), false)
		testCluster(t, insecureStarterEndpoint(5), false)
		testCluster(t, insecureStarterEndpoint(10), false)
	}

	if isVerbose {
		t.Log("Waiting for termination")
	}
	SendIntrAndWait(t, child)
}