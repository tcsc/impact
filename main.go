package main

import (
	"bufio"
	"errors"
	"fmt"
	pflag "github.com/ogier/pflag"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type testResult int

const (
	fetchTimedOut       testResult = iota
	fetchFailed         testResult = iota
	failedPrePatchTest  testResult = iota
	failedPostPatchTest testResult = iota
	failedUnexpectedly  testResult = iota
	patchFailed         testResult = iota
	passed              testResult = iota
)

func (e testResult) Error() string {
	switch e {
	case fetchTimedOut:
		return "Fetch timed out"

	case fetchFailed:
		return "Fetch failed"

	case failedPrePatchTest:
		return "Failed pre-patch testing"

	case failedPostPatchTest:
		return "Failed post-patch testing"

	case failedUnexpectedly:
		return "Failed unexpectedly"

	case patchFailed:
		return "Patch failed to apply"

	case passed:
		return "Passed"

	default:
		panic(fmt.Sprintf("Invalid test result value: %d", e))
	}
}

type pkg struct {
	index int
	slug  string
}

type reply struct {
	pkg
	result testResult
	err_   error
}

func fetchCode(idx int, p pkg, dir string, timeout time.Duration, env []string) testResult {
	fmt.Printf("%04d: %d Fetching code...\n", p.index, idx)
	get := exec.Command("go", "get", "-t", p.slug)
	get.Env = env
	get.Stdout = os.Stdout
	get.Stderr = os.Stderr

	ch := make(chan error, 1)
	go func() { ch <- get.Run() }()
	select {
	case err := <-ch:
		if err == nil {
			return passed
		} else {
			return fetchFailed
		}

	case <-time.After(timeout):
		fmt.Printf("%04d: %d Timed out\n", p.index, idx)
		get.Process.Kill()
		return fetchTimedOut
	}
}

func runTests(p pkg, logfile, dir string, env []string) error {
	file, err := os.Create(path.Join(dir, logfile))
	if err != nil {
		return err
	}
	defer file.Close()

	test := exec.Command("go", "test", "-v", p.slug)
	test.Stdout = file
	test.Env = env

	return test.Run()
}

func applyPatch(patchFile, dir string, args *arguments) error {
	patchFile, err := filepath.Abs(patchFile)
	if err != nil {
		return err
	}

	cmd := exec.Command("patch", "-p1",
		"-d", path.Join(dir, "src", args.packageName),
		"-i", patchFile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func getEnv() []string {
	env := os.Environ()
	result := make([]string, 0, len(env))
	for _, s := range env {
		if !strings.HasPrefix(s, "GOPATH=") {
			result = append(result, s)
		}
	}
	return result
}

func quickCheck(idx int, p pkg, dir string, args arguments) (testResult, error) {
	fmt.Printf("%04d: %d Checking out %s into %s\n", p.index, idx, p.slug, dir)
	err := os.Mkdir(dir, 0755)
	if err != nil {
		return failedUnexpectedly, err
	}

	env := getEnv()
	env = append(env, fmt.Sprintf("GOPATH=%s", dir))

	result := fetchCode(idx, p, dir, args.fetchTimeout, env)
	if result != passed {
		fmt.Printf("%04d: %d Failed to fetch code: %s\n",
			p.index, idx, result.Error())
		return result, nil
	}

	fmt.Printf("%04d: %d Running pre-patch tests\n", p.index, idx)
	err = runTests(p, "pre-test.log", dir, env)
	if err != nil {
		fmt.Printf("%04d: %d Failed pre-patch tests. No further testing.\n", p.index, idx)
		return failedPrePatchTest, nil
	}

	fmt.Printf("%04d: %d Applying patch\n", p.index, idx)
	err = applyPatch("mock.patch", dir, &args)
	if err != nil {
		fmt.Printf("%04d: %d Failed to apply patch. Bailing our.\n", p.index, idx)
		return patchFailed, nil
	}

	fmt.Printf("%04d: %d Running post-patch tests\n", p.index, idx)
	err = runTests(p, "post-test.log", dir, env)
	if err != nil {
		fmt.Printf("%04d: %d Failed post-patch tests: %s.\n", p.index, idx, err.Error())
		return failedPostPatchTest, nil
	}

	fmt.Printf("%04d: %d Passed.\n", p.index, idx)

	return passed, nil
}

func loadPackageList(filename string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	pkgs := make([]string, 0)
	s := bufio.NewScanner(file)
	for s.Scan() {
		if s.Err() != nil {
			return nil, err
		}
		pkgs = append(pkgs, strings.TrimSpace(s.Text()))
	}

	return pkgs, nil
}

type arguments struct {
	fetchTimeout    time.Duration
	reportFile      string
	patchFile       string
	packageName     string
	packageListFile string
	concurrency     int
}

func parseArgs() (arguments, error) {
	var result arguments

	flags := pflag.NewFlagSet("Impact", pflag.ContinueOnError)
	flags.StringVarP(&result.packageName, "package", "p", "",
		"The package to test. Paths in the patch file must be relative to this.")
	flags.StringVarP(&result.packageListFile, "package-file", "f", "packages.txt",
		"The file containing the list of packages to test")
	flags.StringVarP(&result.patchFile, "delta", "d", "delta.patch",
		"A patch describing the change to test")
	flags.DurationVarP(&result.fetchTimeout, "timeout", "t", 60*time.Minute,
		"How long to wait for the source code fetch befor giving up.")
	flags.StringVarP(&result.reportFile, "report", "r", "report.txt",
		"How long to wait for the source code frtch befor giving up.")
	flags.IntVarP(&result.concurrency, "concurrency", "n", 8,
		"How many tests to run simultaneously")

	err := flags.Parse(os.Args[1:])
	if err != nil {
		return result, err
	}

	if result.packageName == "" {
		return result, errors.New("Must specify a package to test")
	}

	result.packageListFile, err = filepath.Abs(result.packageListFile)
	if err != nil {
		return result, err
	}

	result.patchFile, err = filepath.Abs(result.patchFile)
	if err != nil {
		return result, err
	}

	result.reportFile, err = filepath.Abs(result.reportFile)

	return result, err
}

func getResult(result map[testResult]int, r testResult) int {
	if val, ok := result[r]; ok {
		return val
	} else {
		return 0
	}
}

func resultCode(r testResult) string {
	switch r {
	case fetchTimedOut:
		return "FT"

	case fetchFailed:
		return "FF"

	case failedPrePatchTest:
		return "F1"

	case failedPostPatchTest:
		return "F2"

	case failedUnexpectedly:
		return "F?"

	case patchFailed:
		return "FP"

	case passed:
		return "P!"

	default:
		panic(fmt.Sprintf("Invalid test result value: %d", r))
	}
}

func writeReport(filename string, results []reply) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	for _, r := range results {
		fmt.Fprintf(file, "%04d, %s, %s, ", r.index, resultCode(r.result), r.slug)
		if r.err_ != nil {
			fmt.Fprintf(file, `"%s"`, r.err_.Error())
		}
		fmt.Fprintln(file, "")
	}

	return nil
}

func run() int {
	args, err := parseArgs()
	if err != nil {
		fmt.Println(err.Error())
		return 1
	}

	fmt.Printf("Loading packages from %s\n", args.packageListFile)
	packages, err := loadPackageList(args.packageListFile)
	if err != nil {
		fmt.Printf("Failed to load pkgs: %s\n", err.Error())
		return 1
	}

	pkgChan := make(chan pkg, 10)
	rpyChan := make(chan reply, 10)
	done := make(chan os.Signal, 1)

	signal.Notify(done, os.Interrupt)

	results := make([]reply, 0, len(packages))
	summary := make(map[testResult]int)

	fmt.Printf("Testing %d packages\n", len(packages))

	collate := func() {
		replies := 0

		for reply := range rpyChan {
			fmt.Printf("%04d: Processing result\n", reply.index)

			results = append(results, reply)

			count, _ := summary[reply.result]
			summary[reply.result] = count + 1

			replies++
			fmt.Printf("Processed %d/%d replies\n", replies, len(packages))

			if replies == len(packages) {
				done <- syscall.SIGQUIT
			}
		}
	}
	go collate()

	test := func(i int) {
		for pkgInfo := range pkgChan {
			workdir, err := filepath.Abs(fmt.Sprintf("%04d", pkgInfo.index))
			result := failedUnexpectedly
			if err == nil {
				result, err = quickCheck(i, pkgInfo, workdir, args)
			}
			rpyChan <- reply{pkg: pkgInfo, result: result, err_: err}
		}
	}

	// fork the workers
	for i := 0; i < args.concurrency; i++ {
		go test(i)
	}

	// start feeding the packages to the workers...
	for i, slug := range packages {
		pkgChan <- pkg{index: i, slug: slug}
	}

	// wait for the user to signal "time's up"
	<-done

	fmt.Printf("Tested %d packages\n", len(packages))
	fmt.Printf("\t%d fetch timed out\n", getResult(summary, fetchTimedOut))
	fmt.Printf("\t%d failed fetching\n", getResult(summary, fetchFailed))
	fmt.Printf("\t%d failed pre-patch testing\n", getResult(summary, failedPrePatchTest))
	fmt.Printf("\t%d failed post-patch testing\n", getResult(summary, failedPostPatchTest))
	fmt.Printf("\t%d failed in unexpected ways\n", getResult(summary, failedUnexpectedly))
	fmt.Printf("\t%d passed testing\n", getResult(summary, passed))

	err = writeReport(args.reportFile, results)
	if err != nil {
		fmt.Printf("Failed to write test report: %s\n", err.Error())
		return 1
	}

	return 0
}

func main() {
	os.Exit(run())
}
