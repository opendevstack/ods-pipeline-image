package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// runCmdInDir invokes exe with given args and env. Stdout and stderr
// are streamed to outWriter and errWriter, respectively.
// If dir is non-empty, the workdir of exe will be set to it.
func runCmdInDir(exe string, args []string, env []string, dir string, outWriter, errWriter io.Writer) error {
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), env...)
	cmdStderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("connect stderr pipe: %w", err)
	}
	cmdStdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("connect stdout pipe: %w", err)
	}
	if dir != "" {
		cmd.Dir = dir
	}
	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("start cmd: %w", err)
	}

	err = collectOutput(cmdStdout, cmdStderr, outWriter, errWriter)
	if err != nil {
		return fmt.Errorf("collect output: %w", err)
	}

	return cmd.Wait()
}

func collectOutput(rcStdout, rcStderr io.ReadCloser, wStdout, wStderr io.Writer) error {
	var stdoutErr, stderrErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		stdoutErr = scan(rcStdout, wStdout)
		wg.Done()
	}()
	stderrErr = scan(rcStderr, wStderr)
	wg.Wait()
	if stdoutErr != nil || stderrErr != nil {
		return fmt.Errorf("scan stdout = %s, scan stderr = %s", stdoutErr, stderrErr)
	}
	return nil
}

func scan(rc io.ReadCloser, w io.Writer) error {
	scanner := bufio.NewScanner(rc)
	for scanner.Scan() {
		fmt.Fprintln(w, scanner.Text())
	}
	return scanner.Err()
}
