// +build examples

/*
Copyright 2020 The Tekton Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package test

import (
	"errors"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	knativetest "knative.dev/pkg/test"
)

var (
	pipelineRunTimeout = 10 * time.Minute
)

const (
	DEFAULT_KO_DOCKER_REPO = `gcr.io\/christiewilson-catfactory`
	DEFAULT_NAMESPACE      = `namespace: default`
)

// GetCreatedTektonCrd parses output of an external ko invocation provided as
// input, as is the kind of Tekton CRD to search for (ie. taskrun)
func GetCreatedTektonCrd(input []byte, kind string) (string, error) {
	re := regexp.MustCompile(kind + `.tekton.dev\/(.+) created`)
	submatch := re.FindSubmatch(input)
	if submatch == nil || len(submatch) < 2 {
		return "", nil
	}
	return string(submatch[1]), nil
}

func waitValidatePipelineRunDone(t *testing.T, c *clients, pipelineRunName string) {
	err := WaitForPipelineRunState(c, pipelineRunName, pipelineRunTimeout, Succeed(pipelineRunName), pipelineRunName)

	if err != nil {
		t.Fatalf("Failed waiting for pipeline run done: %v", err)
	}
	return
}

func waitValidateTaskRunDone(t *testing.T, c *clients, taskRunName string) {
	// Per test basis
	err := WaitForTaskRunState(c, taskRunName, Succeed(taskRunName), taskRunName)

	if err != nil {
		t.Fatalf("Failed waiting for task run done: %v", err)
	}
	return
}

// SubstituteEnv substitutes docker repos and bucket paths from the system
// environment for input to allow tests on local clusters. It also updates the
// namespace for ServiceAccounts so that they work under test
func SubstituteEnv(input []byte, namespace string) ([]byte, error) {
	val, ok := os.LookupEnv("KO_DOCKER_REPO")
	var output []byte
	if ok {
		re := regexp.MustCompile(DEFAULT_KO_DOCKER_REPO)
		output = re.ReplaceAll(input, []byte(val))
	} else {
		return nil, errors.New("KO_DOCKER_REPO is not set")
	}

	re := regexp.MustCompile(DEFAULT_NAMESPACE)
	output = re.ReplaceAll(output, []byte(strings.ReplaceAll(DEFAULT_NAMESPACE, "default", namespace)))
	return output, nil
}

// KoCreate wraps the ko binary and invokes `ko create` for input within
// namespace
func KoCreate(input []byte, namespace string) ([]byte, error) {
	cmd := exec.Command("ko", "create", "-n", namespace, "-f", "-")
	cmd.Stdin = strings.NewReader(string(input))

	out, err := cmd.CombinedOutput()
	return out, err
}

// DeleteClusterTask removes a single clustertask by name using provided
// clientset. Test state is used for logging. DeleteClusterTask does not wait
// for the clustertask to be deleted, so it is still possible to have name
// conflicts during test
func DeleteClusterTask(t *testing.T, c *clients, name string) {
	t.Logf("Deleting clustertask %s", name)
	err := c.ClusterTaskClient.Delete(name, &metav1.DeleteOptions{})
	if err != nil {
		t.Fatalf("Failed to delete clustertask: %v", err)
	}
}

type waitFunc func(t *testing.T, c *clients, name string)

func exampleTest(path string, waitValidateFunc waitFunc, kind string) func(t *testing.T) {
	return func(t *testing.T) {
		SkipIfExcluded(t)

		t.Parallel()

		// Setup unique namespaces for each test so they can run in complete
		// isolation
		c, namespace := setup(t)

		knativetest.CleanupOnInterrupt(func() { tearDown(t, c, namespace) }, t.Logf)
		defer tearDown(t, c, namespace)

		inputExample, err := ioutil.ReadFile(path)

		if err != nil {
			t.Fatalf("Error reading file: %v", err)
		}

		subbedInput, err := SubstituteEnv(inputExample, namespace)
		if err != nil {
			t.Skipf("Couldn't substitute environment: %v", err)
		}

		out, err := KoCreate(subbedInput, namespace)
		if err != nil {
			t.Fatalf("%s Output: %s", err, out)
		}

		// Parse from KoCreate for now
		name, err := GetCreatedTektonCrd(out, kind)
		if name == "" {
			// Nothing to check from ko create, this is not a taskrun or pipeline
			// run. Some examples in the directory do not directly output a TaskRun
			// or PipelineRun (ie. task-result.yaml).
			t.Skipf("pipelinerun or taskrun not created for %s", path)
		} else if err != nil {
			t.Fatalf("Failed to get created Tekton CRD of kind %s: %v", kind, err)
		}

		// NOTE: If an example creates more than one clustertask, they will not all
		// be cleaned up
		clustertask, err := GetCreatedTektonCrd(out, "clustertask")
		if clustertask != "" {
			knativetest.CleanupOnInterrupt(func() { DeleteClusterTask(t, c, clustertask) }, t.Logf)
			defer DeleteClusterTask(t, c, clustertask)
		} else if err != nil {
			t.Fatalf("Failed to get created clustertask: %v", err)
		}

		waitValidateFunc(t, c, name)
	}
}

func getExamplePaths(t *testing.T, dir string) []string {
	var examplePaths []string

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			t.Fatalf("couldn't walk path %s: %v", path, err)
		}
		// Do not append root and any other folders named "examples"
		if info.Name() == "examples" && info.IsDir() {
			return nil
		}
		if info.Name() == "no-ci" && info.IsDir() {
			return filepath.SkipDir
		}
		if info.IsDir() == false && filepath.Ext(info.Name()) == ".yaml" {
			// Ignore test matching the regexp in the TEST_EXAMPLES_IGNORES
			// environement variable.
			val, ok := os.LookupEnv("TEST_EXAMPLES_IGNORES")
			if ok {
				re := regexp.MustCompile(val)
				submatch := re.FindSubmatch([]byte(path))
				if submatch != nil {
					t.Logf("Skipping test %s", path)
					return nil
				}
			}
			t.Logf("Adding test %s", path)
			examplePaths = append(examplePaths, path)
			return nil
		}
		return nil
	})
	if err != nil {
		t.Fatalf("couldn't walk example directory %s: %v", dir, err)
	}

	return examplePaths
}

func extractTestName(baseDir string, path string) string {
	re := regexp.MustCompile(baseDir + "/(.+).yaml")
	submatch := re.FindSubmatch([]byte(path))
	if submatch == nil {
		return path
	}
	return string(submatch[1])
}

func TestExamples(t *testing.T) {
	baseDir := "../examples"

	t.Parallel()
	for _, path := range getExamplePaths(t, baseDir) {
		testName := extractTestName(baseDir, path)
		waitValidateFunc := waitValidatePipelineRunDone
		kind := "pipelinerun"

		if strings.Contains(path, "/taskruns/") {
			waitValidateFunc = waitValidateTaskRunDone
			kind = "taskrun"
		}

		t.Run(testName, exampleTest(path, waitValidateFunc, kind))
	}
}
