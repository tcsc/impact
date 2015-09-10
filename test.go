package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

var prePatchTestFailed = errors.New("pre test failed")
var postPatchTestFailed = errors.New("post test failed")

func fetchCode(idx int, p pkg, dir string, env []string) error {
	fmt.Printf("%04d: %d Fetching code...\n", p.index, idx)
	get := exec.Command("go", "get", "-t", p.slug)
	get.Env = env
	get.Stdout = os.Stdout
	get.Stderr = os.Stderr
	return get.Run()
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

func applyPatch(patchFile, dir string) error {
	patchFile, err := filepath.Abs(patchFile)
	if err != nil {
		return err
	}

	cmd := exec.Command("patch", "-p1",
		"-d", path.Join(dir, "src", "github.com/stretchr/testify"),
		"-i", patchFile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func quickCheck(idx int, p pkg, dir string) error {
	fmt.Printf("%04d: %d Checking out %s into %s\n", p.index, idx, p.slug, dir)
	err := os.Mkdir(dir, 0755)
	if err != nil {
		return err
	}

	env := os.Environ()
	env = append(env, fmt.Sprintf("GOPATH=%s", dir))

	err = fetchCode(idx, p, dir, env)
	if err != nil {
		fmt.Printf("%04d: %d Failed to fetch code (counds as a pre-patch failure): %s\n",
			p.index, idx, err.Error())
		return prePatchTestFailed
	}

	fmt.Printf("%04d: %d Running pre-patch tests\n", p.index, idx)
	err = runTests(p, "pre-test.log", dir, env)
	if err != nil {
		fmt.Printf("%04d: %d Failed pre-patch tests. No further testing.\n", p.index, idx)
		return prePatchTestFailed
	}

	fmt.Printf("%04d: %d Applying patch\n", p.index, idx)
	err = applyPatch("mock.patch", dir)
	if err != nil {
		fmt.Printf("%04d: %d Failed to apply patch. Bailing our.\n", p.index, idx)
		return err
	}

	fmt.Printf("%04d: %d Running post-patch tests\n", p.index, idx)
	err = runTests(p, "post-test.log", dir, env)
	if err != nil {
		fmt.Printf("%04d: %d Failed post-patch tests: %s.\n", p.index, idx, err.Error())
		return postPatchTestFailed
	}

	fmt.Printf("%04d: %d Passed.\n", p.index, idx)

	return nil
}

type pkg struct {
	index int
	slug  string
}

type reply struct {
	pkg
	err error
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

func main() {
	packages, err := loadPackageList("pkgs.txt")
	if err != nil {
		fmt.Printf("Failed to load pkgs: %s\n", err.Error())
		os.Exit(1)
	}

	pkgChan := make(chan pkg, 10)
	rpyChan := make(chan reply, 10)
	done := make(chan os.Signal, 1)

	signal.Notify(done, os.Interrupt)

	passed := make([]pkg, 0)
	failedPreTest := make([]pkg, 0)
	failedPostTest := make([]pkg, 0)
	failedUnexpectedly := make([]pkg, 0)

	collate := func() {
		replies := 0
		for reply := range rpyChan {
			pkgInfo := pkg{index: reply.index, slug: reply.slug}
			fmt.Printf("%04d: Processing result\n", reply.index)

			switch reply.err {
			case prePatchTestFailed:
				failedPreTest = append(failedPreTest, pkgInfo)

			case postPatchTestFailed:
				failedPostTest = append(failedPostTest, pkgInfo)

			case nil:
				passed = append(passed, pkgInfo)

			default:
				fmt.Printf("%04d: failed for unexpected reasons: %s\n",
					reply.index, reply.err.Error())

				failedUnexpectedly = append(failedUnexpectedly, pkgInfo)
			}

			replies++
			if replies == len(packages) {
				fmt.Println("Signalling completion")
				done <- syscall.SIGQUIT
				break
			}
		}
	}
	go collate()

	test := func(i int) {
		for pkgInfo := range pkgChan {
			rpy := reply{pkg: pkgInfo, err: nil}

			workdir, err := filepath.Abs(fmt.Sprintf("%04d", pkgInfo.index))
			if err == nil {
				err = quickCheck(i, pkgInfo, workdir)
			}
			rpy.err = err
			rpyChan <- rpy
		}
	}

	for i := 0; i < 8; i++ {
		go test(i)
	}

	for i, slug := range packages {
		pkgChan <- pkg{index: i, slug: slug}
	}

	fmt.Printf("Waiting for test completion...\n")
	<-done

	fmt.Printf("Tested %d packages\n", len(packages))

	fmt.Printf("\t%d failed pre-patch testing\n", len(failedPreTest))
	for _, p := range failedPreTest {
		fmt.Printf("\t\t%04d: %s\n", p.index, p.slug)
	}

	fmt.Printf("\t%d failed post-patch testing\n", len(failedPostTest))
	for _, p := range failedPostTest {
		fmt.Printf("\t\t%04d: %s\n", p.index, p.slug)
	}

	fmt.Printf("\t%d failed in unexpected ways\n", len(failedUnexpectedly))
	for _, p := range failedUnexpectedly {
		fmt.Printf("\t\t%04d: %s\n", p.index, p.slug)
	}

	fmt.Printf("\t%d passed testing\n", len(passed))

	os.Exit(0)
}
