package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
)

var prePatchTestFailed = errors.New("pre test failed")
var postPatchTestFailed = errors.New("post test failed")

func fetchCode(p pkg, dir string, env []string) error {
	fmt.Printf("%04d: Fetching code...\n", p.index)
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

	test := exec.Command("go", "test", "-v", fmt.Sprintf("%s/...", p.slug))
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

func quickCheck(p pkg, dir string) error {
	fmt.Printf("%04d: Checking out %s into %s\n", p.index, p.slug, dir)
	err := os.Mkdir(dir, 0755)
	if err != nil {
		return err
	}

	env := os.Environ()
	env = append(env, fmt.Sprintf("GOPATH=%s", dir))

	err = fetchCode(p, dir, env)
	if err != nil {
		fmt.Printf("%04d: Failed to fetch code (counds as a pre-patch failure): %s\n",
			p.index, err.Error())
		return prePatchTestFailed
	}

	fmt.Printf("%04d: Running pre-patch tests\n", p.index)
	err = runTests(p, "pre-test.log", dir, env)
	if err != nil {
		fmt.Printf("%04d: Failed pre-patch tests. No further testing.\n", p.index)
		return prePatchTestFailed
	}

	fmt.Printf("%04d: Applying patch\n", p.index)
	err = applyPatch("mock.patch", dir)
	if err != nil {
		fmt.Printf("%04d: Failed to apply patch. Bailing our.\n", p.index)
		return err
	}

	fmt.Printf("%04d: Running post-patch tests\n", p.index)
	err = runTests(p, "post-test.log", dir, env)
	if err != nil {
		fmt.Printf("%04d: Failed post-patch tests: %s.\n", p.index, err.Error())
		return postPatchTestFailed
	}

	fmt.Printf("%04d: Passed.\n", p.index)

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

func main() {
	packages := []string{
		"github.com/BlueDragonX/dockerclient/mockclient",
		"github.com/CenturyLinkLabs/watchtower/container/mockclient",
		"github.com/CiscoCloud/mesos-consul/mesos/zkdetect",
		"github.com/yieldr/go-log/log/logstream",
		"github.com/Hearst-DD/goib/mocks",
	}

	pkgChan := make(chan pkg, 10)
	rpyChan := make(chan reply, 10)
	done := make(chan bool, 1)

	passed := make([]pkg, 0)
	failedPreTest := make([]pkg, 0)
	failedPostTest := make([]pkg, 0)
	failedUnexpectedly := make([]pkg, 0)

	collate := func() {
		replies := 0
		for reply := range rpyChan {
			pkgInfo := pkg{index: reply.index, slug: reply.slug}
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
				done <- true
				break
			}
		}
	}
	go collate()

	test := func() {
		for pkgInfo := range pkgChan {
			rpy := reply{pkg: pkgInfo, err: nil}

			workdir, err := filepath.Abs(fmt.Sprintf("%04d", pkgInfo.index))
			if err == nil {
				err = quickCheck(pkgInfo, workdir)
			}
			rpy.err = err
			rpyChan <- rpy
		}
	}
	go test()
	go test()
	go test()
	go test()

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
