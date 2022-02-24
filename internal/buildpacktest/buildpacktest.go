// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package buildpacktest contains utilities for testing buildpacks that
// use the `gcpbuildpack` package.
package buildpacktest

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/buildpacks/internal/buildpacktestenv"
	"github.com/GoogleCloudPlatform/buildpacks/pkg/env"
	"github.com/GoogleCloudPlatform/buildpacks/pkg/fileutil"
	gcp "github.com/GoogleCloudPlatform/buildpacks/pkg/gcpbuildpack"
)

var (
	flagTestData string // Path to directory or archive containing source test data.
)

// defineFlags sets up flags that control the behavior of the test runner.
func defineFlags() {
	flag.StringVar(&flagTestData, "test-data", "", "Location of the test data files.")
}

func init() {
	defineFlags()
}

type buildpackPhase string

const (
	detectPhase buildpackPhase = "Detect"
	buildPhase  buildpackPhase = "Build"

	// runTestAsHelperProcessEnv is an env variable that signals the current
	// golang test being run is actually a child process of the main golang
	// test process. The child process is used to execute the buildpack phase
	// under test without impacting the main test process. The env value is
	// the buildpackPhase to execute.
	//
	// This is similar to how the exec package tests exec.Command
	// (see https://golang.org/src/os/exec/exec_test.go).
	runTestAsHelperProcessEnv = "RUN_TEST_AS_HELPER_PROCESS"
)

type config struct {
	buildpackPhase buildpackPhase
	buildFn        gcp.BuildFn
	detectFn       gcp.DetectFn
	testName       string
	files          map[string]string
	envs           []string
	stack          string
	want           int
	appPath        string
	mockProcessMap map[string]*buildpacktestenv.MockProcess
}

// Result encapsulates the result of a buildpack phase ran as a child process.
type Result struct {
	// Output is the combined stdout and stderr of executing the build function
	// or detect function in a child process. Almost all buildpack output is
	// logged to stderr. Debug mode is on for tests, so all ctx.Exec commands
	// will be logged to stderr. Stdout and stderr from ctx.Exec calls end up
	// being printed to stderr by the `gcpbuildpack` package.
	//
	// Some extraneous Go test output appears in the Output here due to
	// re-using the main test binary as the entrypoint for the child process.
	Output string
	// ExitCode is the exit code of the child process that ran the buildpack
	// function.
	ExitCode int
}

// CommandExecuted returns true if the command was executed using ctx.Exec, otherwise returns false.
func (r *Result) CommandExecuted(command string) bool {
	re := regexp.MustCompile(fmt.Sprintf(`(?s)Running.*%s.*Done`, command))
	return re.FindString(r.Output) != ""
}

// Option is a type for buildpack test options.
type Option func(cfg *config)

// WithTestName specifies the test case name if a table-driven test is being
// used. This is important when invoking the test binary again as a child
// process to execute the buildpack phase.
func WithTestName(testName string) Option {
	return func(cfg *config) {
		cfg.testName = testName
	}
}

// WithApp specifies an app, by directory name, to build from testdata.
func WithApp(appName string) Option {
	return func(cfg *config) {
		cfg.appPath = appName
	}
}

// WithEnvs specifies env vars to set for the buildpack test.
func WithEnvs(envs ...string) Option {
	return func(cfg *config) {
		cfg.envs = envs
	}
}

// WithExecMock mocks the behavior of a shell command executed by a
// ctx.Exec call. `commandRegex` is the command to mock; the regex must match
// the full command that would have been executed, though it
// does not have to be the beginning of the command. `stdout` is what will
// be printed to stdout, `stderr` is what wiil be printed totderr. `exitCode`
// will be the exit code of the command.
//
// All commands executed through ctx.Exec have stdout and stderr redirected
// to the return parameters of ctx.Exec. However, the combined output ends
// up being logged to stderr of the parent process. The stderr of executing
// detectFn or buildFn can be searched for the stdout or stderr of any ctx.Exec
// mocks.
func WithExecMock(commandRegex string, opts ...ExecMockOptions) Option {
	return func(cfg *config) {
		if cfg.mockProcessMap == nil {
			cfg.mockProcessMap = map[string]*buildpacktestenv.MockProcess{}
		}
		mp := &buildpacktestenv.MockProcess{
			Stdout:   "",
			Stderr:   "",
			ExitCode: 0,
		}
		for _, o := range opts {
			o(mp)
		}
		cfg.mockProcessMap[commandRegex] = mp
	}
}

// ExecMockOptions are options that configure the behavior of the mock command
// that replaces ctx.Exec calls.
type ExecMockOptions func(*buildpacktestenv.MockProcess)

// MockStdout configures what a mocked command prints to stdout.
func MockStdout(msg string) ExecMockOptions {
	return func(mp *buildpacktestenv.MockProcess) {
		mp.Stdout = msg
	}
}

// MockStderr configures what a mocked command prints to stderr.
func MockStderr(msg string) ExecMockOptions {
	return func(mp *buildpacktestenv.MockProcess) {
		mp.Stderr = msg
	}
}

// MockExitCode configures what a mocked command uses as the exit code.
func MockExitCode(code int) ExecMockOptions {
	return func(mp *buildpacktestenv.MockProcess) {
		mp.ExitCode = code
	}
}

// TestDetect is a helper for testing a buildpack's implementation of /bin/detect.
// This MUST be called from a test function with the name `func TestDetect(t *testing.T)`
// A child process will be started that looks for that test name. The child
// process will run a buildpack phase instead of the test again, however.
func TestDetect(t *testing.T, detectFn gcp.DetectFn, testName string, files map[string]string, envs []string, want int) {
	TestDetectWithStack(t, detectFn, testName, files, envs, "com.stack", want)
}

// TestDetectWithStack is a helper for testing a buildpack's implementation of
// /bin/detect which allows setting a custom stack name. This MUST be called
// from a test function with the stub `func TestDetectWithStack(t *testing.T)`.
// A child process will be started that looks for that test name. The child
// process will run a buildpack phase instead of the test again, however.
func TestDetectWithStack(t *testing.T, detectFn gcp.DetectFn, testName string, files map[string]string, envs []string, stack string, want int) {
	result, err := runBuildpackPhaseForTest(t, &config{
		buildpackPhase: detectPhase,
		detectFn:       detectFn,
		testName:       testName,
		files:          files,
		envs:           envs,
		stack:          stack,
		want:           want,
	})

	if result.ExitCode != want {
		t.Errorf("unexpected exit status %d, want %d", result.ExitCode, want)
		t.Errorf("\ncombined stdout, stderr: %s", result.Output)
	}

	if err == nil && want != 0 {
		t.Errorf("unexpected exit status 0, want %d", want)
		t.Errorf("\ncombined stdout, stderr: %s", result.Output)
	}
}

// RunBuild is a helper for testing a buildpack's implementation of /bin/build.
// This MUST be called from a test function with the stub `func TestBuild(t *testing.T)`
// A child process will be started that looks for that test name. The child
// process will run a buildpack phase instead of the test again, however.
func RunBuild(t *testing.T, buildFn gcp.BuildFn, opts ...Option) (*Result, error) {
	t.Helper()
	cfg := &config{
		buildpackPhase: buildPhase,
		buildFn:        buildFn,
	}

	for _, o := range opts {
		o(cfg)
	}

	return runBuildpackPhaseForTest(t, cfg)
}

// runBuildpackPhaseForTest runs a buildpack phase as a separate child process.
// A child process is used to avoid the test suite itself being terminated by
// errant calls to os.Exit() in the buildpack.
func runBuildpackPhaseForTest(t *testing.T, cfg *config) (*Result, error) {
	testDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting working directory: %v", err)
	}

	if bp := os.Getenv(runTestAsHelperProcessEnv); bp != "" {
		runBuildpackPhaseMain(t, cfg)
	} else {
		// Invoke buildpack phase in a separate process. This is done
		// by executing the current tests again in a separate process and adding
		// the env var that signals the buildpack phase should be run (args[0]
		// is the current running binary).
		testBinary := filepath.Join(testDir, os.Args[0])
		args := []string{fmt.Sprintf("-test.run=Test%s/^%s$", cfg.buildpackPhase, strings.ReplaceAll(cfg.testName, " ", "_"))}
		// Forward the `buildpacktest` flags to the child process.
		args = append(args, os.Args[1:]...)
		cmd := exec.Command(testBinary, args...)
		cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%s", runTestAsHelperProcessEnv, cfg.buildpackPhase))

		for _, e := range cfg.envs {
			cmd.Env = append(cmd.Env, e)
		}

		t.Logf("running command %v", cmd)

		output, err := cmd.CombinedOutput()
		exitCode := 0
		if e, ok := err.(*exec.ExitError); ok {
			exitCode = e.ExitCode()
		}
		result := &Result{
			// Almost all buildpack output is relogged to Stderr
			Output:   string(output),
			ExitCode: exitCode,
		}

		return result, err
	}

	return &Result{}, nil
}

// runBuildpackPhaseMain runs a buildpack phase. It is the equivalent
// of `func main()` for a helper process. To avoid confusion, it is written
// like the main of a standard Go app, using "log.Fatalf" in place of
// "t.Fatalf".
func runBuildpackPhaseMain(t *testing.T, cfg *config) {
	phasePassed, err := runBuildpackPhase(t, cfg)
	if err != nil {
		log.Fatalf("buildpack error: %v", err)
	}

	if cfg.buildpackPhase == detectPhase && !phasePassed {
		// mimic the libcnb exit code for when /bin/detect runs but does
		// not detect anything.
		os.Exit(100)
	}

	// Do not allow any other Go test validation to continue in the child
	// process.
	os.Exit(0)
}

func runBuildpackPhase(t *testing.T, cfg *config) (bool, error) {
	temps := buildpacktestenv.SetUpTempDirs(t)
	opts := []gcp.ContextOption{gcp.WithApplicationRoot(temps.CodeDir), gcp.WithBuildpackRoot(temps.BuildpackDir)}

	// Mock out calls to ctx.Exec, if specified
	if len(cfg.mockProcessMap) > 0 {
		mockProcessBinary, err := mockProcessBinaryPath()
		if err != nil {
			return false, fmt.Errorf("unable to locate mock process binary: %w", err)
		}
		eCmd := buildpacktestenv.NewMockExecCmd(t, mockProcessBinary, cfg.mockProcessMap)
		opts = append(opts, gcp.WithExecCmd(eCmd))
	}

	// Logs all ctx.Exec commands to stderr
	os.Setenv(env.DebugMode, "true")
	ctx := gcp.NewContext(opts...)

	if cfg.appPath != "" {
		// Copy apps from test data into temp code dir
		if err := fileutil.MaybeCopyPathContents(temps.CodeDir, filepath.Join(flagTestData, cfg.appPath), fileutil.AllPaths); err != nil {
			return false, fmt.Errorf("unable to copy app directory %q to %q: %v", cfg.appPath, temps.CodeDir, err)
		}
	}

	for f, c := range cfg.files {
		fn := filepath.Join(temps.CodeDir, f)

		if dir := path.Dir(fn); dir != "" {
			if err := os.MkdirAll(dir, 0744); err != nil {
				return false, fmt.Errorf("creating directory tree %s: %v", dir, err)
			}
		}

		if err := ioutil.WriteFile(fn, []byte(c), 0644); err != nil {
			return false, fmt.Errorf("writing file %s: %v", fn, err)
		}
	}

	if err := os.Chdir(temps.CodeDir); err != nil {
		return false, fmt.Errorf("changing to code dir %q: %v", temps.CodeDir, err)
	}

	if cfg.buildpackPhase == buildPhase {
		if err := cfg.buildFn(ctx); err != nil {
			return false, fmt.Errorf("build error: %v", err)
		}
	} else {
		detect, err := cfg.detectFn(ctx)
		if err != nil {
			return false, fmt.Errorf("detect error: %v", err)
		}

		// Mimics the exit code of libcnb library when the detect function
		// succeeds but does not pass detect.
		if !detect.Result().Pass {
			return false, nil
		}
	}

	return true, nil
}

// mockProcessBinaryPath returns the path to the mockprocess binary within
// the current build target's (a go_test) runtime files. The mockprocess
// binary comes bundled with the `buildpacktest` package, so it's expected
// to be where the buildpacktest package's location is placed.
func mockProcessBinaryPath() (string, error) {
	// Returns the file that would have been at the top frame of a stack
	// trace created from this line (this file itself).
	// {buildpacksRepo}/internal/buildpacktest/buildpacktest.go
	_, callingFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("unable to determine Go runtime information about calling file")
	}

	// {buildpacksRepo}/internal/buildpacktest
	callingDir := filepath.Dir(callingFile)

	buildpackTest := "internal/buildpacktest"
	// {buildpacksRepo}
	buildpacksRepo := strings.TrimSuffix(filepath.ToSlash(callingDir), buildpackTest)

	// Full path to currently executing test binary
	// {bazelRuntimeRoot}/{buildpacksRepo}/{relativePathToTestBinary}
	executingBinary := filepath.ToSlash(os.Args[0])

	// [{bazelRuntimeRoot}, {relativePathToTestBinary}]
	split := strings.Split(executingBinary, buildpacksRepo)
	if len(split) < 2 {
		return "", fmt.Errorf("unable to determine bazel runtime root, executing test binary: %q, inferred buildpacks repo path: %q, split result: %v", executingBinary, buildpacksRepo, split)
	}

	// {bazelRuntimeRoot}/{buildpacksRepo}/internal/buildpacktest/mockprocess/mockprocess
	mockProcessBinary := filepath.Join(split[0], buildpacksRepo, buildpackTest, "mockprocess", "mockprocess")
	return filepath.FromSlash(mockProcessBinary), nil
}
